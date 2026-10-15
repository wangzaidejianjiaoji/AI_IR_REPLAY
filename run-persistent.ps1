<#
.SYNOPSIS
单实例定时审核守护循环；通常由 start-persistent.ps1 后台启动。
.EXAMPLE
.\run-persistent.ps1 -Instance gvs          # 前台守护 GVS
.\run-persistent.ps1 -Instance gvs-infer    # 前台守护 infer
.NOTES
加载 configs/config-<实例>.yaml 和本机 %LOCALAPPDATA%\AI_IR_REPLAY\runtime-env.ps1。
文件锁防重复启动；审核进程退出后 15 秒重启，日志在 data/runtime。
停止时先结束本守护进程，再结束审核子进程；手动指定 MR 请用 start-persistent.ps1。
#>
param([Parameter(Mandatory=$true)][ValidateSet('gvs','gvs-infer')][string]$Instance)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = [Console]::OutputEncoding
$workspace = $PSScriptRoot
$runtimeDir = Join-Path $env:LOCALAPPDATA 'AI_IR_REPLAY'
$logDir = Join-Path $workspace 'data\runtime'
New-Item -ItemType Directory -Path $logDir -Force | Out-Null
Set-Location -LiteralPath $workspace
$supervisorLock = $null
try {
    $supervisorLock = [IO.File]::Open((Join-Path $runtimeDir ($Instance + '.supervisor.lock')), 'OpenOrCreate', 'ReadWrite', 'None')
} catch { exit 0 }
try {
    while ($true) {
        try {
            . (Join-Path $runtimeDir 'runtime-env.ps1')
            Remove-Item Env:AGENT_REVIEW_DRY_RUN -ErrorAction SilentlyContinue
            $env:ZONEINFO = Join-Path $workspace '.gotmp\go1.23.12\go\lib\time\zoneinfo.zip'
            $env:AGENT_SERVER_ADDR = if ($Instance -eq 'gvs') { '127.0.0.1:8081' } else { '127.0.0.1:8082' }
            foreach ($required in @('KIMI_API_KEY','CODEUP_TOKEN','CODEUP_ORG_ID')) {
                if (-not [Environment]::GetEnvironmentVariable($required)) { throw "Missing environment variable: $required" }
            }
            $logPath = Join-Path $logDir ($Instance + '-' + (Get-Date -Format 'yyyy-MM-dd') + '.log')
            Add-Content -LiteralPath $logPath -Encoding Unicode -Value ("[{0}] Starting {1}" -f (Get-Date -Format o), $Instance)
            & (Join-Path $workspace 'bin\gvs-pr-agent.exe') -config ("configs/config-{0}.yaml" -f $Instance) >> $logPath 2>&1
            Add-Content -LiteralPath $logPath -Encoding Unicode -Value ("[{0}] Exited: {1}; restarting in 15 seconds" -f (Get-Date -Format o), $LASTEXITCODE)
        } catch {
            Add-Content -LiteralPath (Join-Path $logDir ($Instance + '-supervisor.log')) -Encoding Unicode -Value ("[{0}] {1}" -f (Get-Date -Format o), $_.Exception.Message)
        }
        Start-Sleep -Seconds 15
    }
} finally { if ($supervisorLock) { $supervisorLock.Dispose() } }





