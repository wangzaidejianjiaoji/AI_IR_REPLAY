<#
.SYNOPSIS
后台启动 AI_IR_REPLAY 的 GVS / infer 常驻审核实例。

.DESCRIPTION
在 PowerShell 中运行，默认同时启动两个实例；也可通过 -Instance 单独选择。
守护进程自动加载本机凭据，审核进程退出后等待 15 秒重启。
重复启动同一实例会被文件锁拦截，不会再创建一套审核服务。
关闭启动终端不影响后台运行；注销或关机期间不运行。
脚本按现有配置发布 MR 评论，模型调用可能收费。

运行前需已有 bin\gvs-pr-agent.exe，以及本机凭据脚本：
%LOCALAPPDATA%\AI_IR_REPLAY\runtime-env.ps1（凭据不放入项目）。

.PARAMETER Instance
all：同时启动 GVS 和 infer（默认）；gvs：仅 GVS；gvs-infer：仅 infer。

.EXAMPLE
Set-Location 'D:\DeepSeekHarness\AI_IR_REPLAY'
.\start-persistent.ps1

.EXAMPLE
.\start-persistent.ps1 -Instance gvs

.EXAMPLE
.\start-persistent.ps1 -Instance gvs-infer

.EXAMPLE
# 若执行策略阻止脚本，仅为本次启动指定策略：
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\start-persistent.ps1

.NOTES
查看帮助：Get-Help .\start-persistent.ps1 -Full
日志：项目 data\runtime\<实例>-<启动日期>.log；日志为 Unicode 格式。
健康检查：http://127.0.0.1:8081/health（GVS）、:8082/health（infer）。
健康接口只表示服务存活，审核及评论是否成功需查看日志。
停止时先结束对应 run-persistent.ps1 守护进程，再结束其审核子进程。
仅停止审核子进程会自动重启；下次用户登录仍会按已有登录启动项启动。
#>
param(
    [ValidateSet('all', 'gvs', 'gvs-infer')]
    [string]$Instance = 'all'
)

$ErrorActionPreference = 'Stop'
$supervisor = Join-Path $PSScriptRoot 'run-persistent.ps1'
$credentials = Join-Path $env:LOCALAPPDATA 'AI_IR_REPLAY\runtime-env.ps1'
$binary = Join-Path $PSScriptRoot 'bin\gvs-pr-agent.exe'
foreach ($required in @($supervisor, $credentials, $binary)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required file missing: $required"
    }
}
$instances = if ($Instance -eq 'all') { @('gvs', 'gvs-infer') } else { @($Instance) }
foreach ($name in $instances) {
    $arguments = '-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "{0}" -Instance {1}' -f $supervisor, $name
    Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -ArgumentList $arguments -WorkingDirectory $PSScriptRoot -WindowStyle Hidden
    Write-Host "Start requested: $name (existing instance is preserved)."
}
