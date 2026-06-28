param(
  [string]$OutputPath = "",
  [switch]$SkipBuild,
  [string]$GoArm = "7"
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot\lib\common.ps1"

$repoRoot = Get-RepoRoot
$extensionRoot = Get-SpotifyExtensionRoot -RepoRoot $repoRoot

if (!$SkipBuild) {
  $binary = Invoke-NativeBuild -RepoRoot $repoRoot -GoArm $GoArm
  Write-Host "Built $binary"
}

if (!$OutputPath) {
  $distDir = Join-Path $repoRoot "dist"
  New-Item -ItemType Directory -Force -Path $distDir | Out-Null
  $OutputPath = Join-Path $distDir "spotify-remote-kual.zip"
}

$localBinary = Join-Path $extensionRoot "bin\spotify-remote-arm"
if (!(Test-Path $localBinary)) {
  throw "Binary not found: $localBinary. Build first or run without -SkipBuild."
}

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("kindle-spotify-package-" + [guid]::NewGuid().ToString("N"))
$tempExtension = Join-Path $tempRoot "extensions\spotify-remote"

try {
  New-Item -ItemType Directory -Force -Path $tempExtension | Out-Null
  Copy-Item -Recurse -Force -Path (Join-Path $extensionRoot "*") -Destination $tempExtension

  foreach ($runtimeFile in @("config.json", "token.json", "oauth.json", "callback.txt", "login_url.txt", "status.txt")) {
    $path = Join-Path $tempExtension "data\$runtimeFile"
    if (Test-Path $path) {
      Remove-Item -Force -LiteralPath $path
    }
  }

  $logs = Join-Path $tempExtension "logs"
  if (Test-Path $logs) {
    Remove-Item -Recurse -Force -LiteralPath $logs
  }

  $parent = Split-Path -Parent $OutputPath
  if ($parent) {
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
  }

  if (Test-Path $OutputPath) {
    Remove-Item -Force -LiteralPath $OutputPath
  }

  Add-Type -AssemblyName System.IO.Compression.FileSystem
  [System.IO.Compression.ZipFile]::CreateFromDirectory($tempRoot, $OutputPath)
  Write-Host "Packaged $OutputPath"
} finally {
  if (Test-Path $tempRoot) {
    Remove-Item -Recurse -Force -LiteralPath $tempRoot
  }
}
