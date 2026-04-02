# 🐳 Docker Setup - Pipeline LOCAL ONLY

**Arquitetura:**
- 🖥️ **Sua máquina (Docker)**: main.go + PostgreSQL
- ☁️ **Azure VM**: Whisper transcrição + GPU
- 🔗 **Comunicação**: SSH + SFTP

## 📋 Pré-requisitos

- Docker 20.10+
- Docker Compose 1.29+
- Mínimo 4GB RAM
- SSH key para Azure VM
- DB2 ODBC configurado localmente (para buscar metadados)

## 🚀 Começar

### 1. Preparar projeto

```bash
# Clone/copie o repositório
cd fluxo-transcricao

# Copie a SSH key da VM para a pasta
cp /caminho/para/vm-speaksense-eus-dev_key.pem .

# Crie a pasta de áudios
mkdir -p audios
```

### 2. Subir containers

```bash
# Build e start
docker-compose up -d

# Verificar status
docker-compose ps

# Ver logs
docker-compose logs -f pipeline
```

### 3. Colocar áudios para processar

```bash
# Copiar áudios para a pasta local
cp seus_audios/*.mp3 audios/

# O pipeline vai detectar automaticamente e:
# 1. Enviar para VM via SFTP
# 2. Esperar transcrição
# 3. Buscar metadados no DB2
# 4. Salvar em PostgreSQL
```

## 📊 Monitoramento

### Logs em tempo real
```bash
docker-compose logs -f pipeline
```

### Status do banco
```bash
docker-compose exec db psql -U srvbi -d transcriberdb

# Listar transcrições
SELECT gravacao_id, nme_pessoa, processado_em 
FROM transcricoes 
ORDER BY processado_em DESC 
LIMIT 10;
```

### Ver dados em formato JSON
```bash
docker-compose exec db psql -U srvbi -d transcriberdb \
  -c "SELECT gravacao_id, timeline_json FROM transcricoes LIMIT 1;"
```

## 🔧 Arquitetura

```
MÁQUINA LOCAL (Docker)
├─ Pipeline Container (Go)
│  ├─ main.go - Orquestra tudo
│  ├─ SSH → VM para enviar áudios
│  ├─ ODBC → DB2 para buscar metadados
│  └─ PostgreSQL → Salva resultados
└─ PostgreSQL Container
   └─ Armazena todas as transcrições

AZURE VM (Não-Docker)
├─ watcher.sh - Monitora /audios
├─ transcrever_canal.py - Whisper + GPU
└─ ffmpeg - Processamento de áudio
```

## 📁 Estrutura Local

```
fluxo-transcricao/
├── Dockerfile
├── docker-compose.yml
├── init.sql                    # Schema PostgreSQL
├── main.go                     # Pipeline
├── audios/                     # 📥 Coloque MP3s aqui
├── temp/                       # Arquivos temporários
├── transcricoes/              # 📤 Saídas (JSONs)
├── vm-speaksense-eus-dev_key.pem  # SSH key
└── DOCKER.md                  # Esta documentação
```

## ⚙️ Variáveis de Ambiente

No `docker-compose.yml`, configure:

```yaml
environment:
  POSTGRES_URL: "postgres://user:pass@db:5432/transcriberdb"
  REMOTE_HOST: "20.127.212.253"        # IP da VM Azure
  REMOTE_USER: "speaksense"            # Usuário VM
  REMOTE_KEY: "/app/vm-speaksense-eus-dev_key.pem"
```

## 🛑 Parar / Limpar

```bash
# Parar (mantém dados)
docker-compose down

# Remover tudo (⚠️ deleta PostgreSQL!)
docker-compose down -v

# Limpar apenas volumes
docker volume rm transcricao-db-volume
```

## 🔐 Segurança

⚠️ **Antes de usar em produção:**

1. **Mudar senha PostgreSQL** no docker-compose.yml
2. **Não commitar SSH key** - adicione ao .gitignore
3. **Usar .env file:**
   ```bash
   # .env
   POSTGRES_PASSWORD=sua_senha_super_segura
   REMOTE_KEY_PATH=/seu/caminho/chave.pem
   ```
4. **Limitar acesso ao PostgreSQL** (porta 5433)

## 🐛 Troubleshooting

### "Connection refused to VM"
```bash
# Verificar conectividade SSH
ssh -i vm-speaksense-eus-dev_key.pem speaksense@20.127.212.253 "echo OK"
```

### "PostgreSQL not ready"
```bash
# Aguarde 30 segundos na primeira execução
docker-compose logs -f db
```

### "DB2 ODBC not found"
```bash
# Precisa ter DB2 ODBC instalado localmente na máquina
# (Não é necessário dentro do Docker)
odbc64 # ou odbcinst -j para verificar
```

### "SSH key permission denied"
```bash
# Permissões corretas
chmod 600 vm-speaksense-eus-dev_key.pem
```

## 📈 Fluxo de Dados

```
1. Você coloca MP3 em ./audios
   ↓
2. Pipeline detecta (inotifywait)
   ↓
3. Envia para VM via SFTP
   ↓
4. Watcher na VM processa (Whisper)
   ↓
5. Pipeline baixa resultado
   ↓
6. Busca metadados no DB2 (ODBC local)
   ↓
7. Salva em PostgreSQL local
   ↓
8. JSON em ./transcricoes/
```

## 📚 Referências

- [Docker Docs](https://docs.docker.com/)
- [PostgreSQL Docker](https://hub.docker.com/_/postgres)
- [faster-whisper](https://github.com/guillaumekln/faster-whisper)
- [Azure VMs](https://azure.microsoft.com/services/virtual-machines/)

## ✅ Checklist Pré-Deploy

- [ ] Docker + Docker Compose instalados
- [ ] SSH key copiada para pasta
- [ ] ODBC para DB2 configurado
- [ ] Pasta audios/ criada
- [ ] docker-compose.yml revisado
- [ ] Firewall permite SSH à VM (porta 22)
- [ ] Firewall permite PostgreSQL (porta 5433)

---

**Status**: Pronto para produção ✓

