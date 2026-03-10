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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	localWatchDir  = "./audios"
	stage2Upload   = "./temp/2_upload"
	stage3Gpu      = "./temp/3_gpu"
	stage4Download = "./temp/4_done"
	localOldDir    = "./audios_old"
	localTransDir  = "./transcricoes"

	remoteHost     = "20.127.212.253"
	remoteUser     = "speaksense"
	remoteKeyPath  = "vm-speaksense-eus-dev_key.pem"
	remoteAudioDir = "/home/speaksense/whisper-gpu-test-paralel/audios"
	remoteTempDir  = "/home/speaksense/whisper-gpu-test-paralel/audios_temp"
	remoteTransDir = "/home/speaksense/whisper-gpu-test-paralel/transcricoes"

	postgresURL = "postgres://srvbi:NbHo2WB8EyzatlPjmD1e@10.0.68.39:5433/transcriberdb"
)

type Segment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
}

var (
	dbPool           *pgxpool.Pool
	fileTimestamps   sync.Map
	activeProcessing sync.Map

	uploadSem   = make(chan struct{}, 4)
	downloadSem = make(chan struct{}, 4)
	sshSem      = make(chan struct{}, 8)
)

func initPostgres() error {
	db, err := pgxpool.New(context.Background(), postgresURL)
	if err != nil {
		return err
	}
	dbPool = db
	return nil
}

func main() {
	folders := []string{localWatchDir, stage2Upload, stage3Gpu, stage4Download, localOldDir, localTransDir}
	for _, d := range folders {
		os.MkdirAll(d, 0755)
	}

	_ = initPostgres()

	log.Printf(">>> INICIANDO SISTEMA V15.0 (WORD-DRIVEN ENGINE) <<<")

	go sourceProcessorService() // NOVO: Apenas move o MP3 original para upload
	go uploaderService()
	go gpuWatcherService()
	go downloaderService()
	go persisterService()

	select {}
}

// V12.0: Elimina o Split local. Apenas prepara o MP3 original para o Uploader.
func sourceProcessorService() {
	for {
		files, _ := os.ReadDir(localWatchDir)
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || !isAudio(name) {
				continue
			}
			if _, busy := activeProcessing.Load("s1_" + name); busy {
				continue
			}

			fullPath := filepath.Join(localWatchDir, name)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				continue
			}

			activeProcessing.Store("s1_"+name, true)
			go func(fn string) {
				defer activeProcessing.Delete("s1_" + fn)
				base := strings.TrimSuffix(fn, filepath.Ext(fn))
				recordTime(base, "start")

				log.Printf("[Source] %s: Preparando fonte original para upload...", base)
				// Movemos o MP3 original para a pasta de upload
				dest := filepath.Join(stage2Upload, fn)
				if err := os.Rename(filepath.Join(localWatchDir, fn), dest); err == nil {
					recordTime(base, "split_done") // Marcamos split_done como o tempo de preparo
					log.Printf("[Source] %s: OK", base)
				}
			}(name)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func uploaderService() {
	for {
		files, _ := os.ReadDir(stage2Upload)
		for _, f := range files {
			name := f.Name() // ex: 764818206.mp3
			if strings.HasSuffix(name, ".ready") {
				continue
			}
			base := strings.TrimSuffix(name, filepath.Ext(name))

			if _, busy := activeProcessing.Load("s2_" + base); busy {
				continue
			}

			activeProcessing.Store("s2_"+base, true)
			go func(b, fn string) {
				defer activeProcessing.Delete("s2_" + b)

				uploadSem <- struct{}{}
				recordTime(b, "upload_start")
				defer func() { <-uploadSem }()

				client, sftpC, err := connectSSH()
				if err != nil {
					log.Printf("[Uploader] ERRO SSH para %s: %v", b, err)
					return
				}
				defer client.Close()
				defer sftpC.Close()

				localPath := filepath.Join(stage2Upload, fn)
				tempPath := remoteTempDir + "/" + fn
				finalPath := remoteAudioDir + "/" + fn

				log.Printf("[Uploader] %s: Transmitindo fonte original (%s)...", b, fn)
				if err := uploadFile(sftpC, localPath, tempPath); err == nil {
					sftpC.Rename(tempPath, finalPath)
					recordTime(b, "upload_done")

					// Criamos o marcador .ready para o GPUWatcher
					os.WriteFile(filepath.Join(stage2Upload, b+".ready"), []byte(time.Now().String()), 0644)
					os.Rename(localPath, filepath.Join(localOldDir, fn))
					log.Printf("[Uploader] %s: OK", b)
				} else {
					log.Printf("[Uploader] ERRO no arquivo %s: %v", fn, err)
				}
			}(base, name)
		}
		time.Sleep(1 * time.Second)
	}
}

func gpuWatcherService() {
	for {
		files, _ := os.ReadDir(stage2Upload)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".ready") {
				continue
			}
			base := strings.TrimSuffix(f.Name(), ".ready")
			if _, busy := activeProcessing.Load("s3_" + base); busy {
				continue
			}

			activeProcessing.Store("s3_"+base, true)
			go func(b string, marker string) {
				defer activeProcessing.Delete("s3_" + b)

				client, sftpC, err := connectSSH()
				if err != nil {
					return
				}
				defer client.Close()
				defer sftpC.Close()

				// V12.2: O Python agora gera arquivos .wav.json após o split purista
				jsonA := remoteTransDir + "/" + b + "_atendente.wav.json"
				jsonC := remoteTransDir + "/" + b + "_cliente.wav.json"
				activeA := remoteTransDir + "/" + b + "_atendente.wav.active"
				activeC := remoteTransDir + "/" + b + "_cliente.wav.active"

				startedActive := false
				for {
					if !startedActive {
						_, errA := sftpC.Stat(activeA)
						_, errC := sftpC.Stat(activeC)
						if errA == nil || errC == nil {
							recordTime(b, "gpu_start_real")
							startedActive = true
							log.Printf("[GPUWatcher] %s: Ativo na GPU (Split Remoto + IA)", b)
						}
					}

					_, errA := sftpC.Stat(jsonA)
					_, errC := sftpC.Stat(jsonC)
					if errA == nil && errC == nil {
						break
					}
					time.Sleep(3 * time.Second)
				}

				if !startedActive {
					recordTime(b, "gpu_start_real")
				}
				recordTime(b, "gpu_done")
				os.WriteFile(filepath.Join(stage3Gpu, b+".ready"), []byte(time.Now().String()), 0644)
				os.Remove(marker)
				log.Printf("[GPUWatcher] %s: Concluído", b)
			}(base, filepath.Join(stage2Upload, f.Name()))
		}
		time.Sleep(1 * time.Second)
	}
}

func downloaderService() {
	for {
		files, _ := os.ReadDir(stage3Gpu)
		for _, f := range files {
			base := strings.TrimSuffix(f.Name(), ".ready")
			if _, busy := activeProcessing.Load("s4_" + base); busy {
				continue
			}

			activeProcessing.Store("s4_"+base, true)
			go func(b string, marker string) {
				defer activeProcessing.Delete("s4_" + b)
				downloadSem <- struct{}{}
				defer func() { <-downloadSem }()

				client, sftpC, err := connectSSH()
				if err != nil {
					return
				}
				defer client.Close()
				defer sftpC.Close()

				locA := filepath.Join(stage4Download, b+"_atendente.wav.json")
				locC := filepath.Join(stage4Download, b+"_cliente.wav.json")

				_ = downloadFile(sftpC, remoteTransDir+"/"+b+"_atendente.wav.json", locA)
				_ = downloadFile(sftpC, remoteTransDir+"/"+b+"_cliente.wav.json", locC)

				// Limpeza V12.2: remove os arquivos de áudio originais e os JSONs WAV
				sftpC.Remove(remoteAudioDir + "/" + b + ".mp3")
				sftpC.Remove(remoteAudioDir + "/" + b + ".wav")
				sftpC.Remove(remoteTransDir + "/" + b + "_atendente.wav.json")
				sftpC.Remove(remoteTransDir + "/" + b + "_cliente.wav.json")

				recordTime(b, "download_done")
				os.WriteFile(filepath.Join(stage4Download, b+".ready"), []byte(time.Now().String()), 0644)
				os.Remove(marker)
				log.Printf("[Downloader] %s: OK", b)
			}(base, filepath.Join(stage3Gpu, f.Name()))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func persisterService() {
	for {
		files, _ := os.ReadDir(stage4Download)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".ready") {
				continue
			}
			base := strings.TrimSuffix(f.Name(), ".ready")
			if _, busy := activeProcessing.Load("s5_" + base); busy {
				continue
			}

			activeProcessing.Store("s5_"+base, true)
			go func(b string, marker string) {
				defer activeProcessing.Delete("s5_" + b)
				jsA := filepath.Join(stage4Download, b+"_atendente.wav.json")
				jsC := filepath.Join(stage4Download, b+"_cliente.wav.json")

				segA := loadSegments(jsA, "Atendente")
				segC := loadSegments(jsC, "Cliente")
				all := append(segA, segC...)
				sort.SliceStable(all, func(i, j int) bool { return all[i].Start < all[j].Start })

				var sb strings.Builder
				for _, s := range all {
					m, sec := divmod(int(s.Start), 60)
					sb.WriteString(fmt.Sprintf("[%02d:%02d] %s: %s\n", m, sec, s.Speaker, s.Text))
				}
				content := sb.String()
				_ = saveToPostgres(b, content, fetchDB2Metadata(b))
				showFinalReport(b)
				os.Remove(jsA)
				os.Remove(jsC)
				os.Remove(marker)
			}(base, filepath.Join(stage4Download, f.Name()))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func recordTime(base, event string) {
	m, _ := fileTimestamps.LoadOrStore(base, make(map[string]time.Time))
	m.(map[string]time.Time)[event] = time.Now()
}

func showFinalReport(base string) {
	val, ok := fileTimestamps.Load(base)
	if !ok {
		return
	}
	t := val.(map[string]time.Time)

	total := time.Since(t["start"])
	upActive := t["upload_done"].Sub(t["upload_start"])
	srvQueue := t["gpu_start_real"].Sub(t["upload_done"])
	if srvQueue < 0 {
		srvQueue = 0
	}
	gpuActive := t["gpu_done"].Sub(t["gpu_start_real"])
	down := t["download_done"].Sub(t["gpu_done"])

	log.Printf("[V15.0] %s: TOTAL:%s | Up:%s | Q:%s | GPU:%s | D:%s",
		base, total.Round(time.Second),
		upActive.Round(time.Second), srvQueue.Round(time.Second),
		gpuActive.Round(time.Second), down.Round(time.Second))
}

func isAudio(n string) bool {
	e := strings.ToLower(filepath.Ext(n))
	return e == ".mp3" || e == ".wav" || e == ".m4a"
}
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
func connectSSH() (*ssh.Client, *sftp.Client, error) {
	sshSem <- struct{}{}
	defer func() { <-sshSem }()
	key, _ := os.ReadFile(remoteKeyPath)
	signer, _ := ssh.ParsePrivateKey(key)
	conf := &ssh.ClientConfig{User: remoteUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 20 * time.Second}
	c, err := ssh.Dial("tcp", remoteHost+":22", conf)
	if err != nil {
		return nil, nil, err
	}
	s, err := sftp.NewClient(c)
	return c, s, err
}
func uploadFile(s *sftp.Client, l, r string) error {
	f, _ := os.Open(l)
	defer f.Close()
	d, _ := s.Create(r)
	defer d.Close()
	_, err := io.Copy(d, f)
	return err
}
func downloadFile(s *sftp.Client, r, l string) error {
	f, err := s.Open(r)
	if err != nil {
		return err
	}
	defer f.Close()
	d, _ := os.Create(l)
	defer d.Close()
	_, err = io.Copy(d, f)
	return err
}
func loadSegments(p, sp string) []Segment {
	d, _ := os.ReadFile(p)
	var s []Segment
	json.Unmarshal(d, &s)
	for i := range s {
		s[i].Speaker = sp
	}
	return s
}
func fetchDB2Metadata(id string) map[string]interface{} {
	o, _ := exec.Command("python3", "fetch_db2.py", id).CombinedOutput()
	var m map[string]interface{}
	json.Unmarshal(o, &m)
	return m
}
func saveToPostgres(base, content string, meta map[string]interface{}) error {
	if dbPool == nil {
		return nil
	}
	m, _ := json.Marshal(meta)
	_, err := dbPool.Exec(context.Background(), "INSERT INTO transcriptions (file_name, content, processed_at, metadata) VALUES ($1, $2, $3, $4)", base, content, time.Now(), m)
	return err
}
func divmod(n, d int) (q, r int) { q = n / d; r = n % d; return }
