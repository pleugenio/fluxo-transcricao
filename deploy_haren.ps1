param(
    [string]$ServerHost = "haren",
    [string]$User = "prvpaulol",
    [string]$Password = "MK8D732qkugodislove@",
    [string]$RepoUrl = "git@github.com:pleugenio/fluxo-transcricao.git"
)

Write-Host "Deploy para servidor $ServerHost" -ForegroundColor Cyan

if (-not (Get-Module -Name Posh-SSH -ListAvailable)) {
    Install-Module -Name Posh-SSH -Force -Scope CurrentUser -Confirm:$false
}

Import-Module Posh-SSH

$SecPassword = ConvertTo-SecureString $Password -AsPlainText -Force
$Credential = New-Object System.Management.Automation.PSCredential($User, $SecPassword)

Write-Host "Conectando..." -ForegroundColor Cyan
$Session = New-SSHSession -ComputerName $ServerHost -Credential $Credential -AcceptKey -WarningAction SilentlyContinue

if ($Session) {
    Write-Host "Conectado!" -ForegroundColor Green
    
    Invoke-SSHCommand -SSHSession $Session -Command "mkdir -p /opt/fluxo-transcricao && cd /opt/fluxo-transcricao" 
    Invoke-SSHCommand -SSHSession $Session -Command "if [ -d '.git' ]; then git pull; else git clone $RepoUrl . ; fi"
    Invoke-SSHCommand -SSHSession $Session -Command "mkdir -p audios transcricoes"
    Invoke-SSHCommand -SSHSession $Session -Command "docker-compose down 2>/dev/null || true"
    Invoke-SSHCommand -SSHSession $Session -Command "docker-compose up -d --build"
    Invoke-SSHCommand -SSHSession $Session -Command "sleep 20"
    Invoke-SSHCommand -SSHSession $Session -Command "docker-compose ps"
    
    Write-Host "Deploy concluido!" -ForegroundColor Green
    
    Remove-SSHSession -SSHSession $Session -Confirm:$false
}
