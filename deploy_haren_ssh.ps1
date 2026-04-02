# Script PowerShell para deploy no servidor haren com SSH key

param(
    [string]$Host = "haren",
    [string]$User = "prvpaulol",
    [string]$Password = "MK8D732qkugodislove@",
    [string]$RepoUrl = "git@github.com:pleugenio/fluxo-transcricao.git"
)

Write-Host "🚀 Deploy para servidor $Host (usando SSH key de /root/.ssh/)" -ForegroundColor Cyan
Write-Host ""

# Instala Posh-SSH se não existir
if (-not (Get-Module -Name Posh-SSH -ListAvailable)) {
    Write-Host "⚠️  Instalando Posh-SSH..." -ForegroundColor Yellow
    Install-Module -Name Posh-SSH -Force -Scope CurrentUser -Confirm:$false
}

Import-Module Posh-SSH

# Cria credential
$SecPassword = ConvertTo-SecureString $Password -AsPlainText -Force
$Credential = New-Object System.Management.Automation.PSCredential($User, $SecPassword)

try {
    Write-Host "🔗 Conectando a $Host como $User..." -ForegroundColor Cyan
    $Session = New-SSHSession -ComputerName $Host -Credential $Credential -AcceptKey -WarningAction SilentlyContinue

    if ($Session) {
        Write-Host "✓ Conectado com sucesso!" -ForegroundColor Green
        Write-Host ""

        # Comandos a executar
        $Commands = @(
            "echo '📂 Preparando diretório...'",
            "mkdir -p /opt/fluxo-transcricao && cd /opt/fluxo-transcricao",
            "git config --global user.email 'deploy@haren.local'",
            "git config --global user.name 'Deploy Bot'",
            "if [ -d '.git' ]; then echo '✓ Repositório existe, atualizando...'; git pull; else echo '✓ Clonando repositório...'; git clone $RepoUrl . ; fi",
            "mkdir -p audios transcricoes",
            "echo '✓ Arquivos preparados'",
            "echo '🐳 Iniciando Docker...'",
            "docker-compose down 2>/dev/null || true",
            "docker-compose up -d --build",
            "echo '⏳ Aguardando inicialização (20 segundos)...'",
            "sleep 20",
            "echo '📊 Status dos containers:'",
            "docker-compose ps",
            "echo ''",
            "echo '✅ Deploy concluído com sucesso!'"
        )

        foreach ($cmd in $Commands) {
            $result = Invoke-SSHCommand -SSHSession $Session -Command $cmd
            Write-Host $result.Output
            
            if ($result.ExitStatus -ne 0) {
                # Ignora erros de docker-compose down (pode não existir)
                if ($cmd -notmatch "down|true|Aguardando") {
                    Write-Host "⚠️  Exit code: $($result.ExitStatus)" -ForegroundColor Yellow
                }
            }
        }

        Write-Host ""
        Write-Host "✅ Deploy finalizado!" -ForegroundColor Green
        Write-Host ""
        Write-Host "📊 Para monitorar logs, execute:" -ForegroundColor Cyan
        Write-Host "  ssh $User@$Host" -ForegroundColor Yellow
        Write-Host "  cd /opt/fluxo-transcricao" -ForegroundColor Yellow
        Write-Host "  docker-compose logs -f pipeline" -ForegroundColor Yellow
        
    } else {
        Write-Host "❌ Falha na conexão ao servidor" -ForegroundColor Red
    }
} catch {
    Write-Host "❌ Erro: $_" -ForegroundColor Red
} finally {
    if ($Session) {
        Remove-SSHSession -SSHSession $Session -Confirm:$false
    }
}
