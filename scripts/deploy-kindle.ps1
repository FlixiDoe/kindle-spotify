param(
  [string]$DriveLetter = "",
  [switch]$SkipBuild,
  [switch]$DeployActiveBinary,
  [string]$GoArm = "7"
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot\lib\kindle.ps1"

$repoRoot = Get-RepoRoot

if (!$SkipBuild) {
  $binary = Invoke-NativeBuild -RepoRoot $repoRoot -GoArm $GoArm
  Write-Host "Built $binary"
}

$kindleRoot = Find-KindleDrive -RequestedDriveLetter $DriveLetter
$result = Copy-SpotifyExtensionToKindle `
  -KindleRoot $kindleRoot `
  -RepoRoot $repoRoot `
  -DeployActiveBinary

Write-Host "Deploy complete."
Write-Host "Kindle binary: $($result.BinaryPath)"
Write-Host "SHA256: $($result.Hash)"
Write-Host "Local config/token files on the Kindle were preserved."
Write-Host "Eject the Kindle, then start KUAL -> Spotify Remote -> Now Playing Display."
