# Stage 1: Builder
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copia arquivos Go (apenas os necessários para o pipeline)
COPY main.go web.go ./
COPY go.mod go.sum ./

# Build
RUN go build -o pipeline main.go web.go

# Stage 2: Runtime (minimalista)
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata openssh-client

WORKDIR /app

# Copia binário do builder
COPY --from=builder /app/pipeline .

# SSH key para conexão com VM (AWS)
COPY Chave_Temp_Paulo.pem .
RUN chmod 600 Chave_Temp_Paulo.pem

# Volumes
VOLUME ["/app/audios", "/app/temp"]

# Variáveis de ambiente
ENV POSTGRES_URL="postgres://srvbi:NbHo2WB8EyzatlPjmD1e@db:5432/transcriberdb"
ENV REMOTE_HOST="172.31.24.27"
ENV REMOTE_USER="speaksense"
ENV REMOTE_KEY="/app/Chave_Temp_Paulo.pem"

CMD ["./pipeline"]
