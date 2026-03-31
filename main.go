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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	localWatchDir  = "C:/Audios"
	stage2Upload   = "./temp/2_upload"
	stage3Gpu      = "./temp/3_gpu"
	stage4Download = "./temp/4_done"
	localOldDir    = "./audios_old"
	localTransDir  = "./transcricoes"

	remoteHost     = "20.127.212.253"
	remoteUser     = "speaksense"
	remoteKeyPath  = "vm-speaksense-eus-dev_key.pem"
	remoteAudioDir = "/home/speaksense/whisper-gpu-test-paralel/audios"
	remoteTransDir = "/home/speaksense/whisper-gpu-test-paralel/transcricoes"

	postgresURL = "postgres://srvbi:NbHo2WB8EyzatlPjmD1e@10.0.68.39:5433/transcriberdb"
)

// Segment representa uma fala com timestamps (usado na timeline e por canal).
type Segment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
}

// DB2Meta contém os metadados da ligação buscados no IBM DB2.
type DB2Meta struct {
	NmePessoa        string `json:"NME_PESSOA"`
	NmeProfissional  string `json:"NME_PROFISSIONAL"`
	DscEquipe        string `json:"DSC_EQUIPE"`
	TpoLigacao       string `json:"TPO_LIGACAO"`
	DtaCriacao       string `json:"DTA_CRIACAO"`
	DtaDiscagem      string `json:"DTA_DISCAGEM"`
	DtaInicioLigacao string `json:"DTA_INICIO_LIGACAO"`
	DtaFimLigacao    string `json:"DTA_FIM_LIGACAO"`
	DscCampanha      string `json:"DSC_CAMPANHA"`
}

// AudioQuality contém métricas de qualidade do áudio geradas pelo transcrever_diarize.py
type AudioQuality struct {
	SnrDb           float64 `json:"snr_db"`
	SilenceRatio    float64 `json:"silence_ratio"`
	ClippingRatio   float64 `json:"clipping_ratio"`
	DropoutCount    int     `json:"dropout_count"`
	Ch0Rms          float64 `json:"ch0_rms"`
	Ch1Rms          float64 `json:"ch1_rms"`
	AudioEnhanced   bool    `json:"audio_enhanced"`
	Diarizer        string  `json:"diarizer"`
	TurnosAtendente int     `json:"turnos_atendente"`
	TurnosCliente   int     `json:"turnos_cliente"`
}

var (
	dbPool           *pgxpool.Pool
	fileTimestamps   sync.Map
	activeProcessing sync.Map

	uploadSem   = make(chan struct{}, 4)
	downloadSem = make(chan struct{}, 4)
	sshSem      = make(chan struct{}, 8)
)


// Dicionário de correções baseadas em análise de 34 transcrições reais
// IMPORTANTE: Apenas erros sistemáticos que NÃO alteram significado
var correctionDict = map[string]string{
	// Nome da instituição (7 ocorrências encontradas)
	"LBZ": "LBV",
	"LBB": "LBV",

	// Espaços em valores monetários (sistemático)
	"R $ ":   "R$ ",
	" ,00":   ",00",
	" ,50":   ",50",
	" ,25":   ",25",
	" ,75":   ",75",

	// Hífens com espaços (5+ ocorrências)
	"bem -vindo":   "bem-vindo",
	"pós -pago":    "pós-pago",
	"pós - pago":   "pós-pago",
	"e -mail":      "e-mail",

	// Separadores de milhares
	"1 .500": "1.500",

	// ═══ NOVOS PADRÕES CONFIRMADOS 3+ VEZES ═══
	// Fusão OCR de palavras (desona = des + ona, etc)
	"desonadoação":   "a doação",
	"desuma doação":  "a doação",

	// Domínios com espaço antes de ponto (3+ ocorrências)
	".org .br":      ".org.br",
	"pixi .org .br": "pixi.org.br",
	"lbv .org .br":  "lbv.org.br",

	// Tratamento (Dana não existe em português, é erro de transcrição)
	"Dana ":  "Dona ",
	"Dana,":  "Dona,",
	"Dana.":  "Dona.",
	"Dana?":  "Dona?",

	// Telefone/CPF com espaço antes de hífen (3+ ocorrências)
	// Nota: padrão regex aplicado separadamente
}

func correctText(text string) string {
	// Aplica correções do dicionário
	for wrong, right := range correctionDict {
		text = strings.ReplaceAll(text, wrong, right)
	}

	// Regex: Remove espaço antes de hífen em numeração (CPF, telefone, etc)
	// Padrão: "número -número" → "número-número" (3+ ocorrências confirmadas)
	reHyphen := regexp.MustCompile(`(\d)\s+-(\d)`)
	text = reHyphen.ReplaceAllString(text, "$1-$2")

	return text
}

func initPostgres() error {
	db, err := pgxpool.New(context.Background(), postgresURL)
	if err != nil {
		return err
	}
	dbPool = db
	return nil
}

func runMigrations() {
	if dbPool == nil {
		return
	}
	migrations := []string{
		`ALTER TABLE transcricoes ADD COLUMN IF NOT EXISTS silence_ratio   NUMERIC(5,3)`,
		`ALTER TABLE transcricoes ADD COLUMN IF NOT EXISTS clipping_ratio  NUMERIC(8,5)`,
		`ALTER TABLE transcricoes ADD COLUMN IF NOT EXISTS dropout_count   INTEGER`,
		`ALTER TABLE transcricoes ADD COLUMN IF NOT EXISTS ch0_rms         NUMERIC(8,4)`,
		`ALTER TABLE transcricoes ADD COLUMN IF NOT EXISTS ch1_rms         NUMERIC(8,4)`,
		// Corrige colunas criadas previamente com precisão menor (2 → 4 decimais)
		`ALTER TABLE transcricoes ALTER COLUMN ch0_rms TYPE NUMERIC(8,4)`,
		`ALTER TABLE transcricoes ALTER COLUMN ch1_rms TYPE NUMERIC(8,4)`,
		// Adiciona coluna para transcrição corrigida
		`ALTER TABLE transcricoes ADD COLUMN IF NOT EXISTS transcricao_corrigida TEXT`,
	}
	for _, sql := range migrations {
		if _, err := dbPool.Exec(context.Background(), sql); err != nil {
			log.Printf("MIGRATION WARN: %v", err)
		}
	}
	log.Printf("Migrations aplicadas.")
}

func main() {
	// Comando para limpar a tabela: go run main.go clean
	if len(os.Args) > 1 && os.Args[1] == "clean" {
		if err := initPostgres(); err != nil {
			log.Fatal(err)
		}
		result, err := dbPool.Exec(context.Background(), "DELETE FROM transcricoes")
		if err != nil {
			log.Fatalf("Erro ao deletar: %v\n", err)
		}
		log.Printf("✓ %d registros deletados\n", result.RowsAffected())
		var count int64
		dbPool.QueryRow(context.Background(), "SELECT COUNT(*) FROM transcricoes").Scan(&count)
		log.Printf("✓ Tabela agora tem %d registros\n", count)
		return
	}

	folders := []string{localWatchDir, stage2Upload, stage3Gpu, stage4Download, localOldDir, localTransDir}
	for _, d := range folders {
		os.MkdirAll(d, 0755)
	}

	if err := initPostgres(); err != nil {
		log.Printf("AVISO: PostgreSQL indisponível — dados não serão persistidos: %v", err)
	}
	runMigrations()

	log.Printf(">>> INICIANDO PIPELINE DE TRANSCRIÇÃO <<<")

	go sourceProcessorService()
	go uploaderService()
	go gpuWatcherService()
	go downloaderService()
	go persisterService()

	select {}
}

// sourceProcessorService move o MP3 original da pasta de entrada para o stage de upload.
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

				dest := filepath.Join(stage2Upload, fn)
				for attempt := 1; attempt <= 5; attempt++ {
					log.Printf("[Source] %s: movendo para upload (tentativa %d)...", base, attempt)
					if err := os.Rename(filepath.Join(localWatchDir, fn), dest); err == nil {
						recordTime(base, "split_done")
						log.Printf("[Source] %s: OK", base)
						break
					} else if attempt == 5 {
						log.Printf("[Source] %s: ERRO ao mover após %d tentativas: %v", base, attempt, err)
					} else {
						time.Sleep(300 * time.Millisecond)
					}
				}
			}(name)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// uploaderService envia o MP3 para a VM via SFTP.
func uploaderService() {
	for {
		files, _ := os.ReadDir(stage2Upload)
		for _, f := range files {
			name := f.Name()
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
				tempPath  := remoteAudioDir + "/.tmp_" + fn
				finalPath := remoteAudioDir + "/" + fn

				// Busca DB2 e envia .meta.json ANTES do rename do áudio
				// Garante que o watcher já encontre o meta quando detectar o arquivo
				meta := fetchDB2Metadata(b)
				if metaBytes, err := json.Marshal(meta); err == nil {
					metaRemote := remoteAudioDir + "/" + b + ".meta.json"
					sftpC.Remove(metaRemote)
					if mf, err := sftpC.Create(metaRemote); err == nil {
						mf.Write(metaBytes)
						mf.Close()
						log.Printf("[Uploader] %s: meta.json pronto (pessoa=%s campanha=%s)", b, meta.NmePessoa, meta.DscCampanha)
					}
				}

				log.Printf("[Uploader] %s: enviando %s...", b, fn)
				if err := uploadFile(sftpC, localPath, tempPath); err != nil {
					log.Printf("[Uploader] ERRO upload %s: %v", fn, err)
					return
				}
				// Verifica que o arquivo temp realmente existe antes de renomear
				if _, statErr := sftpC.Stat(tempPath); statErr != nil {
					log.Printf("[Uploader] ERRO: temp file não encontrado após upload (%s): %v", tempPath, statErr)
					return
				}
				// Remove destino se já existir (evita SSH_FX_FAILURE no rename)
				sftpC.Remove(finalPath)
				if err := sftpC.Rename(tempPath, finalPath); err != nil {
					log.Printf("[Uploader] ERRO rename %s: %v", fn, err)
					return
				}
				recordTime(b, "upload_done")

				// Marca como enviado; move local para audios_old
				os.WriteFile(filepath.Join(stage2Upload, b+".ready"), []byte(time.Now().String()), 0644)
				os.Rename(localPath, filepath.Join(localOldDir, fn))
				log.Printf("[Uploader] %s: OK", b)
			}(base, name)
		}
		time.Sleep(1 * time.Second)
	}
}

// gpuWatcherService aguarda o watcher.sh na VM terminar a transcrição.
// Detecta o arquivo .active (em andamento) e os JSONs de saída (concluído).
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
			go func(b, marker string) {
				defer activeProcessing.Delete("s3_" + b)

				client, sftpC, err := connectSSH()
				if err != nil {
					return
				}
				defer client.Close()
				defer sftpC.Close()

				// Caminhos dos arquivos de saída do transcrever.py
				jsonAt     := remoteTransDir + "/" + b + "_atendente.json"
				jsonCl     := remoteTransDir + "/" + b + "_cliente.json"
				activeFile := remoteTransDir + "/" + b + ".active"

				gpuStarted := false
				for {
					// Detecta início do processamento na GPU
					if !gpuStarted {
						if _, err := sftpC.Stat(activeFile); err == nil {
							recordTime(b, "gpu_start_real")
							gpuStarted = true
							log.Printf("[GPUWatcher] %s: transcrição iniciada na GPU", b)
						}
					}

					// Aguarda ambos os JSONs de saída
					_, errAt := sftpC.Stat(jsonAt)
					_, errCl := sftpC.Stat(jsonCl)
					if errAt == nil && errCl == nil {
						break
					}
					time.Sleep(3 * time.Second)
				}

				if !gpuStarted {
					recordTime(b, "gpu_start_real")
				}
				recordTime(b, "gpu_done")

				os.WriteFile(filepath.Join(stage3Gpu, b+".ready"), []byte(time.Now().String()), 0644)
				os.Remove(marker)
				log.Printf("[GPUWatcher] %s: transcrição concluída", b)
			}(base, filepath.Join(stage2Upload, f.Name()))
		}
		time.Sleep(1 * time.Second)
	}
}

// downloaderService baixa todos os arquivos de saída da VM.
func downloaderService() {
	for {
		files, _ := os.ReadDir(stage3Gpu)
		for _, f := range files {
			base := strings.TrimSuffix(f.Name(), ".ready")
			if _, busy := activeProcessing.Load("s4_" + base); busy {
				continue
			}

			activeProcessing.Store("s4_"+base, true)
			go func(b, marker string) {
				defer activeProcessing.Delete("s4_" + b)

				downloadSem <- struct{}{}
				defer func() { <-downloadSem }()

				client, sftpC, err := connectSSH()
				if err != nil {
					return
				}
				defer client.Close()
				defer sftpC.Close()

				// Arquivos obrigatorios: sem eles a transcricao e invalida
				requiredFiles := map[string]string{
					b + "_atendente.json": filepath.Join(stage4Download, b+"_atendente.json"),
					b + "_cliente.json":   filepath.Join(stage4Download, b+"_cliente.json"),
					b + ".txt":            filepath.Join(stage4Download, b+".txt"),
				}
				// Arquivos opcionais: podem nao existir em transcricoes antigas.
				optionalFiles := map[string]string{
					b + "_timeline.json": filepath.Join(stage4Download, b+"_timeline.json"),
					b + "_quality.json":  filepath.Join(stage4Download, b+"_quality.json"),
				}
				optionalDefaults := map[string]string{
					b + "_timeline.json": "[]",
					b + "_quality.json":  `{"snr_db":0,"silence_ratio":0,"clipping_ratio":0,"dropout_count":0,"ch0_rms":0,"ch1_rms":0,"audio_enhanced":false,"diarizer":"","turnos_atendente":0,"turnos_cliente":0,"duracao_segundos":0}`,
				}

				allOk := true
				for remote, local := range requiredFiles {
					if err := downloadFile(sftpC, remoteTransDir+"/"+remote, local); err != nil {
						log.Printf("[Downloader] ERRO ao baixar %s: %v", remote, err)
						allOk = false
					}
				}
				if !allOk {
					log.Printf("[Downloader] %s: falha em arquivo obrigatorio", b)
					return
				}

				for remote, local := range optionalFiles {
					if err := downloadFile(sftpC, remoteTransDir+"/"+remote, local); err != nil {
						os.WriteFile(local, []byte(optionalDefaults[remote]), 0644)
					}
				}

				// Salva cópia permanente em ./transcricoes
				for _, fmap := range []map[string]string{requiredFiles, optionalFiles} {
					for _, local := range fmap {
						dst := filepath.Join(localTransDir, filepath.Base(local))
						data, _ := os.ReadFile(local)
						os.WriteFile(dst, data, 0644)
					}
				}

				// Limpeza remota
				for _, fmap := range []map[string]string{requiredFiles, optionalFiles} {
					for remote := range fmap {
						sftpC.Remove(remoteTransDir + "/" + remote)
					}
				}
				sftpC.Remove(remoteAudioDir + "/" + b + ".mp3")
				sftpC.Remove(remoteAudioDir + "/" + b + ".wav")
				sftpC.Remove(remoteAudioDir + "/" + b + ".m4a")
				sftpC.Remove(remoteAudioDir + "/" + b + ".meta.json")

				recordTime(b, "download_done")
				os.WriteFile(filepath.Join(stage4Download, b+".ready"), []byte(time.Now().String()), 0644)
				os.Remove(marker)
				log.Printf("[Downloader] %s: OK", b)
			}(base, filepath.Join(stage3Gpu, f.Name()))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// persisterService lê os arquivos baixados, busca metadados do DB2 e persiste no PostgreSQL.
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
			go func(b, marker string) {
				defer activeProcessing.Delete("s5_" + b)

				locAt       := filepath.Join(stage4Download, b+"_atendente.json")
				locCl       := filepath.Join(stage4Download, b+"_cliente.json")
				locTimeline := filepath.Join(stage4Download, b+"_timeline.json")
				locTxt      := filepath.Join(stage4Download, b+".txt")
				locQuality  := filepath.Join(stage4Download, b+"_quality.json")

				// Lê transcrição formatada
				txtBytes, _ := os.ReadFile(locTxt)
				txtContent := string(txtBytes)

				// Lê JSONs brutos para o banco (JSONB)
				timelineJSON, _ := os.ReadFile(locTimeline)
				atJSON, _       := os.ReadFile(locAt)
				clJSON, _       := os.ReadFile(locCl)

				// Extrai estatísticas da timeline
				timeline    := loadTimeline(locTimeline)
				totalTurnos := len(timeline)
				duracao     := 0.0
				if len(timeline) > 0 {
					duracao = timeline[len(timeline)-1].End
				}

				// Lê métricas de qualidade do áudio
				var quality AudioQuality
				if qBytes, err := os.ReadFile(locQuality); err == nil {
					json.Unmarshal(qBytes, &quality)
				}

				// Busca metadados no DB2
				meta := fetchDB2Metadata(b)

				log.Printf("[Persister] %s: %d turnos | %.1fs | SNR=%.1fdB | silêncio=%.0f%% | dropouts=%d | clipping=%.3f%% | enhanced=%v | pessoa=%s",
					b, totalTurnos, duracao, quality.SnrDb, quality.SilenceRatio*100, quality.DropoutCount, quality.ClippingRatio*100, quality.AudioEnhanced, meta.NmePessoa)

				// Aplicar correções de erros comuns de transcrição
				// NÃO aplicar correções automáticas - manter transcrição original fiel

			// Gerar versão corrigida
				txtCorrected := correctText(txtContent)

				if err := saveToPostgres(b, txtContent, txtCorrected, timelineJSON, atJSON, clJSON,
					totalTurnos, duracao, meta, quality); err != nil {
					log.Printf("[Persister] ERRO PostgreSQL para %s: %v", b, err)
				} else {
					log.Printf("[Persister] %s: persistido com sucesso.", b)
				}

				showFinalReport(b)

				// Remove arquivos temporários do stage
				for _, p := range []string{locAt, locCl, locTimeline, locTxt, locQuality, marker} {
					os.Remove(p)
				}
			}(base, filepath.Join(stage4Download, f.Name()))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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

	// Se não passou pelo sourceProcessor (arquivo já estava na VM), reporta só o que temos
	start := t["start"]
	if start.IsZero() {
		gpuActive := t["gpu_done"].Sub(t["gpu_start_real"])
		down      := t["download_done"].Sub(t["gpu_done"])
		log.Printf("[RELATÓRIO] %s: GPU:%s | Download:%s (arquivo pré-existente na VM)",
			base, gpuActive.Round(time.Second), down.Round(time.Second))
		return
	}

	total    := time.Since(start)
	upActive := t["upload_done"].Sub(t["upload_start"])
	srvQueue := t["gpu_start_real"].Sub(t["upload_done"])
	if srvQueue < 0 {
		srvQueue = 0
	}
	gpuActive := t["gpu_done"].Sub(t["gpu_start_real"])
	down      := t["download_done"].Sub(t["gpu_done"])

	log.Printf("[RELATÓRIO] %s: TOTAL:%s | Upload:%s | Fila:%s | GPU:%s | Download:%s",
		base,
		total.Round(time.Second),
		upActive.Round(time.Second),
		srvQueue.Round(time.Second),
		gpuActive.Round(time.Second),
		down.Round(time.Second),
	)
}

func isAudio(n string) bool {
	e := strings.ToLower(filepath.Ext(n))
	return e == ".mp3" || e == ".wav" || e == ".m4a"
}

func loadTimeline(p string) []Segment {
	d, _ := os.ReadFile(p)
	var segs []Segment
	json.Unmarshal(d, &segs)
	return segs
}

func isMP3ID(id string) bool {
	// IDs numéricos são gravações MP3 com registro no DB2.
	// IDs no formato 20260320T... são WAV internos sem registro no DB2.
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(id) > 0
}

func fetchDB2Metadata(id string) DB2Meta {
	// WAV internos não têm registro no DB2
	if !isMP3ID(id) {
		return DB2Meta{}
	}

	// Localiza fetch_db2.py relativo ao executável (robusto para Linux/WSL/Windows)
	scriptPath := "fetch_db2.py"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "fetch_db2.py")
		if _, serr := os.Stat(candidate); serr == nil {
			scriptPath = candidate
		}
	}

	// Candidatos em ordem de preferência (Linux-first para binário nativo WSL)
	type candidate struct {
		cmd  string
		args []string
	}
	candidates := []candidate{
		{"/usr/bin/python3",    []string{scriptPath, id}},
		{"/usr/local/bin/python3", []string{scriptPath, id}},
		{"python3",             []string{scriptPath, id}},
		{"python",              []string{scriptPath, id}},
		{"python3.exe",         []string{scriptPath, id}},
		{"python.exe",          []string{scriptPath, id}},
	}

	badPath := func(s string) bool {
		return strings.Contains(s, "can't open file") ||
			strings.Contains(s, "No such file or directory") ||
			strings.Contains(s, "cannot find the path")
	}
	notFound := func(e string) bool {
		return strings.Contains(e, "not found") ||
			strings.Contains(e, "file not found") ||
			strings.Contains(e, "not recognized")
	}

	// Tenta Python local primeiro
	for _, c := range candidates {
		o, err := exec.Command(c.cmd, c.args...).CombinedOutput()
		if err != nil {
			errStr := err.Error()
			outStr := string(o)
			if notFound(errStr) {
				continue // interpretador não instalado
			}
			if badPath(outStr) {
				// Python rodou mas não achou o script — tenta próximo candidato
				continue
			}
			// Erro real, mas não retorna ainda — vai tentar SSH
		} else {
			// Sucesso local
			var m DB2Meta
			if err := json.Unmarshal(o, &m); err != nil {
				log.Printf("[DB2] ERRO JSON para %s: %v | output: %s", id, err, string(o))
				return DB2Meta{}
			}
			log.Printf("[DB2] Metadados obtidos localmente para %s", id)
			return m
		}
	}

	// Python local falhou, tenta via SSH na VM
	log.Printf("[DB2] Python local indisponível, tentando via SSH para %s...", id)
	ssh, sftp, err := connectSSH()
	if err != nil {
		log.Printf("[DB2] SSH falhou: %v - metadados não serão preenchidos", err)
		return DB2Meta{}
	}
	defer ssh.Close()
	defer sftp.Close()

	session, err := ssh.NewSession()
	if err != nil {
		log.Printf("[DB2] SSH session falhou: %v", err)
		return DB2Meta{}
	}
	defer session.Close()

	// Executa fetch_db2.py na VM
	out, err := session.Output(fmt.Sprintf("cd /home/speaksense && python3 fetch_db2.py %s", id))
	if err != nil {
		log.Printf("[DB2] ERRO ao executar fetch_db2.py na VM para %s: %v", id, err)
		return DB2Meta{}
	}

	var m DB2Meta
	if err := json.Unmarshal(out, &m); err != nil {
		log.Printf("[DB2] ERRO JSON para %s (via SSH): %v | output: %s", id, err, string(out))
		return DB2Meta{}
	}

	log.Printf("[DB2] Metadados obtidos via SSH para %s", id)
	return m
}

func saveToPostgres(base, txtContent, txtCorrected string, timelineJSON, atJSON, clJSON []byte,
	totalTurnos int, duracao float64, meta DB2Meta, quality AudioQuality) error {

	if dbPool == nil {
		return nil
	}

	rawMeta, _ := json.Marshal(meta)

	_, err := dbPool.Exec(context.Background(), `
		INSERT INTO transcricoes (
			gravacao_id, processado_em,
			transcricao_txt, transcricao_corrigida, timeline_json, atendente_json, cliente_json,
			total_turnos, turnos_atendente, turnos_cliente, duracao_segundos,
			snr_db, silence_ratio, clipping_ratio, dropout_count, ch0_rms, ch1_rms,
			audio_enhanced, diarizer,
			nme_pessoa, nme_profissional, dsc_equipe, tpo_ligacao,
			dta_criacao, dta_discagem, dta_inicio_ligacao, dta_fim_ligacao,
			dsc_campanha, db2_metadata_json
		) VALUES (
			$1, $2,
			$3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17,
			$18, $19,
			$20, $21, $22, $23,
			$24, $25, $26, $27,
			$28, $29
		)
		ON CONFLICT (gravacao_id) DO UPDATE SET
			processado_em           = EXCLUDED.processado_em,
			transcricao_txt         = EXCLUDED.transcricao_txt,
			transcricao_corrigida   = EXCLUDED.transcricao_corrigida,
			timeline_json           = EXCLUDED.timeline_json,
			atendente_json      = EXCLUDED.atendente_json,
			cliente_json        = EXCLUDED.cliente_json,
			total_turnos        = EXCLUDED.total_turnos,
			turnos_atendente    = EXCLUDED.turnos_atendente,
			turnos_cliente      = EXCLUDED.turnos_cliente,
			duracao_segundos    = EXCLUDED.duracao_segundos,
			snr_db              = EXCLUDED.snr_db,
			silence_ratio       = EXCLUDED.silence_ratio,
			clipping_ratio      = EXCLUDED.clipping_ratio,
			dropout_count       = EXCLUDED.dropout_count,
			ch0_rms             = EXCLUDED.ch0_rms,
			ch1_rms             = EXCLUDED.ch1_rms,
			audio_enhanced      = EXCLUDED.audio_enhanced,
			diarizer            = EXCLUDED.diarizer,
			nme_pessoa          = EXCLUDED.nme_pessoa,
			nme_profissional    = EXCLUDED.nme_profissional,
			dsc_equipe          = EXCLUDED.dsc_equipe,
			tpo_ligacao         = EXCLUDED.tpo_ligacao,
			dta_criacao         = EXCLUDED.dta_criacao,
			dta_discagem        = EXCLUDED.dta_discagem,
			dta_inicio_ligacao  = EXCLUDED.dta_inicio_ligacao,
			dta_fim_ligacao     = EXCLUDED.dta_fim_ligacao,
			dsc_campanha        = EXCLUDED.dsc_campanha,
			db2_metadata_json   = EXCLUDED.db2_metadata_json
	`,
		base, time.Now(),
		txtContent, txtCorrected, timelineJSON, atJSON, clJSON,
		totalTurnos, quality.TurnosAtendente, quality.TurnosCliente, fmt.Sprintf("%.2f", duracao),
		quality.SnrDb, quality.SilenceRatio, quality.ClippingRatio, quality.DropoutCount, quality.Ch0Rms, quality.Ch1Rms,
		quality.AudioEnhanced, quality.Diarizer,
		meta.NmePessoa, meta.NmeProfissional, meta.DscEquipe, meta.TpoLigacao,
		meta.DtaCriacao, meta.DtaDiscagem, meta.DtaInicioLigacao, meta.DtaFimLigacao,
		meta.DscCampanha, rawMeta,
	)
	return err
}

func connectSSH() (*ssh.Client, *sftp.Client, error) {
	sshSem <- struct{}{}
	defer func() { <-sshSem }()

	key, err := os.ReadFile(remoteKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("chave SSH não encontrada em %s: %w", remoteKeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("erro ao parsear chave SSH: %w", err)
	}
	conf := &ssh.ClientConfig{
		User:            remoteUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}
	c, err := ssh.Dial("tcp", remoteHost+":22", conf)
	if err != nil {
		return nil, nil, err
	}
	s, err := sftp.NewClient(c)
	return c, s, err
}

func uploadFile(s *sftp.Client, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	d, err := s.Create(remotePath)
	if err != nil {
		return err
	}

	if _, err = io.Copy(d, f); err != nil {
		d.Close()
		return fmt.Errorf("io.Copy: %w", err)
	}
	// Close must be checked — it finalises the file on the SFTP server.
	// A silent defer would discard the error and leave the remote file incomplete.
	if err = d.Close(); err != nil {
		return fmt.Errorf("sftp close: %w", err)
	}
	return nil
}

func downloadFile(s *sftp.Client, remotePath, localPath string) error {
	f, err := s.Open(remotePath)
	if err != nil {
		return err
	}
	defer f.Close()

	d, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer d.Close()

	_, err = io.Copy(d, f)
	return err
}
