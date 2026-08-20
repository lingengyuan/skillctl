# skillctl

跨平台 AI skill 检查与安全更新 CLI，支持 Windows、macOS 和 Linux。

`skillctl` 负责统一扫描多个 Agent 的 skill 目录、识别真实来源、检查本地漂移，并调用 Git、Vercel Skills 或 GitHub CLI 对应的原始更新方式。它不会根据 skill 名称猜测来源，也不会覆盖存在本地修改的副本。

## 安装

Windows PowerShell：

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
```

macOS / Linux：

```sh
curl -fsSLO https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.sh
chmod +x install.sh
./install.sh
```

Go：

```sh
go install github.com/lingengyuan/skillctl@latest
```

## 使用

```sh
skillctl list
skillctl check
skillctl update --dry-run
skillctl update
skillctl doctor
```

筛选或指定 skill：

```sh
skillctl list --host codex
skillctl check obsidian-assistant
skillctl check --json
skillctl check --timeout 30s
skillctl check --path ~/.codex/skills --path ~/.claude/skills
```

当不同目录存在同名 skill 旽，`update` 与 `track` 按名称执行会被拒绝，避免一次修改多个不相关来源；只读的 `check`、`list` 与 `doctor` 仍会展示全部安装。可以通过 `--path`、`--host` 或 `--scope` 缩小范围；确实需要操作全部同名安装时，显式增加 `--all-matches`。

```sh
skillctl update --host codex shared-name
skillctl update --all-matches shared-name
```

`skillctl update --dry-run` 使用与 `check` 相同的只读检查流程，不修改文件。

## 来源跟踪

为复制安装的 skill 登记 Git 来源：

```sh
skillctl track \
  --source https://github.com/example/skills.git \
  --skill-path skills/example-skill \
  example-skill
```

也可以从 Codex 或 Claude Code 的结构化会话记录恢复可信来源：

```sh
skillctl track --from-history
skillctl track --from-history example-skill
```

历史记录只提供候选来源；内容与当前版本或 Git 历史匹配后才会登记。程序不会读取普通对话文本，也不会根据名称猜测仓库。

Git worktree、`skillctl track`、Vercel Skills v3 lock 和 `gh skill` metadata 支持安全更新。Vercel 更新需要 Node.js / `npx` 或全局 `skills` 命令；`gh skill` 更新需要 GitHub CLI。Codex system skills 和通过 Codex curated cache 验证的 skills 只检查、不更新。无法确认来源的目录显示为 `local/untracked`。

## 诊断

```sh
skillctl doctor
skillctl doctor --json
skillctl doctor --fix
```

`doctor` 只检查本地状态，包括：

- 必需目录、可选目录和失效链接；
- 同名但位于不同目录的安装；
- 无效或失效的 `sources.json` 条目；
- Git、GitHub CLI、Vercel Skills CLI 可用性；
- 中断后遗留的 operation lock。

`doctor --fix` 当前只删除已确认失效的来源记录和超过 24 小时的 operation lock，不会修改 skill 内容。

## 配置

首次运行会自动创建 `config.toml`：

- Windows：`%APPDATA%\skillctl\config.toml`
- macOS：`~/Library/Application Support/skillctl/config.toml`
- Linux：`~/.config/skillctl/config.toml`

```toml
network_timeout = "10s"

[[roots]]
path = "~/.codex/skills"
host = "codex"
scope = "user"
required = false
```

缺失的 root 默认跳过。对于必须存在的自定义目录，设置 `required = true`；命令行传入的 `--path` 始终视为必需目录。

`network_timeout` 默认是 `10s`，分别应用于每个远端来源、Provider 更新和 Git 网络操作；也可以用 `--timeout` 临时覆盖。`[[roots]]` 可以重复配置。也可以使用 `--config FILE` 临时指定配置文件。

设置 `XDG_STATE_HOME` 时，Vercel Skills v3 lock 会从 `$XDG_STATE_HOME/skills/.skill-lock.json` 读取；否则使用 `~/.agents/.skill-lock.json`。

## JSON

`check` 和 `update` 的每条报告包含 `schemaVersion`、`state` 与 `reasonCode`。同名安装合并展示时，`installations` 会保留每个真实路径和 Provider 状态。`list` 与 `doctor` 使用带 `schemaVersion` 的 JSON envelope。

## 开发

需要 Go 1.26 和 Git。

```sh
gofmt -w .
go test ./...
go test -race ./...
go test -tags=integration . -run '^TestIntegration'
go vet ./...
```

## License

[MIT](LICENSE)
