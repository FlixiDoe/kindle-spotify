param(
  [string]$GoArm = "7"
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot\lib\common.ps1"

$binary = Invoke-NativeBuild -GoArm $GoArm
Write-Host "Built $binary"
Write-Host "Target: GOOS=linux GOARCH=arm GOARM=$GoArm CGO_ENABLED=0"
