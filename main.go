package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/crypto/ssh"
)

const (
	localWatchDir  = "./audios"
	remoteHost     = "20.127.212.253"
	remoteUser     = "speaksense"
	remoteKeyPath  = "vm-speaksense-eus-dev_key.pem"
	remoteAudioDir = "/home/speaksense/whisper-gpu-test-paralel/audios"
	remoteTransDir = "/home/speaksense/whisper-gpu-test-paralel/transcricoes"
	remoteWorkDir  = "/home/speaksense/whisper-gpu-test-paralel"
	remoteScript   = "python3 parallel_transcribe.py"
	localOldDir    = "./audios_old"
	localTransDir  = "./transcricoes"
	mongoURI       = "mongodb://root:STInfra123@10.0.68.120:27017/transcription?authSource=transcriptions"
	mongoDB        = "transcription"
	mongoColl      = "transcriptions"
)

type TranscriptionDoc struct {
	FileName    string                 `bson:"file_name"`
	Content     string                 `bson:"content"`
	ProcessedAt time.Time              `bson:"processed_at"`
	AudioPath   string                 `bson:"audio_path"`
	Metadata    map[string]interface{} `bson:"metadata,omitempty"`
}

var mongoClient *mongo.Client

func initMongoClient() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		return err
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		return err
	}

	mongoClient = client
	log.Println("Conectado ao MongoDB com sucesso!")
	return nil
}

func main() {
	// 1. Garantir que as pastas locais existem
	for _, dir := range []string{localWatchDir, localOldDir, localTransDir} {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			err := os.MkdirAll(dir, 0755)
			if err != nil {
				log.Fatalf("Erro ao criar diretório local %s: %v", dir, err)
			}
		}
	}

	// 2. Inicializar MongoDB
	err := initMongoClient()
	if err != nil {
		log.Printf("Aviso: Não foi possível conectar ao MongoDB (o fluxo continuará sem salvar no banco): %v", err)
	}

	// 3. Loop de Polling (Mais confiável para WSL2 /mnt/c)
	log.Printf("Monitorando a pasta (polling): %s", localWatchDir)
	processedFiles := make(map[string]bool)

	for {
		files, err := os.ReadDir(localWatchDir)
		if err != nil {
			log.Printf("Erro ao ler diretório: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}

			fileName := f.Name()
			if !processedFiles[fileName] {
				ext := strings.ToLower(filepath.Ext(fileName))

				if ext == ".mp3" || ext == ".wav" || ext == ".m4a" {
					fullPath := filepath.Join(localWatchDir, fileName)
					log.Printf(">>> NOVO ÁUDIO DETECTADO: %s", fileName)

					// Pequeno delay para garantir que o arquivo foi totalmente escrito
					time.Sleep(1 * time.Second)

					if handleNewFile(fullPath) {
						processedFiles[fileName] = true
						moveToOld(fullPath)
					}
				}
			}
		}

		time.Sleep(3 * time.Second)
	}
}

func getSSHConfig() (*ssh.ClientConfig, error) {
	key, err := os.ReadFile(remoteKeyPath)
	if err != nil {
		return nil, fmt.Errorf("não foi possível ler a chave privada: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("não foi possível parsear a chave privada: %v", err)
	}

	config := &ssh.ClientConfig{
		User: remoteUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	return config, nil
}

func handleNewFile(filePath string) bool {
	fileName := filepath.Base(filePath)
	log.Printf("[%s] Iniciando processamento...", fileName)

	// Extrair ID para o DB2 (ex: 851077886 do arquivo 851077886.mp3)
	gravacaoID := strings.TrimSuffix(fileName, filepath.Ext(fileName))

	config, err := getSSHConfig()
	if err != nil {
		log.Printf("[%s] Erro na config SSH: %v", fileName, err)
		return false
	}

	// Conectar ao servidor
	client, err := ssh.Dial("tcp", remoteHost+":22", config)
	if err != nil {
		log.Printf("[%s] Erro ao conectar no servidor: %v", fileName, err)
		return false
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		log.Printf("[%s] Erro ao criar cliente SFTP: %v", fileName, err)
		return false
	}
	defer sftpClient.Close()

	// 1. Enviar arquivo via SFTP
	remoteAudioPath := filepath.Join(remoteAudioDir, fileName)
	remoteAudioPath = strings.ReplaceAll(remoteAudioPath, "\\", "/")
	err = uploadFile(sftpClient, filePath, remoteAudioPath)
	if err != nil {
		log.Printf("[%s] Erro no upload: %v", fileName, err)
		return false
	}

	// 2. Executar script remoto
	err = runRemoteScript(client)
	if err != nil {
		log.Printf("[%s] Erro ao executar script remoto: %v", fileName, err)
		return false
	}

	// 3. Baixar transcrição
	transFileName := fileName + ".txt"
	remoteTransPath := filepath.Join(remoteTransDir, transFileName)
	remoteTransPath = strings.ReplaceAll(remoteTransPath, "\\", "/")
	localTransPath := filepath.Join(localTransDir, transFileName)

	err = downloadFile(sftpClient, remoteTransPath, localTransPath)
	if err != nil {
		log.Printf("[%s] Erro ao baixar transcrição: %v", fileName, err)
		return false
	}

	// 4. Buscar Metadados no DB2 (Local via Python Bridge)
	log.Printf("[%s] Buscando metadados no DB2...", fileName)
	metadata := fetchDB2Metadata(gravacaoID)

	// 5. Salvar no MongoDB
	content, err := os.ReadFile(localTransPath)
	if err == nil {
		err = saveToMongo(fileName, string(content), localTransPath, metadata)
		if err != nil {
			log.Printf("[%s] Erro ao salvar no MongoDB: %v", fileName, err)
		} else {
			log.Printf("[%s] Transcrição salva no MongoDB com sucesso.", fileName)
		}
	} else {
		log.Printf("[%s] Erro ao ler transcrição baixada para salvar no banco: %v", fileName, err)
	}

	// 6. Limpar servidor
	log.Printf("[%s] Limpando arquivos no servidor...", fileName)
	sftpClient.Remove(remoteAudioPath)
	sftpClient.Remove(remoteTransPath)

	log.Printf("[%s] Processamento concluído com sucesso!", fileName)
	return true
}

func fetchDB2Metadata(gravacaoID string) map[string]interface{} {
	cmd := exec.Command("python3", "fetch_db2.py", gravacaoID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Aviso: Erro ao buscar no DB2 (Python): %v - %s", err, string(out))
		return nil
	}

	var meta map[string]interface{}
	err = json.Unmarshal(out, &meta)
	if err != nil {
		log.Printf("Aviso: Erro ao decodificar JSON do DB2: %v", err)
		return nil
	}

	return meta
}

func saveToMongo(fileName, content, audioPath string, metadata map[string]interface{}) error {
	if mongoClient == nil {
		return fmt.Errorf("cliente MongoDB não inicializado")
	}

	collection := mongoClient.Database(mongoDB).Collection(mongoColl)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doc := TranscriptionDoc{
		FileName:    fileName,
		Content:     content,
		ProcessedAt: time.Now(),
		AudioPath:   audioPath,
		Metadata:    metadata,
	}

	_, err := collection.InsertOne(ctx, doc)
	return err
}

func moveToOld(filePath string) {
	fileName := filepath.Base(filePath)
	destPath := filepath.Join(localOldDir, fileName)

	err := os.Rename(filePath, destPath)
	if err != nil {
		log.Printf("Erro ao mover arquivo para audios_old: %v", err)
		return
	}
	log.Printf("Arquivo movido localmente para: %s", destPath)
}

func uploadFile(sftpClient *sftp.Client, localPath, remotePath string) error {
	srcFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("erro ao abrir arquivo local: %v", err)
	}
	defer srcFile.Close()

	dstFile, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("erro ao criar arquivo remoto: %v", err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("erro ao copiar conteúdo: %v", err)
	}

	log.Printf("Arquivo enviado: %s", remotePath)
	return nil
}

func downloadFile(sftpClient *sftp.Client, remotePath, localPath string) error {
	srcFile, err := sftpClient.Open(remotePath)
	if err != nil {
		return fmt.Errorf("erro ao abrir arquivo remoto: %v", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("erro ao criar arquivo local: %v", err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("erro ao copiar conteúdo: %v", err)
	}

	log.Printf("Transcrição baixada para: %s", localPath)
	return nil
}

func runRemoteScript(sshClient *ssh.Client) error {
	session, err := sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("erro ao criar sessão SSH: %v", err)
	}
	defer session.Close()

	fullCommand := fmt.Sprintf("cd %s && %s", remoteWorkDir, remoteScript)
	log.Printf("Executando no servidor: %s", fullCommand)

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	err = session.Run(fullCommand)
	if err != nil {
		return fmt.Errorf("erro ao executar comando: %v", err)
	}

	return nil
}
