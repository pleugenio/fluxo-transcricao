#!/bin/bash
# Execute isso DENTRO do servidor haren como root

set -e

echo "=== AUTO-DEPLOY FLUXO-TRANSCRICAO ==="
echo ""

cd /opt
mkdir -p fluxo-transcricao
cd fluxo-transcricao

echo "📂 Clonando repositorio (HTTPS)..."
if [ -d ".git" ]; then
    echo "✓ Repositório já existe, atualizando..."
    git pull origin main
else
    echo "✓ Clonando novo repositório..."
    git clone https://github.com/pleugenio/fluxo-transcricao.git .
fi

echo "✓ Repositorio pronto"
echo ""

echo "📁 Criando estrutura de pastas..."
mkdir -p audios transcricoes
ls -la
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
