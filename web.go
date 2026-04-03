package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func startWebServer() {
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/files", handleListFiles)

	fmt.Println("📱 Servidor web iniciando na porta 8080...")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Printf("❌ Erro ao iniciar servidor web: %v\n", err)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html lang="pt-BR">
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>Upload - Fluxo Transcrição</title>
	<style>
		* { margin: 0; padding: 0; box-sizing: border-box; }
		body {
			font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
			background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
			min-height: 100vh;
			display: flex;
			justify-content: center;
			align-items: center;
			padding: 20px;
		}
		.container {
			background: white;
			border-radius: 10px;
			box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
			padding: 40px;
			max-width: 600px;
			width: 100%;
		}
		h1 { color: #333; text-align: center; margin-bottom: 30px; }
		.dropzone {
			border: 2px dashed #667eea;
			border-radius: 8px;
			padding: 40px;
			text-align: center;
			cursor: pointer;
			transition: all 0.3s;
			background: #f9f9f9;
		}
		.dropzone:hover { border-color: #764ba2; background: #f0f0f0; }
		.dropzone.dragover { border-color: #764ba2; background: #e8e8ff; }
		.dropzone p { color: #666; margin: 10px 0; }
		input[type="file"] { display: none; }
		.files-list { margin-top: 30px; }
		.file-item {
			display: flex;
			justify-content: space-between;
			padding: 10px;
			background: #f5f5f5;
			border-radius: 5px;
			margin-bottom: 8px;
			font-size: 14px;
		}
		.success { background: #efe; color: #3c3; padding: 15px; border-radius: 5px; display: none; margin-top: 20px; }
		.error { background: #fee; color: #c33; padding: 15px; border-radius: 5px; display: none; margin-top: 20px; }
	</style>
</head>
<body>
	<div class="container">
		<h1>🎤 Upload de Áudios</h1>
		<div class="dropzone" id="dropzone">
			<p><strong>Arraste arquivos aqui</strong></p>
			<p>ou clique para selecionar (MP3, WAV, M4A, FLAC)</p>
			<input type="file" id="fileInput" multiple accept=".mp3,.wav,.m4a,.flac">
		</div>
		<div class="success" id="success">✅ Arquivo enviado!</div>
		<div class="error" id="error"></div>
		<div class="files-list" id="filesList" style="display: none;">
			<h3>Arquivos na fila:</h3>
			<div id="uploadedFiles"></div>
		</div>
	</div>

	<script>
		const dropzone = document.getElementById('dropzone');
		const fileInput = document.getElementById('fileInput');

		dropzone.addEventListener('click', () => fileInput.click());
		dropzone.addEventListener('dragover', (e) => { e.preventDefault(); dropzone.classList.add('dragover'); });
		dropzone.addEventListener('dragleave', () => dropzone.classList.remove('dragover'));
		dropzone.addEventListener('drop', (e) => {
			e.preventDefault();
			dropzone.classList.remove('dragover');
			handleFiles(e.dataTransfer.files);
		});

		fileInput.addEventListener('change', (e) => handleFiles(e.target.files));

		function handleFiles(files) {
			Array.from(files).forEach(file => {
				const formData = new FormData();
				formData.append('file', file);
				fetch('/upload', { method: 'POST', body: formData })
					.then(r => r.json())
					.then(data => {
						if (data.error) {
							document.getElementById('error').textContent = '❌ ' + data.error;
							document.getElementById('error').style.display = 'block';
						} else {
							document.getElementById('success').style.display = 'block';
							setTimeout(() => document.getElementById('success').style.display = 'none', 3000);
							loadFiles();
						}
					});
			});
		}

		function loadFiles() {
			fetch('/files').then(r => r.json()).then(data => {
				const div = document.getElementById('uploadedFiles');
				div.innerHTML = '';
				if (data.files && data.files.length > 0) {
					document.getElementById('filesList').style.display = 'block';
					data.files.forEach(f => {
						const item = document.createElement('div');
						item.className = 'file-item';
						item.innerHTML = '<span>' + f + '</span><span>⏳ Na fila</span>';
						div.appendChild(item);
					});
				}
			});
		}

		loadFiles();
		setInterval(loadFiles, 5000);
	</script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		sendJSON(w, map[string]string{"error": "Erro ao ler arquivo"}, http.StatusBadRequest)
		return
	}
	defer file.Close()

	filename := filepath.Base(handler.Filename)
	if !isValidAudioFile(filename) {
		sendJSON(w, map[string]string{"error": "Tipo de arquivo não suportado"}, http.StatusBadRequest)
		return
	}

	os.MkdirAll(localWatchDir, 0755)

	filepath := filepath.Join(localWatchDir, filename)
	dst, err := os.Create(filepath)
	if err != nil {
		sendJSON(w, map[string]string{"error": "Erro ao salvar arquivo"}, http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		sendJSON(w, map[string]string{"error": "Erro ao copiar arquivo"}, http.StatusInternalServerError)
		return
	}

	fmt.Printf("📝 Arquivo recebido: %s\n", filename)
	sendJSON(w, map[string]string{"success": "Arquivo enviado com sucesso"}, http.StatusOK)
}

func handleListFiles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(localWatchDir)
	if err != nil {
		sendJSON(w, map[string]interface{}{"files": []string{}}, http.StatusOK)
		return
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && isValidAudioFile(entry.Name()) {
			files = append(files, entry.Name())
		}
	}

	sendJSON(w, map[string]interface{}{"files": files}, http.StatusOK)
}

func isValidAudioFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".mp3" || ext == ".wav" || ext == ".m4a" || ext == ".flac"
}

func sendJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
