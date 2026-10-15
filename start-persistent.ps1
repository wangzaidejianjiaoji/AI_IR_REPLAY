<#
.SYNOPSIS
启动后台定时审核，或前台执行一次指定 MR 审核。
.EXAMPLE
.\start-persistent.ps1                                           # 定时：GVS + infer
.\start-persistent.ps1 -Mode scheduled -Instance gvs              # 定时：仅 GVS
.\start-persistent.ps1 -Instance gvs-infer                         # 定时：仅 infer
.\start-persistent.ps1 -Mode manual -Instance gvs -MR 123 -DryRun   # 手动：替换编号；移除 -DryRun 发布评论
.\start-persistent.ps1 -Action status                            # 检查全部实例
.\start-persistent.ps1 -Action stop -Instance gvs                 # 停止 GVS 守护与审核进程
.\start-persistent.ps1 -Action restart -Instance gvs-infer         # 重启 infer
.NOTES
在项目目录运行。手动模式必须选单个实例；先停止其守护进程和审核进程。
可选 -Repo <配置内仓库全路径>、-CheckpointDir <目录>；默认检查点为 data/checkpoints/<实例>。
定时模式后台运行，审核进程退出后 15 秒重启；日志在 data/runtime。
依赖新版 bin/gvs-pr-agent.exe 和 %LOCALAPPDATA%\AI_IR_REPLAY\runtime-env.ps1。
执行策略受限时：powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\start-persistent.ps1
#>
[CmdletBinding()]
param(
    [ValidateSet('all', 'gvs', 'gvs-infer')]
    [string]$Instance = 'all',
    [ValidateSet('scheduled', 'manual')]
    [string]$Mode = 'scheduled',
    [int]$MR,
    [string]$Repo,
    [switch]$DryRun,
    [string]$CheckpointDir,
    [ValidateSet('start', 'stop', 'restart', 'status')]
    [string]$Action = 'start'
)

$ErrorActionPreference = 'Stop'
if ($Action -ne 'start') {
    foreach ($reviewOption in @('Mode', 'MR', 'Repo', 'DryRun', 'CheckpointDir')) {
        if ($PSBoundParameters.ContainsKey($reviewOption)) { throw "-$reviewOption cannot be combined with -Action $Action." }
    }
    $controller = Join-Path $PSScriptRoot 'bin\start-persistent.exe'
    if (-not (Test-Path -LiteralPath $controller -PathType Leaf)) { $controller = Join-Path $PSScriptRoot 'bin\gvs-pr-agent.exe' }
    if (-not (Test-Path -LiteralPath $controller -PathType Leaf)) { throw 'Build bin/start-persistent.exe from ./cmd/agent before managing instances.' }
    & $controller -action $Action -instance $Instance
    exit $LASTEXITCODE
}
if ($Mode -eq 'manual') {
    if ($Instance -eq 'all' -or $MR -le 0) {
        throw 'Manual mode requires -Instance gvs|gvs-infer and -MR <positive integer>.'
    }
    if ($PSBoundParameters.ContainsKey('Repo') -and [string]::IsNullOrWhiteSpace($Repo)) {
        throw '-Repo must not be empty.'
    }
    if ($PSBoundParameters.ContainsKey('CheckpointDir') -and [string]::IsNullOrWhiteSpace($CheckpointDir)) {
        throw '-CheckpointDir must not be empty.'
    }
} else {
    foreach ($manualOption in @('MR', 'Repo', 'DryRun', 'CheckpointDir')) {
        if ($PSBoundParameters.ContainsKey($manualOption)) { throw "-$manualOption requires -Mode manual." }
    }
}
$supervisor = Join-Path $PSScriptRoot 'run-persistent.ps1'
$credentials = Join-Path $env:LOCALAPPDATA 'AI_IR_REPLAY\runtime-env.ps1'
$binary = Join-Path $PSScriptRoot 'bin\gvs-pr-agent.exe'
$requiredFiles = @($credentials, $binary)
if ($Mode -eq 'scheduled') { $requiredFiles += $supervisor }
foreach ($required in $requiredFiles) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file missing: $required"
    }
}
if ($Mode -eq 'manual') {
    $config = "configs/config-$Instance.yaml"
    if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot $config) -PathType Leaf)) { throw "Missing config: $config" }
    if (-not $Repo) {
        $Repo = if ($Instance -eq 'gvs') { 'lizheng/lzvsg/gvs' } else { 'lizheng/lzvsg/gvs_infer' }
    }
    if (-not $CheckpointDir) { $CheckpointDir = "data/checkpoints/$Instance" }
    $runtimeDir = Split-Path $credentials -Parent
    $manualLock = $null
    try {
        $manualLock = [IO.File]::Open((Join-Path $runtimeDir ($Instance + '.supervisor.lock')), 'OpenOrCreate', 'ReadWrite', 'None')
    } catch { throw "Instance $Instance is busy. Stop its supervisor and agent before manual review; do not delete lock files." }
    try {
        . $credentials | Out-Null
        foreach ($required in @('KIMI_API_KEY', 'CODEUP_TOKEN', 'CODEUP_ORG_ID')) {
            if (-not [Environment]::GetEnvironmentVariable($required)) { throw "Missing environment variable: $required" }
        }
        Remove-Item Env:AGENT_REVIEW_DRY_RUN -ErrorAction SilentlyContinue
        $env:ZONEINFO = Join-Path $PSScriptRoot '.gotmp\go1.23.12\go\lib\time\zoneinfo.zip'
        $agentArguments = @('-config', $config, '-repo', $Repo, '-mr', "$MR", '-checkpoint-dir', $CheckpointDir)
        if ($DryRun) { $agentArguments += '-dry-run' }
        Push-Location -LiteralPath $PSScriptRoot
        try {
            & $binary @agentArguments
            $reviewExitCode = $LASTEXITCODE
        } finally { Pop-Location }
    } finally { $manualLock.Dispose() }
    exit $reviewExitCode
}
$instances = if ($Instance -eq 'all') { @('gvs', 'gvs-infer') } else { @($Instance) }
foreach ($name in $instances) {
    $arguments = '-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "{0}" -Instance {1}' -f $supervisor, $name
    Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -ArgumentList $arguments -WorkingDirectory $PSScriptRoot -WindowStyle Hidden
    Write-Host "Start requested: $name (existing instance is preserved)."
}
