. "$PSScriptRoot\common.ps1"

function Find-KindleDrive {
  param([string]$RequestedDriveLetter)

  if ($RequestedDriveLetter) {
    $letter = $RequestedDriveLetter.TrimEnd(":")
    $root = "${letter}:\"
    if (!(Test-Path $root)) {
      throw "Requested Kindle drive $root is not mounted."
    }
    return $root
  }

  $candidates = Get-CimInstance Win32_LogicalDisk |
    Where-Object {
      $_.DriveType -eq 2 -and
      ($_.VolumeName -eq "Kindle" -or (Test-Path (Join-Path $_.DeviceID "extensions")))
    } |
    Sort-Object @{Expression = { if ($_.VolumeName -eq "Kindle") { 0 } else { 1 } }}

  if (!$candidates) {
    throw "No Kindle USB drive found. Connect the Kindle or pass -DriveLetter I."
  }

  return "$($candidates[0].DeviceID)\"
}

function Copy-SpotifyExtensionToKindle {
  param(
    [string]$KindleRoot,
    [string]$RepoRoot = (Get-RepoRoot),
    [switch]$DeployActiveBinary
  )

  $extensionRoot = Get-SpotifyExtensionRoot -RepoRoot $RepoRoot
  $mirrorRoot = Get-SpotifyMirrorRoot -RepoRoot $RepoRoot
  $targetRoot = Join-Path $KindleRoot "extensions\spotify-remote"
  $targetMirrorRoot = Join-Path $KindleRoot "extensions\spotifyremote"

  Write-Host "Deploying to $targetRoot"
  New-Item -ItemType Directory -Force -Path $targetRoot | Out-Null
  New-Item -ItemType Directory -Force -Path (Join-Path $targetRoot "bin") | Out-Null
  New-Item -ItemType Directory -Force -Path (Join-Path $targetRoot "data") | Out-Null

  $localBinary = Join-Path $extensionRoot "bin\spotify-remote-arm"
  if (!(Test-Path $localBinary)) {
    throw "Binary not found: $localBinary. Run without -SkipBuild or build first."
  }

  $binaryName = if ($DeployActiveBinary) { "spotify-remote-arm" } else { "spotify-remote-arm.new" }
  $targetBinary = Join-Path $targetRoot "bin\$binaryName"
  Copy-RequiredFile -Source $localBinary -Destination $targetBinary

  $topLevelFiles = @(
    "config.xml",
    "menu.json",
    "launch.sh",
    "run-kual.sh",
    "run-native.sh",
    "stop.sh",
    "recover.sh",
    "nowplaying-launch.sh",
    "nowplaying.sh",
    "nowplaying-stop.sh",
    "start.sh",
    "README.md",
    "build.sh",
    "build.ps1"
  )

  foreach ($file in $topLevelFiles) {
    Copy-RequiredFile -Source (Join-Path $extensionRoot $file) -Destination (Join-Path $targetRoot $file)
  }

  foreach ($obsoleteDir in @("www")) {
    $obsoleteTargetDir = Join-Path $targetRoot $obsoleteDir
    if (Test-Path $obsoleteTargetDir) {
      Remove-Item -Recurse -Force -LiteralPath $obsoleteTargetDir
    }
  }

  foreach ($dir in @("src")) {
    $sourceDir = Join-Path $extensionRoot $dir
    $targetDir = Join-Path $targetRoot $dir
    if (Test-Path $targetDir) {
      Remove-Item -Recurse -Force -LiteralPath $targetDir
    }
    Copy-Item -Recurse -Force -LiteralPath $sourceDir -Destination $targetRoot
  }

  Copy-RequiredFile `
    -Source (Join-Path $extensionRoot "data\config.example.json") `
    -Destination (Join-Path $targetRoot "data\config.example.json")

  New-Item -ItemType Directory -Force -Path $targetMirrorRoot | Out-Null
  Copy-RequiredFile -Source (Join-Path $mirrorRoot "menu.json") -Destination (Join-Path $targetMirrorRoot "menu.json")
  Copy-RequiredFile -Source (Join-Path $mirrorRoot "config.xml") -Destination (Join-Path $targetMirrorRoot "config.xml")

  $localHash = Get-FileHash $localBinary -Algorithm SHA256
  $remoteHash = Get-FileHash $targetBinary -Algorithm SHA256
  if ($localHash.Hash -ne $remoteHash.Hash) {
    throw "Hash mismatch after deploy. Local=$($localHash.Hash) Kindle=$($remoteHash.Hash)"
  }

  return [pscustomobject]@{
    TargetRoot = $targetRoot
    BinaryPath = $targetBinary
    Hash       = $remoteHash.Hash
  }
}
