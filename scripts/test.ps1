$ErrorActionPreference = "Stop"
. "$PSScriptRoot\lib\common.ps1"

$repoRoot = Get-RepoRoot

Test-JsonFile (Join-Path $repoRoot "extensions\spotify-remote\menu.json")
Invoke-NativeTests -RepoRoot $repoRoot

Write-Host "Validation complete."
