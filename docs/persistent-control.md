# 常驻审核的一键启停

保留原名称 `start-persistent.sh`、`start-persistent.ps1` 和原有参数。两个脚本增加相同的动作：`start`、`stop`、`restart`、`status`；默认动作仍是 start，默认实例仍为 all（GVS 与 infer）。进程查询、身份核对、停止顺序和重启流程共用 `internal/lifecycle` 中的 Go 代码；Bash/PowerShell 保留各自的启动及凭据加载方式，无需为 Linux 安装 PowerShell。

## 首次构建管理工具

从项目根目录执行，要求 Go 1.23+。使用独立管理二进制，可以管理旧实例而不覆盖运行中的审核二进制：

```bash
# Linux
go build -o bin/start-persistent ./cmd/agent
```

```powershell
# Windows；Go 不在 PATH 时替换为本机 go.exe 完整路径
go build -o bin/start-persistent.exe ./cmd/agent
```

脚本优先使用 `bin/start-persistent[.exe]`，不存在时回退到 `bin/gvs-pr-agent[.exe]`。回退的审核二进制也必须由支持 `-action` 的新版源码构建，旧版会提示未知参数。管理工具只是同一程序的另一份构建，不会因运行 status/stop 而加载审查配置、读取模型凭据或调用模型。

## Linux

```bash
bash ./start-persistent.sh                               # 启动全部，与以前相同
bash ./start-persistent.sh --action status                 # 检查全部
bash ./start-persistent.sh gvs --action stop               # 停止 GVS
bash ./start-persistent.sh gvs-infer --action restart        # 重启 infer
bash ./start-persistent.sh --action stop                   # 停止全部

# 原有指定 MR 参数完全保留；编号替换为实际 MR
bash ./start-persistent.sh gvs --mode manual --mr 123 --dry-run
bash ./start-persistent.sh gvs --mode manual --mr 123 --repo lizheng/lzvsg/gvs --checkpoint-dir data/checkpoints/gvs --dry-run
```

## Windows PowerShell

```powershell
.\start-persistent.ps1                                    # 启动全部，与以前相同
.\start-persistent.ps1 -Action status                     # 检查全部
.\start-persistent.ps1 -Action stop -Instance gvs          # 停止 GVS
.\start-persistent.ps1 -Action restart -Instance gvs-infer # 重启 infer
.\start-persistent.ps1 -Action stop                       # 停止全部

# 原有指定 MR 参数完全保留；编号替换为实际 MR
.\start-persistent.ps1 -Mode manual -Instance gvs -MR 123 -DryRun
.\start-persistent.ps1 -Mode manual -Instance gvs -MR 123 -Repo lizheng/lzvsg/gvs -CheckpointDir data/checkpoints/gvs -DryRun
```

`Action start` 可以显式填写。stop/status/restart 只接受实例选择，不能与 Mode、MR、Repo、DryRun 或 CheckpointDir 混用。此处 MR 指 Codeup 合并请求，未增加 GitHub 单 PR 指定入口。手动 MR 审核仍需先停止对应定时实例；dry-run 仍可能收费、写知识和推进成功基线。

## 状态与停止行为

- status 输出 `supervisor=<PID>` 和 `agent=<PID>`，或 `stopped`。只有 supervisor 表示正在重启等待或审核程序未能启动；只有 agent 可能是手动审核或遗留子进程。status 是进程检查，不代表平台、模型或最近一次审核成功；查询成功即返回 0，包括 stopped 状态。
- stop 先结束守护程序，再扫描并结束对应审核进程（含该实例的手动 MR 审核），防止被守护程序拉起。已停止时重复执行成功。
- Linux 发送 SIGTERM；Windows 使用进程终止，正在审核的工作可能中断。未完成的审查可能需要重新调用模型；已有发布或知识写入不会回滚。
- 以当前管理工具所在项目的脚本路径、审核可执行文件路径、配置实例和进程创建标识核对目标，不直接信任 PID 文件，不删除锁文件或基线。管理工具应放在当前项目的 bin 目录，并由启动实例的同一账号运行。
- 停止后等待最多 15 秒确认退出；超时返回非零，restart 不继续启动。restart 的启动阶段仍需要原有凭据、二进制和配置；管理命令应串行执行。
- 状态、基线、检查点和日志保留。日志仍在 `data/runtime`。Windows 的 GVS 管理端口沿用 8081，Linux 沿用 8083，infer 均沿用 8082。

## 本地验证

```bash
go test -count=1 ./internal/lifecycle ./cmd/agent
bash tools/test-start-modes.sh
# Linux 环境额外执行真实 flock/守护重启测试
bash tools/test-persistent-linux.sh
```

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tools/test-start-modes.ps1
```

测试使用临时目录、模拟凭据和假审核进程，不访问模型或代码托管平台。`internal/lifecycle` 包包含真实平台脚本的启动、防重复、停止和重启测试；Git Bash 的路由测试不替代 Linux 原生生命周期测试。
