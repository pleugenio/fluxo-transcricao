#!/bin/bash
# Execute isso DENTRO do servidor haren como root

set -e

echo "=== AUTO-DEPLOY FLUXO-TRANSCRICAO ==="
echo ""

cd /opt
mkdir -p fluxo-transcricao
cd fluxo-transcricao

echo "📂 Clonando repositorio..."
if [ -d ".git" ]; then
    git pull origin main
else
    git clone git@github.com:pleugenio/fluxo-transcricao.git .
fi

echo "✓ Repositorio atualizado"
echo ""

echo "📁 Criando estrutura de pastas..."
mkdir -p audios transcricoes
echo "✓ Pastas criadas"
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
echo "  docker-compose logs -f pipeline"
