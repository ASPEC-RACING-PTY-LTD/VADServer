# Runs a local development server with throwaway data in .dev-data/.
# Local test account used during development: username "admin", password "dev-admin-password" (throwaway data only).
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$data = Join-Path $root '.dev-data'
New-Item -ItemType Directory -Force -Path (Join-Path $data 'storage\default') | Out-Null
$env:VAULTDOCK_STATE_DIR = Join-Path $data 'state'
$env:VAULTDOCK_DEFAULT_STORAGE = Join-Path $data 'storage\default'
$env:VAULTDOCK_STORAGE_ROOTS = Join-Path $data 'storage'
$env:VAULTDOCK_LISTEN = '127.0.0.1:8080'
$env:VAULTDOCK_LOG_FORMAT = 'text'
$env:VAULTDOCK_LOG_LEVEL = 'info'
if (-not $env:VAULTDOCK_SETUP_TOKEN) { $env:VAULTDOCK_SETUP_TOKEN = 'dev-setup-token' }
Set-Location $root
go run ./cmd/vaultdock serve
