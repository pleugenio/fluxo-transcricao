#!/bin/bash

HOST="haren"
USER="prvpaulol"
REPO="https://github.com/seu-usuario/fluxo-transcricao.git"

echo "🚀 Deploy para servidor $HOST (10.0.68.178)"
echo ""
echo "Conectando como $USER..."
echo ""

# Cria comando SSH que será executado no servidor
COMMANDS='
set -e

echo "✓ Conectado ao servidor haren"
echo ""

# Cria diretório
mkdir -p /opt/fluxo-transcricao
cd /opt/fluxo-transcricao
echo "✓ Diretório /opt/fluxo-transcricao criado"

# Clone ou pull
if [ -d ".git" ]; then
    echo "✓ Repositório existe, atualizando..."
    git pull origin main
else
    echo "✓ Clonando repositório..."
    git clone '"$REPO"' .
fi

# Cria pastas
mkdir -p audios transcricoes
echo "✓ Pastas audios e transcricoes criadas"

# Verifica Docker
if ! command -v docker &> /dev/null; then
    echo "❌ Docker não instalado"
    exit 1
fi

echo "✓ Docker disponível"
echo ""

# Inicia containers
echo "🐳 Iniciando Docker Compose..."
docker-compose down 2>/dev/null || true
docker-compose up -d --build

echo "⏳ Aguardando inicialização (20 segundos)..."
sleep 20

echo ""
echo "📊 Status dos containers:"
docker-compose ps

echo ""
echo "✅ Deploy concluído!"
echo "📝 Para ver logs: ssh '"$USER"'@'"$HOST"' -c \"cd /opt/fluxo-transcricao && docker-compose logs -f pipeline\""
'

# Executa via SSH (usuário precisa colocar senha)
ssh -o StrictHostKeyChecking=accept-new "$USER@$HOST" "$COMMANDS"
