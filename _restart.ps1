$ErrorActionPreference = "Continue"
$Root = (Get-Location).Path
$Bin = Join-Path $Root "go\bin"
$Log = Join-Path $Root "logs"
if (-not (Test-Path $Log)) { New-Item -ItemType Directory -Path $Log | Out-Null }
Write-Host "stopping existing coordinator / hive-agents / hub ..."
foreach ($n in @("coordinator", "hive-agents", "hub")) { Get-Process -Name $n -ErrorAction SilentlyContinue | ForEach-Object { try { $_.Kill() } catch {} } }
Start-Sleep -Milliseconds 800
function Start-Hive { param([string]$Name, [string]$Exe, [string[]]$Argv)
  $so = Join-Path $Log ($Name + ".out.log")
  $se = Join-Path $Log ($Name + ".err.log")
  $p = Start-Process -FilePath $Exe -ArgumentList $Argv -WorkingDirectory (Join-Path $Root "go") -RedirectStandardOutput $so -RedirectStandardError $se -PassThru -WindowStyle Hidden
  Write-Host ("started " + $Name + " pid " + $p.Id)
}
Start-Hive "hive-agents" (Join-Path $Bin "hive-agents.exe") @("-serve")
Start-Hive "hub" (Join-Path $Bin "hub.exe") @("-port", "9500")
Start-Hive "coordinator" (Join-Path $Bin "coordinator.exe") @("-port", "9200")
Write-Host "started"
