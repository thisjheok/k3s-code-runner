param(
  [Parameter(Mandatory = $true)]
  [string]$VmHost,

  [string]$VmUser = "jhjang",

  [string]$VmPath = "~/mini-code-runner/mini-code-runner"
)

$ErrorActionPreference = "Stop"

$ProjectRoot = Split-Path -Parent $PSScriptRoot
$Target = "${VmUser}@${VmHost}:${VmPath}"

$Items = @(
  "Dockerfile",
  "README.md",
  "go.mod",
  "go.sum",
  "cmd",
  "deploy",
  "internal",
  "web"
)

Write-Host "Creating target directory on ${VmUser}@${VmHost}:${VmPath}"
ssh "${VmUser}@${VmHost}" "mkdir -p ${VmPath}"

foreach ($Item in $Items) {
  $Source = Join-Path $ProjectRoot $Item
  if (Test-Path $Source) {
    Write-Host "Syncing $Item"
    scp -r $Source $Target
  }
}

Write-Host "Sync complete."
