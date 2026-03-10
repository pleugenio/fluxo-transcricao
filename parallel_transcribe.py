import os
import json
import time
import queue
import threading
import sys
import subprocess
from faster_whisper import WhisperModel
from concurrent.futures import ThreadPoolExecutor

# ==============================
# CONFIGURAÇÃO V15.0 (WORD-DRIVEN ENGINE)
# ==============================
MODEL_SIZE = "large-v3"
DEVICE = "cuda"
COMPUTE_TYPE = "float16" 
NUM_WORKERS = 2 

# Prompt Curto e Objetivo (Evita desvios)
INITIAL_PROMPT = "Transcrição precisa. Luciano, LBV, trinta reais, Olito Meira."

# Pastas no Servidor
AUDIO_FOLDER = "./audios"      # Fonte original (MP3/M4A)
OUTPUT_FOLDER = "./transcricoes"
TEMP_SPLIT_FOLDER = "./temp_split"

os.makedirs(AUDIO_FOLDER, exist_ok=True)
os.makedirs(OUTPUT_FOLDER, exist_ok=True)
os.makedirs(TEMP_SPLIT_FOLDER, exist_ok=True)

in_progress = set()
in_progress_lock = threading.Lock()

# ==============================
# POOL DE MODELOS
# ==============================
print(f"--- INICIANDO WHISPER SERVICE V15.0 (WORD-DRIVEN ENGINE) ---")
print(f"Carregando {NUM_WORKERS} instâncias na GPU...")
sys.stdout.flush()

model_pool = queue.Queue()
try:
    for i in range(NUM_WORKERS):
        print(f"Carregando instância {i+1}...")
        m = WhisperModel(MODEL_SIZE, device=DEVICE, compute_type=COMPUTE_TYPE)
        model_pool.put(m)
    print("Pronto para processar.\n")
    sys.stdout.flush()
except Exception as e:
    print(f"ERRO CRÍTICO NO CARREGAMENTO: {e}")
    sys.stdout.flush()
    exit(1)

def run_remote_split(input_path, base_name):
    """V13.4: Split Robusto (Foco no Início do Áudio)"""
    at_path = os.path.join(TEMP_SPLIT_FOLDER, f"{base_name}_atendente.wav")
    cl_path = os.path.join(TEMP_SPLIT_FOLDER, f"{base_name}_cliente.wav")
    
    # Simplificado: Extrai canais puristas sem filtros que possam dar "skip" no início
    cmd = [
        "ffmpeg", "-y", "-i", input_path,
        "-filter_complex", "[0:a]pan=mono|c0=c0[at];[0:a]pan=mono|c0=c1[cl]",
        "-map", "[at]", "-ac", "1", "-ar", "16000", at_path,
        "-map", "[cl]", "-ac", "1", "-ar", "16000", cl_path
    ]
    
    try:
        subprocess.run(cmd, check=True, capture_output=True)
        return at_path, cl_path
    except Exception as e:
        print(f"ERRO NO SPLIT MINIMALISTA ({base_name}): {e}")
        return None, None

def transcribe_audio(audio_path, speaker_label):
    fn = os.path.basename(audio_path)
    active_marker = os.path.join(OUTPUT_FOLDER, fn + ".active")
    
    current_model = model_pool.get()
    with open(active_marker, "w") as f:
        f.write(str(time.time()))

    print(f"-> Transcrevendo {speaker_label}: {fn}")
    sys.stdout.flush()
    
    try:
        # V15.0: Transmissão por Palavra (O fim dos blocos gigantes)
        segments, info = current_model.transcribe(
            audio_path,
            beam_size=10, 
            word_timestamps=True,
            initial_prompt=INITIAL_PROMPT,
            language="pt",
            condition_on_previous_text=False, # Evita que a IA "se perca" no tempo
            temperature=0, 
            compression_ratio_threshold=2.2,
            no_speech_threshold=0.5, 
            log_prob_threshold=-1.0
        )
        
        all_words = []
        for s in list(segments):
            if s.words:
                all_words.extend(s.words)
        
        results = []
        if not all_words:
            return results # Áudio vazio ou apenas ruído
            
        # RECONSTRUÇÃO CIRÚRGICA:
        # Agrupamos palavras em linhas apenas se o gap for menor que 0.3s
        current_segment_words = [all_words[0]]
        start_time = all_words[0].start
        
        for i in range(1, len(all_words)):
            w_prev = all_words[i-1]
            w_curr = all_words[i]
            
            gap = w_curr.start - w_prev.end
            # Gap de 0.3s ou mudança brusca = NOVA LINHA
            if gap > 0.3:
                results.append({
                    "start": round(start_time, 3),
                    "end": round(w_prev.end, 3),
                    "text": " ".join([x.word.strip() for x in current_segment_words]).strip()
                })
                current_segment_words = [w_curr]
                start_time = w_curr.start
            else:
                current_segment_words.append(w_curr)
        
        # Adiciona a última parte
        if current_segment_words:
            results.append({
                "start": round(start_time, 3),
                "end": round(current_segment_words[-1].end, 3),
                "text": " ".join([x.word.strip() for x in current_segment_words]).strip()
            })
            
        return results
    finally:
        if os.path.exists(active_marker): os.remove(active_marker)
        model_pool.put(current_model)

def process_source_file(input_path):
    fn = os.path.basename(input_path)
    base = os.path.splitext(fn)[0]
    
    print(f"-> Processando fonte: {fn}")
    sys.stdout.flush()
    start_time = time.time()
    
    # 1. SPLIT LOCAL NO SERVIDOR (Gerando WAVs)
    at_file, cl_file = run_remote_split(input_path, base)
    if not at_file or not cl_file:
        with in_progress_lock: in_progress.remove(fn)
        return

    # 2. TRANSCRIÇÃO DOS CANAIS PURISTAS (WAV)
    res_at = transcribe_audio(at_file, "Atendente")
    res_cl = transcribe_audio(cl_file, "Cliente")

    # 3. SALVAR JSONS (Formatos de nome compatíveis com o Go)
    # Nota: O Go espera base_atendente.wav.json ou base_atendente.ogg.json
    # Vamos manter o sufixo original do áudio gerado para evitar confusão no Downloader
    with open(os.path.join(OUTPUT_FOLDER, f"{base}_atendente.wav.json"), "w", encoding="utf-8") as f:
        json.dump(res_at, f, ensure_ascii=False, indent=2)
    with open(os.path.join(OUTPUT_FOLDER, f"{base}_cliente.wav.json"), "w", encoding="utf-8") as f:
        json.dump(res_cl, f, ensure_ascii=False, indent=2)

    # V15.1: MODO DEPURAÇÃO - Não apagar os splits para análise do usuário
    # if os.path.exists(at_file): os.remove(at_file)
    # if os.path.exists(cl_file): os.remove(cl_file)
    
    dur = round(time.time() - start_time, 2)
    print(f"<- Concluído: {fn} (Total Server: {dur}s)")
    sys.stdout.flush()
    
    with in_progress_lock:
        in_progress.remove(fn)

def main_service_loop():
    print(f"Monitorando {AUDIO_FOLDER} por arquivos MP3/M4A originais...")
    sys.stdout.flush()
    with ThreadPoolExecutor(max_workers=NUM_WORKERS) as executor:
        while True:
            try:
                all_files = [f for f in os.listdir(AUDIO_FOLDER) if f.lower().endswith((".mp3", ".m4a", ".wav"))]
                
                for f in sorted(all_files):
                    base = os.path.splitext(f)[0]
                    # Verifica se já processou
                    if os.path.exists(os.path.join(OUTPUT_FOLDER, f"{base}_atendente.wav.json")):
                        continue

                    with in_progress_lock:
                        if f in in_progress: continue
                        path = os.path.join(AUDIO_FOLDER, f)
                        if os.path.exists(path) and os.path.getsize(path) > 1000:
                            in_progress.add(f)
                            executor.submit(process_source_file, path)
                
                time.sleep(1)
            except Exception as e:
                print(f"Erro Loop: {e}")
                sys.stdout.flush()
                time.sleep(2)

if __name__ == "__main__":
    main_service_loop()
