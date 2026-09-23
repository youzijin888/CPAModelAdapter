param([string]$Binary = (Join-Path $PSScriptRoot 'cpa.exe'))
$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) {
    throw 'CPA executable not found. Usage: .\install.ps1 .\dist\cpa-windows-amd64.exe'
}
& $Binary _setup
if ($LASTEXITCODE -ne 0) { throw 'CPA installation failed; review the message above.' }
