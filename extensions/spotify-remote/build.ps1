$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$outDir = Join-Path $root "bin"
$out = Join-Path $outDir "spotify-remote-arm"

New-Item -ItemType Directory -Force -Path $outDir | Out-Null
Push-Location $root
try {

  $env:CGO_ENABLED = "0"
  $env:GO111MODULE = "off"
  $env:GOOS = "linux"
  $env:GOARCH = "arm"
  $env:GOARM = "7"
  go build -trimpath -ldflags "-s -w" -o $out ".\src\native"

  Write-Host "Built $out"
  Write-Host "On Kindle, ensure executable bit if needed: chmod 755 extensions/spotify-remote/bin/spotify-remote-arm"
} finally {
  Pop-Location
}
