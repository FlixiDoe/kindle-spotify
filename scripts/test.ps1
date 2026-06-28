$ErrorActionPreference = "Stop"
. "$PSScriptRoot\lib\common.ps1"

$repoRoot = Get-RepoRoot

Test-JsonFile (Join-Path $repoRoot "extensions\spotify-remote\menu.json")
Test-JsonFile (Join-Path $repoRoot "extensions\spotifyremote\menu.json")
Invoke-NativeTests -RepoRoot $repoRoot

Write-Host "Validation complete."
