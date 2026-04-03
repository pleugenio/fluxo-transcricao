# Stage 1: Builder
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copia arquivos Go
COPY *.go .
COPY go.mod .
COPY go.sum .

# Build
RUN go build -o pipeline .

# Stage 2: Runtime (minimalista)
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata openssh-client

WORKDIR /app

# Copia binário do builder
COPY --from=builder /app/pipeline .

# SSH key para conexão com VM
COPY vm-speaksense-eus-dev_key.pem .
RUN chmod 600 vm-speaksense-eus-dev_key.pem

# Volumes
VOLUME ["/app/audios", "/app/temp"]

# Variáveis de ambiente
ENV POSTGRES_URL="postgres://srvbi:NbHo2WB8EyzatlPjmD1e@db:5432/transcriberdb"
ENV REMOTE_HOST="20.127.212.253"
ENV REMOTE_USER="speaksense"
ENV REMOTE_KEY="/app/vm-speaksense-eus-dev_key.pem"

CMD ["./pipeline"]
