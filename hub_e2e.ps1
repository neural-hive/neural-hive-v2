<#
.SYNOPSIS
  Neural Hive hub end-to-end verification: the simple request (HIVE = 1) and the complex
  request (HIVE >= 2) are executed against the running Hub. The real execution trace of both
  runs, the real DeepSeek responses and the on-chain anchor are written to logs/e2e.log.
#>
[CmdletBinding()]
param([int]$HubPort = 0)

$ErrorActionPreference = "Continue"
$Root = $PSScriptRoot
if (-not $Root) { $Root = (Get-Location).Path }
$LogDir = Join-Path $Root "logs"
if (-not (Test-Path $LogDir)) { New-Item -ItemType Directory -Path $LogDir | Out-Null }
$E2E = Join-Path $LogDir "e2e.log"
$Errors = Join-Path $LogDir "errors.log"
$Front = Join-Path $LogDir "frontend.log"
$AgentLog = Join-Path $LogDir "agents.log"
$script:Checks = 0
$script:Failed = 0

function Stamp { return (Get-Date).ToUniversalTime().ToString("yyyy-MM-dd HH:mm:ssZ") }
function Add-Line { param([string]$File,[string]$Message) Add-Content -Path $File -Value ("[" + (Stamp) + "] " + $Message) }
function Log { param([string]$m) Add-Line $E2E $m; Write-Host $m }
function LogFront { param([string]$m) Add-Line $Front $m; Write-Host $m }
function Fail { param([string]$m) Add-Line $E2E ("FAIL " + $m); Add-Line $Errors ("FAIL " + $m); Write-Host ("FAIL " + $m) -ForegroundColor Red }
function Check { param([string]$Name,[bool]$Ok,[string]$Detail)
  $script:Checks++
  if ($Ok) { Log ("CHECK PASS " + $Name + " :: " + $Detail) } else { $script:Failed++; Fail ("CHECK FAIL " + $Name + " :: " + $Detail) }
  return $Ok
}
