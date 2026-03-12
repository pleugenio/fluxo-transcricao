import os
import json
import time
import threading
import subprocess
import sys
import zlib
from concurrent.futures import ThreadPoolExecutor, as_completed
from faster_whisper import WhisperModel

# ==============================
# CONFIGURAÇÃO V16.1 (ANTI-HALLUCINATION)
# ==============================

MODEL_SIZE = "large-v3"
DEVICE = "cuda"
COMPUTE_TYPE = "int8_float16"
NUM_WORKERS = 2  # Processa atendente e cliente em paralelo

AUDIO_FOLDER = "./audios"
OUTPUT_FOLDER = "./transcricoes"
TEMP_SPLIT_FOLDER = "./temp_split"

os.makedirs(OUTPUT_FOLDER, exist_ok=True)
os.makedirs(TEMP_SPLIT_FOLDER, exist_ok=True)

# ==============================
# CONTEXTO INSTITUCIONAL
# ==============================

prompt_contexto = """
Transcrição de telemarketing da Legião da Boa Vontade (LBV).

Palavras importantes:
LBV
Legião da Boa Vontade
DDD
CPF
plano pós-pago
adesão à doação mensal
contribuição mensal
"""

# ==============================
# CARREGAMENTO DO MODELO
# ==============================

print("--- INICIANDO WHISPER SERVICE V16.1 (ANTI-HALLUCINATION) ---")
print("Carregando modelo na GPU...")
sys.stdout.flush()

model = WhisperModel(
    MODEL_SIZE,
    device=DEVICE,
    compute_type=COMPUTE_TYPE
)
print("Modelo carregado.\n")
sys.stdout.flush()

# Lock para acesso ao modelo (NUM_WORKERS=2 mas modelo=1, então serializamos)
model_lock = threading.Lock()

# Controle de processamento em andamento
in_progress = set()
in_progress_lock = threading.Lock()

# ==============================
# SPLIT ESTÉREO (FFmpeg)
# ==============================

def split_stereo(input_path, base_name):
    """Separa canal esquerdo (atendente) e direito (cliente) do áudio estéreo."""
    at_path = os.path.join(TEMP_SPLIT_FOLDER, f"{base_name}_atendente.wav")
    cl_path = os.path.join(TEMP_SPLIT_FOLDER, f"{base_name}_cliente.wav")

    cmd = [
        "ffmpeg", "-y", "-i", input_path,
        "-filter_complex", "[0:a]pan=mono|c0=c0[at];[0:a]pan=mono|c0=c1[cl]",
        "-map", "[at]", "-ac", "1", "-ar", "16000", at_path,
        "-map", "[cl]", "-ac", "1", "-ar", "16000", cl_path
    ]

    try:
        subprocess.run(cmd, check=True, capture_output=True)
        return at_path, cl_path
    except subprocess.CalledProcessError as e:
        print(f"ERRO FFmpeg ({base_name}): {e.stderr.decode()}")
        return None, None

# ==============================
# FUNÇÃO DE TRANSCRIÇÃO
# ==============================

def transcrever(audio_path, speaker_label):
    """Transcreve um canal de áudio e retorna lista de segmentos com timestamps."""
    print(f"-> Transcrevendo {speaker_label}: {os.path.basename(audio_path)}")
    sys.stdout.flush()

    start = time.time()

    with model_lock:
        segments, info = model.transcribe(
            audio_path,
            beam_size=5,
            temperature=0,
            initial_prompt=prompt_contexto,
            vad_filter=True,
            vad_parameters={"min_silence_duration_ms": 800},
            word_timestamps=True,
            condition_on_previous_text=True,
            language="pt",
            # Anti-alucinação:
            compression_ratio_threshold=1.8,  # "nao nao nao" tem razão > 1.8 → descarta
            log_prob_threshold=-0.5,           # Rejeita transcrições de baixa confiança
            no_speech_threshold=0.6            # Mais agressivo na detecção de silêncio
        )
        # Materializa o gerador enquanto o lock está ativo
        segments = list(segments)

    elapsed = time.time() - start
    print(f"   {speaker_label} concluído em {round(elapsed, 1)}s (RTF: {round(elapsed/info.duration, 3)})") 
    sys.stdout.flush()

    # Reconstrói segmentos a partir dos timestamps de palavras
    # Um silêncio > 0.5s entre palavras cria uma nova linha
    results = []
    all_words = []
    for s in segments:
        if s.words:
            all_words.extend(s.words)

    if not all_words:
        return results

    GAP_THRESHOLD = 1.0  # segundos de silêncio para quebrar uma linha

    current_words = [all_words[0]]
    seg_start = all_words[0].start

    for i in range(1, len(all_words)):
        prev = all_words[i - 1]
        curr = all_words[i]
        gap = curr.start - prev.end

        if gap > GAP_THRESHOLD:
            text = " ".join(w.word.strip() for w in current_words).strip()
            if text:
                results.append({
                    "start": round(seg_start, 3),
                    "end": round(prev.end, 3),
                    "text": text
                })
            current_words = [curr]
            seg_start = curr.start
        else:
            current_words.append(curr)

    # Último segmento
    if current_words:
        text = " ".join(w.word.strip() for w in current_words).strip()
        if text:
            results.append({
                "start": round(seg_start, 3),
                "end": round(current_words[-1].end, 3),
                "text": text
            })

    return results

# ==============================
# PROCESSAMENTO DE ARQUIVO
# ==============================

def process_file(input_path):
    fn = os.path.basename(input_path)
    base = os.path.splitext(fn)[0]

    print(f"\n[>] Processando: {fn}")
    sys.stdout.flush()
    t0 = time.time()

    # 1. Split estéreo
    at_file, cl_file = split_stereo(input_path, base)
    if not at_file or not cl_file:
        with in_progress_lock:
            in_progress.discard(fn)
        return

    # 2. Transcrição paralela dos dois canais
    res_at = []
    res_cl = []

    with ThreadPoolExecutor(max_workers=2) as ex:
        fut_at = ex.submit(transcrever, at_file, "Atendente")
        fut_cl = ex.submit(transcrever, cl_file, "Cliente")
        res_at = fut_at.result()
        res_cl = fut_cl.result()

    # 3. Salva JSONs
    with open(os.path.join(OUTPUT_FOLDER, f"{base}_atendente.wav.json"), "w", encoding="utf-8") as f:
        json.dump(res_at, f, ensure_ascii=False, indent=2)
    with open(os.path.join(OUTPUT_FOLDER, f"{base}_cliente.wav.json"), "w", encoding="utf-8") as f:
        json.dump(res_cl, f, ensure_ascii=False, indent=2)

    # MODO DEBUG - Mantém os splits temporários para análise
    # for p in [at_file, cl_file]:
    #     if os.path.exists(p):
    #         os.remove(p)

    elapsed = round(time.time() - t0, 2)
    print(f"[<] Concluído: {fn} em {elapsed}s total")
    sys.stdout.flush()

    with in_progress_lock:
        in_progress.discard(fn)

# ==============================
# LOOP PRINCIPAL (WATCH FOLDER)
# ==============================

def main():
    print(f"Monitorando {AUDIO_FOLDER} por arquivos de áudio...")
    sys.stdout.flush()

    with ThreadPoolExecutor(max_workers=NUM_WORKERS) as executor:
        while True:
            try:
                all_files = [
                    f for f in os.listdir(AUDIO_FOLDER)
                    if f.lower().endswith((".mp3", ".m4a", ".wav"))
                ]

                for f in sorted(all_files):
                    base = os.path.splitext(f)[0]
                    out_json = os.path.join(OUTPUT_FOLDER, f"{base}_atendente.wav.json")

                    if os.path.exists(out_json):
                        continue  # Já processado

                    with in_progress_lock:
                        if f in in_progress:
                            continue
                        path = os.path.join(AUDIO_FOLDER, f)
                        if os.path.exists(path) and os.path.getsize(path) > 1000:
                            in_progress.add(f)
                            executor.submit(process_file, path)

                time.sleep(1)

            except Exception as e:
                print(f"Erro no loop: {e}")
                sys.stdout.flush()
                time.sleep(2)

if __name__ == "__main__":
    main()
