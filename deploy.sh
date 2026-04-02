#!/bin/bash

HOST="haren"
USER="prvpaulol"
PASS="MK8D732qkugodislove@"
REPO="git@github.com:pleugenio/fluxo-transcricao.git"

echo "🚀 Deploy iniciado..."
echo ""
echo "Conectando a $HOST via SSH..."
echo ""

# Executa comandos via SSH
ssh -o StrictHostKeyChecking=accept-new "$USER@$HOST" << SSHCMD
set -e
echo "✓ Conectado"
cd /root
mkdir -p /opt/fluxo-transcricao
cd /opt/fluxo-transcricao
echo "✓ Diretorio criado"

if [ -d ".git" ]; then
    echo "✓ Atualizando repositorio..."
    git pull
else
    echo "✓ Clonando repositorio..."
    git clone $REPO .
fi

mkdir -p audios transcricoes
echo "✓ Pastas criadas"

echo "🐳 Iniciando Docker..."
docker-compose down 2>/dev/null || true
docker-compose up -d --build

echo "⏳ Aguardando (20s)..."
sleep 20

echo "📊 Status:"
docker-compose ps

echo ""
echo "✅ Deploy concluido!"
SSHCMD
