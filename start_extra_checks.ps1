# extra checks
if ($hubOk) {
    $idOk = $false; $idDetail = '' 
    try {
        $al = Invoke-RestMethod -Uri ($HubUrl + '/agents') -TimeoutSec 30
        $withContract = @($al.agents | Where-Object { $_.contractId })
        $withBal = @($al.agents | Where-Object { $_.balanceHive -ne $null })
        $sample = $al.agents[0]
        $idOk = ($withContract.Count -ge 1) -and ($withBal.Count -ge 1)
        $idDetail = 'agents=' + $al.agents.Count + ' withOnChainContract=' + $withContract.Count + ' withBalance=' + $withBal.Count + ' sample=' + $sample.id + ' contract=' + $sample.contractId + ' balance=' + $sample.balanceHive + ' HIVE'
    } catch { $idDetail = $_.Exception.Message }
    Add-Result 'each agent is uniquely identified by its own smart contract and shows its HIVE balance' $idOk $idDetail
    if (-not $idOk) { $script:Failed++ }
    $capOk = $false; $capDetail = '' 
    try {
        $cb = @{ task = 'Reply with one short sentence.'; history = @(@{role='user';content='my name is saptarsi'},@{role='assistant';content='noted'},@{role='user';content='what is my name?'}); historyLimit = 5 } | ConvertTo-Json -Compress -Depth 6
        $cr = Invoke-RestMethod -Uri ($HubUrl + '/task') -Method Post -ContentType 'application/json' -Headers @{ 'X-Hive-Selftest' = $script:SelfTestToken } -Body $cb -TimeoutSec 360
        $capOk = [bool]($cr.answer -and $cr.answer.Length -gt 0)
        $capDetail = 'answered ' + ([string]$cr.answer).Length + ' chars with a historyLimit cap'
    } catch { $capDetail = $_.Exception.Message }
    Add-Result 'conversation history with a configurable limit is accepted and answered' $capOk $capDetail
    if (-not $capOk) { $script:Failed++ }
}

# --- name recall: the user tells the agent their name (xyz), then asks it back with the full
# conversation history attached. This is the end-to-end memory test, reported in logs/name_test.log.
if ($hubOk) {
    $nameLog = Join-Path $LogDir 'name_test.log'
    Set-Content -Path $nameLog -Value ('Neural Hive name-recall test ' + (Get-Date).ToString('s'))
    $nameOk = $false; $nameDetail = '' 
    try {
        $t1 = 'Hello, my name is xyz. Please remember my name for later.'
        $q1 = @{ task = $t1 } | ConvertTo-Json -Compress
        $r1 = Invoke-RestMethod -Uri ($HubUrl + '/task') -Method Post -ContentType 'application/json' -Headers @{ 'X-Hive-Selftest' = $script:SelfTestToken } -Body $q1 -TimeoutSec 360
        $ans1 = [string]$r1.answer
        $t2 = 'What is my name?'
        $q2 = @{ task = $t2; history = @(@{ role = 'user'; content = $t1 }, @{ role = 'assistant'; content = $ans1 }) } | ConvertTo-Json -Compress -Depth 5
        $r2 = Invoke-RestMethod -Uri ($HubUrl + '/task') -Method Post -ContentType 'application/json' -Headers @{ 'X-Hive-Selftest' = $script:SelfTestToken } -Body $q2 -TimeoutSec 360
        $ans2 = [string]$r2.answer
        $nameOk = ($ans2 -match '(?i)xyz')
        $nameDetail = 'turn1_agents=' + (@($r1.agentsUsed).Count) + ' | recall_agents=' + (@($r2.agentsUsed).Count) + ' | recall_answer=' + ($ans2 -replace '[\r\n]+', ' ')
        Add-Content -Path $nameLog -Value ('USER: ' + $t1)
        Add-Content -Path $nameLog -Value ('HIVE: ' + $ans1)
        Add-Content -Path $nameLog -Value ('USER: ' + $t2)
        Add-Content -Path $nameLog -Value ('HIVE: ' + $ans2)
        Add-Content -Path $nameLog -Value ('RESULT: ' + (if ($nameOk) { 'PASS' } else { 'FAIL' }) + ' ' + $nameDetail)
        Add-Content -Path $e2eLog -Value '--- name-recall test (user name = xyz) ---'
        Add-Content -Path $e2eLog -Value ('USER: ' + $t1)
        Add-Content -Path $e2eLog -Value ('HIVE: ' + $ans1)
        Add-Content -Path $e2eLog -Value ('USER: ' + $t2)
        Add-Content -Path $e2eLog -Value ('HIVE: ' + $ans2)
    } catch { $nameDetail = $_.Exception.Message; Add-Content -Path $nameLog -Value ('ERROR: ' + $nameDetail) }
    Add-Result 'name recall (told the agent my name is xyz, then asked what is my name)' $nameOk $nameDetail
    if (-not $nameOk) { $script:Failed++ }
}
