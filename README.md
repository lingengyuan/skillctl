# skillctl

跨平台 AI skill 更新检查工具，支持 Windows、macOS 和 Linux。

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
skillctl check
skillctl update
skillctl check obsidian-assistant
skillctl check --json
skillctl check --timeout 30s
skillctl check --path ~/.codex/skills --path ~/.claude/skills
```

为复制安装的 skill 登记 Git 来源：

```sh
skillctl track \
  --source https://github.com/example/skills.git \
  --skill-path skills/example-skill \
  example-skill
```

Git worktree、`skillctl track`、Vercel Skills v3 lock 和 `gh skill` metadata 支持安全更新。Vercel 更新需要 Node.js / `npx` 或全局 `skills` 命令；`gh skill` 更新需要 GitHub CLI。Codex system skills 只检查、不更新。无法确认来源的目录显示为 `local/untracked`。

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
```

`network_timeout` 默认是 `10s`，也可以用 `--timeout` 临时覆盖。`[[roots]]` 可以重复配置。也可以使用 `--config FILE` 临时指定配置文件。

## 开发

需要 Go 1.26 和 Git。

```sh
go test ./...
go test -tags=integration . -run '^TestIntegration'
go vet ./...
```

## License

[MIT](LICENSE)
