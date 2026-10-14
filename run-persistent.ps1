<#
.SYNOPSIS
单个审核实例的守护脚本，通常由 start-persistent.ps1 后台启动。

.PARAMETER Instance
必填：gvs 或 gvs-infer。分别加载 configs/config-gvs.yaml 和
configs/config-gvs-infer.yaml，监听本机 8081 和 8082 端口。

.EXAMPLE
# 前台运行 GVS 守护循环；后台运行请使用 start-persistent.ps1。
.\run-persistent.ps1 -Instance gvs

.NOTES
凭据从 %LOCALAPPDATA%\AI_IR_REPLAY\runtime-env.ps1 加载。
日志写入项目 data\runtime；审核进程退出后等待 15 秒重新启动。
文件锁阻止同一用户重复启动相同实例。
停止守护进程后需同时结束其审核子进程，才能完全停止服务。
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





