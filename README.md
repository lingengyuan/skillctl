# skillctl

管理 Codex、Claude Code、Cursor、Copilot、Gemini 和 OpenCode 的 Skills，统一查看安装、追踪来源、检查更新、分发和恢复。

## 安装

macOS / Linux：

```sh
curl -fsSLO https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.sh
chmod +x install.sh
./install.sh
```

Windows PowerShell：

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
```

脚本安装最新正式版，重新运行即可升级；指定版本使用 `--version v0.0.5`（PowerShell：`-Version v0.0.5`）。安装包见 [Releases](https://github.com/lingengyuan/skillctl/releases)。Git 来源需要 Git。

## 使用

```sh
skillctl list
skillctl check
skillctl install ./my-skill --host codex --host claude
skillctl diff my-skill
skillctl update my-skill --dry-run
skillctl update my-skill
```

本地目录须包含有效的 `SKILL.md`。安装默认全局生效，`--project PATH` 指定项目；同名安装用 ID、`--host` 或 `--path` 区分。

| 操作 | 命令 |
|---|---|
| 查看详情 | `skillctl show NAME` |
| 停用 / 启用 | `skillctl disable NAME` / `enable NAME` |
| 固定 / 解锁版本 | `skillctl pin NAME` / `unpin NAME` |
| 移除使用关系 | `skillctl remove NAME` |
| 查看历史 / 回退 | `skillctl history` / `skillctl rollback latest` |
| 诊断 / 修复 | `skillctl doctor` / `skillctl doctor --fix` |

现有安装沿用原管理方式；宿主插件通过 `--package` 操作整个包。远端更新要求来源可验证。新安装使用共享存储，更新影响所有活动使用关系，同一物理路径不能只对其中一个 Agent 停用。

`check` 可保存验证过的来源和基线；`--dry-run` 不修改安装或保存这些状态，远端读取可使用缓存。更新允许独立来源部分成功。回退要求现场与历史记录一致；插件更新或移除不保证可回退。`remove` 保留共享内容与历史。

## 配置与项目同步

`skillctl config init` 初始化本机配置，`config path` 查看位置。`SKILLCTL_HOME` 可隔离配置、存储和历史，须位于 Agent 扫描目录外。

项目用 `skillctl.toml` 声明 Skills，与生成的 `skillctl.lock` 一起提交：

```sh
skillctl sync                          # 按声明与锁文件同步
skillctl sync --frozen                 # 严格使用锁定版本
skillctl update --file ./skillctl.toml # 推进版本
```

`--frozen` 在缺锁、声明变化、制品缺失或摘要不符时失败。配置示例、离线同步、导入导出与自动化接口见 [命令参考](docs/commands.md)。

## 开发

需要 Go 1.27：

```sh
go build -o ./dist/skillctl .  # Windows 输出 ./dist/skillctl.exe
go test ./...
go test -race ./...
go test -tags=integration ./... -run '^TestIntegration'
go vet ./...
```

[实现说明](docs/architecture.md) · [MIT License](LICENSE)
