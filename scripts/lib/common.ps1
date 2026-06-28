$ErrorActionPreference = "Stop"

function Get-RepoRoot {
  $scriptDir = Split-Path -Parent $PSScriptRoot
  return (Resolve-Path (Join-Path $scriptDir "..")).Path
}

function Get-SpotifyExtensionRoot {
  param([string]$RepoRoot = (Get-RepoRoot))
  return (Join-Path $RepoRoot "extensions\spotify-remote")
}

function Get-SpotifyMirrorRoot {
  param([string]$RepoRoot = (Get-RepoRoot))
  return (Join-Path $RepoRoot "extensions\spotifyremote")
}

function Resolve-GoExe {
  if ($env:GOEXE) {
    if (!(Test-Path $env:GOEXE)) {
      throw "GOEXE points to a missing file: $env:GOEXE"
    }
    return $env:GOEXE
  }

  $goCommand = Get-Command go -ErrorAction SilentlyContinue
  if ($goCommand) {
    return $goCommand.Source
  }

  throw "Go toolchain not found. Install Go, add it to PATH, or set GOEXE to the full go.exe path."
}

function Set-KindleGoEnv {
  param(
    [string]$GoArm = "7"
  )

  $env:CGO_ENABLED = "0"
  $env:GO111MODULE = "off"
  $env:GOOS = "linux"
  $env:GOARCH = "arm"
  $env:GOARM = $GoArm
}

function Invoke-NativeBuild {
  param(
    [string]$RepoRoot = (Get-RepoRoot),
    [string]$GoArm = "7"
  )

  $extensionRoot = Get-SpotifyExtensionRoot -RepoRoot $RepoRoot
  $outDir = Join-Path $extensionRoot "bin"
  $out = Join-Path $outDir "spotify-remote-arm"
  $goExe = Resolve-GoExe

  New-Item -ItemType Directory -Force -Path $outDir | Out-Null
  Push-Location $extensionRoot
  try {
    Set-KindleGoEnv -GoArm $GoArm
    & $goExe build -trimpath -ldflags "-s -w" -o $out ".\src\native"
  } finally {
    Pop-Location
  }

  return $out
}

function Invoke-NativeTests {
  param(
    [string]$RepoRoot = (Get-RepoRoot)
  )

  $goExe = Resolve-GoExe
  $extensionRoot = Get-SpotifyExtensionRoot -RepoRoot $RepoRoot
  Push-Location $extensionRoot
  try {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARM -ErrorAction SilentlyContinue
    Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue
    & $goExe test ./src/native
    if ($LASTEXITCODE -ne 0) {
      throw "Go native tests failed with exit code $LASTEXITCODE."
    }
  } finally {
    Pop-Location
  }
}

function Test-JsonFile {
  param([string]$Path)

  python -m json.tool $Path > $null
}

function Copy-RequiredFile {
  param(
    [string]$Source,
    [string]$Destination
  )

  if (!(Test-Path $Source)) {
    throw "Required file not found: $Source"
  }

  $parent = Split-Path -Parent $Destination
  New-Item -ItemType Directory -Force -Path $parent | Out-Null
  Copy-Item -Force -LiteralPath $Source -Destination $Destination
}
