# skillctl

`skillctl` 是一个跨平台命令行工具，用于检查和更新电脑中的 AI skills。

它会递归扫描配置的 skill 目录，识别 Git 仓库和已登记来源的复制目录。发现远程更新时，`skillctl check` 会显示结果，`skillctl update` 会执行安全更新。

支持 Windows、macOS 和 Linux。

## 功能

- 递归扫描多个 skill 目录
- 检查 Git 仓库是否落后于远程分支
- 更新可以安全快进的 Git 仓库
- 管理从 Git 仓库复制安装的单个 skill
- 检测本地修改，避免覆盖用户文件
- 支持按名称检查或更新指定 skill
- 使用配置文件管理扫描目录

## 安装

### Windows

在 PowerShell 中运行：

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
Remove-Item .\install.ps1
```

安装程序会把 `skillctl.exe` 安装到：

```text
%LOCALAPPDATA%\Programs\skillctl\bin
```

该目录会自动加入当前用户的 `PATH`。安装完成后，新开一个终端即可使用 `skillctl`。

卸载：

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1 -Uninstall
Remove-Item .\install.ps1
```

### macOS 和 Linux

```sh
curl -fsSLO https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.sh
chmod +x install.sh
./install.sh
rm install.sh
```

程序会安装到 `~/.local/bin/skillctl`。安装脚本会为 Bash、Zsh 或 Fish 配置当前用户的 `PATH`。安装完成后，新开一个终端即可使用。

卸载：

```sh
curl -fsSLO https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.sh
chmod +x install.sh
./install.sh --uninstall
rm install.sh
```

### 使用 Go 安装

需要 Go 1.26 或更高版本：

```sh
go install github.com/lingengyuan/skillctl@latest
```

请确认 Go 的二进制安装目录已经加入 `PATH`。

## 快速使用

检查全部 skills：

```sh
skillctl check
```

更新全部存在远程更新的 skills：

```sh
skillctl update
```

检查指定 skill：

```sh
skillctl check obsidian-assistant
```

更新指定 skill：

```sh
skillctl update obsidian-assistant
```

临时指定扫描目录：

```sh
skillctl check --path ~/.codex/skills --path ~/.claude/skills
```

`--path` 可以重复使用。使用该参数后，本次命令只扫描指定目录。

## 支持的 skill 类型

### Git 仓库

如果 skill 位于 Git 仓库中，`skillctl` 会获取当前分支的远程更新并比较提交记录。

`skillctl update` 只更新满足以下条件的仓库：

- 当前分支已经配置上游分支
- 工作区没有未提交修改
- 本地分支没有领先或分叉
- 更新可以 fast-forward
- 仓库根目录位于允许更新的扫描范围内

不满足条件时，工具会显示原因并跳过，不会强制覆盖或重置仓库。

### 复制安装的 skill

很多 skill 只是从 Git 仓库复制出来的一个目录，本地目录本身没有 `.git`。这类 skill 需要登记一次来源：

```sh
skillctl track \
  --path ~/.codex/skills \
  --source https://github.com/vercel-labs/agent-skills.git \
  --skill-path skills/react-best-practices \
  vercel-react-best-practices
```

参数说明：

- `--source` 指定 Git 仓库地址或本地 Git 仓库路径
- `--skill-path` 指定 skill 在仓库中的相对路径
- `--ref` 可选，指定远程分支、tag 或 commit
- 最后的参数是 `SKILL.md` 中声明的 skill 名称

如果仓库中只有一个同名 skill，可以省略 `--skill-path`：

```sh
skillctl track \
  --path ~/.codex/skills \
  --source https://github.com/example/skills.git \
  example-skill
```

登记来源时，skill 的本地内容必须和来源仓库的当前版本或历史版本完全匹配。后续更新前，工具还会再次检查本地文件。发现用户修改时会跳过更新。

## 配置

首次运行 `skillctl check` 或 `skillctl update` 时，程序会创建默认配置文件。

配置文件位置：

- Windows：`%APPDATA%\skillctl\config.toml`
- macOS：`~/Library/Application Support/skillctl/config.toml`
- Linux：`$XDG_CONFIG_HOME/skillctl/config.toml`，未设置时使用 `~/.config/skillctl/config.toml`

配置示例：

```toml
paths = [
  "~/.agents/skills",
  "~/.config/agents/skills",
  "~/.codex/skills",
  "~/.claude/skills",
  "~/.cursor/skills",
  "~/.copilot/skills",
  "~/.gemini/skills",
  "~/.config/opencode/skills",
]
```

目录会被递归扫描。相对路径以配置文件所在目录为基准。

复制安装的 skill 来源记录保存在同一目录的 `sources.json` 中。Git 来源缓存保存在操作系统的用户缓存目录中。

也可以使用其他配置文件：

```sh
skillctl check --config ./skillctl.toml
```

## 命令

```text
skillctl check [--config FILE] [--path DIR ...] [skill ...]
skillctl update [--config FILE] [--path DIR ...] [skill ...]
skillctl track --source GIT_URL [--skill-path REPO_PATH] [--ref REF] [--path DIR] skill
skillctl --version
skillctl --help
```

选项必须写在 skill 名称之前。

## 输出示例

```text
obsidian-assistant: update available
wechat2md: up to date
local-skill: unmanaged (source unknown)
```

常见跳过原因包括：

- `working tree is dirty`，工作区存在本地修改
- `no upstream`，当前分支没有上游分支
- `ahead`，本地分支领先远程
- `branch has diverged`，本地和远程已经分叉
- `local files were modified`，复制安装的 skill 已被本地修改
- `source unknown`，复制目录尚未登记来源

## 退出码

- `0`，命令执行完成。没有更新或安全跳过也返回 `0`
- `1`，扫描、Git、网络、选择或更新失败
- `2`，命令参数或配置无效

单个 skill 更新失败不会阻止工具继续处理其他 skills。

## 安全原则

- 不要求管理员或 root 权限
- 不执行强制重置
- 不覆盖存在本地修改的 skill
- Git 仓库只允许 fast-forward 更新
- 更新复制目录前验证来源、skill 名称和内容哈希
- 状态保存失败时回滚已替换的 skill 目录
- 不修改 skill 目录中的来源文件

## 本地开发

环境要求：

- Go 1.26 或更高版本
- Git

运行测试：

```sh
go test ./...
```

静态检查：

```sh
go vet ./...
```

Windows 下构建全部发布包：

```powershell
.\scripts\build.ps1
```

构建结果会写入 `dist` 目录，包括 Windows、macOS 和 Linux 的 AMD64、ARM64 压缩包以及 `SHA256SUMS`。

## License

[MIT License](LICENSE)
