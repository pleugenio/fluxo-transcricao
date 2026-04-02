#!/bin/bash
# Execute isso DENTRO do servidor haren como root
# Uso: bash remote_deploy_https_token.sh seu_github_token

TOKEN=${1:-$GITHUB_TOKEN}

if [ -z "$TOKEN" ]; then
    echo "❌ Token não fornecido!"
    echo ""
    echo "Uso:"
    echo "  bash remote_deploy_https_token.sh SEU_GITHUB_TOKEN"
    echo ""
    echo "Ou defina a variável:"
    echo "  export GITHUB_TOKEN=seu_token"
    echo "  bash remote_deploy_https_token.sh"
    exit 1
fi

set -e

echo "=== AUTO-DEPLOY FLUXO-TRANSCRICAO ==="
echo "Token: ${TOKEN:0:10}...***"
echo ""

cd /opt
mkdir -p fluxo-transcricao
cd fluxo-transcricao

echo "📂 Clonando repositorio (HTTPS com Token)..."
if [ -d ".git" ]; then
    echo "✓ Repositório já existe, atualizando..."
    git pull origin main
else
    echo "✓ Clonando novo repositório..."
    git clone https://pleugenio:${TOKEN}@github.com/pleugenio/fluxo-transcricao.git .
fi

echo "✓ Repositorio pronto"
echo ""

echo "📁 Criando estrutura de pastas..."
mkdir -p audios transcricoes
echo "✓ Pastas criadas"
echo ""

echo "🔍 Verificando Docker..."
docker --version
docker-compose --version
echo "✓ Docker disponível"
echo ""

echo "🐳 Iniciando Docker Compose..."
docker-compose down 2>/dev/null || true
docker-compose up -d --build

echo "⏳ Aguardando inicializacao (20 segundos)..."
sleep 20

echo ""
echo "📊 Status dos containers:"
docker-compose ps

echo ""
echo "✅ Deploy concluido com sucesso!"
echo ""
echo "Para ver logs em tempo real:"
echo "  cd /opt/fluxo-transcricao"
echo "  docker-compose logs -f pipeline"
echo ""
echo "Para parar os containers:"
echo "  docker-compose down"
