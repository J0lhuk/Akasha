param([string]$Data = (Join-Path $env:APPDATA 'Akasha'), [int]$Port = 7341)
$ErrorActionPreference = 'Stop'
$AkashaRoot = Split-Path $PSScriptRoot -Parent
$AkashaExecutable = Join-Path $AkashaRoot 'bin\akasha.exe'
if (-not (Test-Path -LiteralPath $AkashaExecutable)) {
    Push-Location $AkashaRoot
    try { go build -trimpath -o $AkashaExecutable ./cmd/akasha; if ($LASTEXITCODE -ne 0) { throw 'Go build failed' } }
    finally { Pop-Location }
}
& $AkashaExecutable init --data $Data
if ($LASTEXITCODE -ne 0) { throw 'Initialization failed' }
Write-Host "UI: http://127.0.0.1:$Port"
Write-Host 'В другом терминале получите локальный токен командой akasha token --data <тот же каталог>.'
& $AkashaExecutable serve --data $Data --listen "127.0.0.1:$Port"
exit $LASTEXITCODE
