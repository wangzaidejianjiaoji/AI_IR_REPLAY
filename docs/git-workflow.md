# Git 提交与拉取

仓库：https://github.com/wangzaidejianjiaoji/AI_IR_REPLAY

本地与远程使用唯一的 `main` 分支；本地 `main` 跟踪 `origin/main`。

开始工作前拉取：

```powershell
git pull
```

完成修改后提交并上传：

```powershell
git status
git add .
git commit -m "描述本次修改"
git push
```

首次在其他电脑获取代码：

```powershell
git clone https://github.com/wangzaidejianjiaoji/AI_IR_REPLAY.git
cd AI_IR_REPLAY
```

源码、配置模板、测试、文档和 `outputs/` 中的最终演示文件纳入版本控制。凭据文件 `env.md`、`.env`，以及 `data/`、`.gotmp/`、`gotmp/`、`.ppt-rebuild/` 等运行状态和临时文件由 `.gitignore` 排除，需要在新机器上单独配置凭据。

此电脑的仓库使用系统代理 `http://127.0.0.1:7897` 访问 GitHub。代理地址仅保存在本地 Git 配置中，不随克隆传播。代理关闭或端口变更时，可调整或移除：

```powershell
git config --local http.proxy http://127.0.0.1:7897
git config --local --unset http.proxy
```

此电脑配置 `pull.ff=only`，遇到本地与远程历史分叉时会停止拉取。先提交本地修改，然后运行 `git fetch`、`git rebase origin/main`，解决冲突后再推送。
