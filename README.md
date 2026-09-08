# skillctl

面向个人和小团队的 Skill 生命周期管理 CLI。统一发现多个 Agent 的安装、解释来源和本地修改，完成安装、分发、更新、停用、移除、固定版本和恢复。现有安装保留位置与管理者；新安装进入共享存储，再以链接或副本提供给 Agent。

本文档对应 `v0.0.5`。安装脚本获取 GitHub 最新正式发布版，也可从源码构建；版本变化见 [GitHub Releases](https://github.com/lingengyuan/skillctl/releases)。

## 从源码运行

需要 Go 1.27；Git 来源和既有 Git 安装的检查、更新还需要 Git。

```sh
go build -o ./dist/skillctl .
./dist/skillctl version
./dist/skillctl --help
```

Windows 使用 `go build -o ./dist/skillctl.exe .`。

已发布版本的安装方式保持兼容：

```sh
curl -fsSLO https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.sh
chmod +x install.sh
./install.sh
```

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/lingengyuan/skillctl/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
```

也可以运行 `go install github.com/lingengyuan/skillctl@latest`。重新执行对应安装命令可更新程序；脚本支持 `./install.sh --version v0.0.5` 或 `.\install.ps1 -Version v0.0.5` 安装指定发布版本。

## 日常使用

以下命令假定当前构建的 `skillctl` 已加入 PATH。`./my-skill` 是包含有效 `SKILL.md` 的本地目录。

```sh
skillctl install ./my-skill --host codex --host claude
skillctl list --json-version 2
skillctl show my-skill
skillctl check
skillctl diff my-skill
skillctl update my-skill --dry-run
skillctl update my-skill
skillctl disable my-skill --host claude
skillctl enable my-skill --host claude
skillctl pin my-skill
skillctl unpin my-skill
skillctl history
skillctl rollback latest
skillctl remove my-skill --host claude
```

参数可以放在名称前后。`--host` / `--agent` / `-a` 可重复；`--scope user` 或 `--global` 选择全局使用关系，`--project PATH` 选择项目。新安装默认使用全局作用域；指定 `--project` 时默认安装到项目。

同名不同来源会保留为独立资产。写操作遇到歧义时，使用 `id`、`contentId`、`--host`、`--scope`、`--path` 缩小范围，或用 `--all-matches` 明确选中全部副本。身份取决于来源、来源内路径及跟踪 ref，名称只用于展示；不同 ref 可作为独立版本安装到不同目标。

新安装的内容位于 Agent 扫描目录外。支持目录链接时创建链接，否则使用复制；`--copy` 可以显式选择复制。更新共享副本会列出受影响的 Agent，并更新所有活动使用关系。物理路径同时被多个 Agent 扫描时，不能只停用其中一个消费者，命令会解释限制。

`remove` 移除使用关系，保留共享内容与历史。`config gc --dry-run` 可以预览可清理的孤立版本；仍被安装或历史引用的版本会保留。

## 项目与可复现环境

在项目中维护 `skillctl.toml`，将它与 `skillctl.lock` 一起提交到 Git。以下例子使用项目内的 `skills/example` 目录：

```toml
version = 1

[[skills]]
id = "example"
name = "example"
source = "./skills/example"
kind = "local"
skill_path = "."
hosts = ["claude"]
scope = "project"
mode = "link"

[profiles.default]
skills = ["example"]

[profiles.empty]
skills = []
```

远端来源可用 `source = "https://github.com/example/skills.git"`、`kind = "git"`、`skill_path = "skills/example"` 和可选的 `ref = "main"`。锁文件记录实际提交或制品版本及内容摘要。

```sh
skillctl sync
skillctl sync --frozen
skillctl sync --frozen --offline
skillctl update --file ./skillctl.toml
skillctl profile list
skillctl profile use empty
skillctl profile use default
```

`sync` 复用与声明匹配的锁定版本；只有新增或改变声明时才解析新版本。`update --file FILE` 显式推进版本，也可附加声明 ID 或名称只推进对应条目。`--frozen` 禁止重新解析版本；缺锁、声明变更、制品缺失或摘要不符都会失败。离线同步使用已验证的共享存储、本地制品或缓存的精确 Git 提交。

profile 的激活状态保存在本机。切换 profile 只释放该环境声明的使用关系，保留手动安装和其他环境仍在使用的内容。两个 profile 可在同一目标间切换独占的不同来源；与其他安装共享的目标会阻止替换。

导出已安装内容供其他环境导入：

```sh
skillctl export example --output ./shared/skillctl.toml
skillctl import ./shared/skillctl.toml --frozen
```

导出目录包含 manifest、lock，以及必要的 `skillctl-artifacts/`。未知来源、本地内容或无法证明精确远端版本的既有副本会导出为内容快照。分享时保留整个目录。宿主插件不转换为独立 Skill 导出，需要缩小选择范围并由宿主安装插件。

共享配置禁止本机绝对路径和带凭据的 URL。私有 Git 源复用现有 Git/SSH 认证，凭据不会写入 manifest、lock 或来源说明。环境同步命令不会自动提交或推送 Git。

## 发现、来源与宿主边界

默认发现用户目录与当前项目，包括当前工作目录至项目根之间的 Skill 目录；支持 Codex、Claude Code、Cursor、Copilot、Gemini、OpenCode，以及原有目录和自定义 roots。当前项目从最近的 `.git` 或 `skillctl.toml` 定位，也可用 `--project` 指定。 Codex 的默认新安装目标为 `~/.agents/skills`，旧的 `.codex/skills` 继续发现。`config hosts` 显示各 Agent 的默认目标路径。

清单保留物理内容、内容身份和每个 Agent 的使用关系。无效文档、不可读目录、断链和损坏的绑定会进入结构化诊断。列表展示安装事实；`current` 表示经过来源检查，宿主管理项使用 `managed`，不会因“由宿主管理”而被标记为最新。

`check` 和 `update` 先检查已知来源，再从 Codex/Claude 的安装记录恢复未知来源。自动恢复要求可识别的实际工具调用、成功完成的结果，以及匹配的 Skill 名称、宿主或目标目录；未完成、失败和无法关联目标的记录不会触发来源请求。候选还必须通过当前内容或 Git 历史比对，且证据不能冲突。`--no-history` 关闭自动恢复；显式入口继续可用：

```sh
skillctl track --from-history
skillctl track --source https://github.com/example/skills.git --skill-path skills/example example
```

| 现有安装管理者 | 检查与更新方式 |
|---|---|
| Git 仓库 | 检查本地修改与 upstream，只允许 fast-forward；事务和历史覆盖实际仓库及必要 Git 元数据 |
| `skillctl track` 复制安装 | 验证原内容基线，以目录和来源记录组成事务 |
| Vercel Skills v3 lock | 保留 lock 与原位置，通过 `skills` / `npx skills` 更新并验证结果 |
| Vercel well-known v0.2 | 验证索引、制品 SHA-256 和本地基线，按来源批量调用原 CLI |
| `gh skill` metadata | 保留 GitHub CLI 管理权，校验 tree、内容和元数据；比较时忽略原 CLI 注入的来源字段 |
| Codex / Claude 插件 | 从有效安装记录读取具体版本和启用状态，使用宿主 CLI 操作整个包 |
| Codex system / 其他宿主管理项 | 展示归属与限制，不用目录覆盖替代宿主操作 |
| 无来源的本地目录 | 可保存、分发、启停和恢复；没有可证明的远端更新来源 |

宿主有效安装记录优先确定插件归属和版本。名称大小写等可移植性问题单独告警；缺失必填字段、损坏的 YAML 和不可读内容仍报告错误，新安装仍执行严格校验。指定单项检查时，无关目录的诊断保持可见，但不会让所选项目失败。

Codex 插件列表使用最近一次 `check` 验证的原生清单；没有缓存时给出诊断，不把插件 cache 中的所有旧目录当作已安装。Claude 读取 `installed_plugins.json` v2 和作用域内的启用配置。插件内容基线用于发现记录之后的本地修改，并不等同于发行方签名。

插件操作须带 `--package`，计划会列出整个包及组件。Claude 支持原生 update/enable/disable/uninstall，移除时保留插件数据。当前已核对的 Codex CLI 支持 remove，没有独立 update/enable/disable 接口；这些操作会报告不支持。插件版本回退取决于宿主是否能恢复精确版本；当前仅对启停提供原生逆操作，不声称能回退插件更新或移除。

## 本机配置与恢复

`list`、普通 `doctor` 和预览不会自动创建或迁移配置。需要编辑默认配置时显式初始化：

```sh
skillctl config path
skillctl config show
skillctl config init
```

默认配置目录为 Windows `%APPDATA%\skillctl`、macOS `~/Library/Application Support/skillctl`、Linux `~/.config/skillctl`。`SKILLCTL_HOME` 可隔离本机配置、inventory、共享存储与历史；该目录必须位于 Agent skill 扫描目录外。发现还尊重 `CODEX_HOME`、`CLAUDE_CONFIG_DIR` 和 OpenCode 的 `XDG_CONFIG_HOME`。

```toml
network_timeout = "10s"

[[roots]]
path = "~/.codex/skills"
host = "codex"
scope = "user"
required = false

[[roots]]
path = "/work/project/.claude/skills"
host = "claude"
scope = "project"
project = "/work/project"
```

本机 discovery 配置允许绝对路径，项目环境 manifest 禁止绝对来源路径，两者用途不同。显式 `--config FILE` 保持该文件的发现范围；需要额外扫描项目时同时指定 `--project`。`--path` 完全替换发现 roots，且视为必需目录。Vercel lock 使用 `$XDG_STATE_HOME/skills/.skill-lock.json`，未设置时读取 `~/.agents/.skill-lock.json`。

`check` 可保存验证过的来源、基线和宿主清单。`--dry-run` 不保存这些状态，也不修改安装或 Git 工作仓库；远端读取可以使用下载缓存，Git 仓库预览使用临时 bare clone。`list` 和普通 `doctor` 不进行网络检查。

`check` 不恢复安装事务，也不修改已安装内容或 Git 工作区。它在短锁内读取本机状态，在锁外获取来源；需要保存已验证来源或宿主观察时，再取得短锁，核对最新状态和内容后合并。未完成事务影响的项目标记为待恢复，不用部分写入的内容建立新基线。

`--timeout` 限制单次来源操作，自动历史恢复的总预算默认使用同一时长，可用 `--recovery-timeout` 单独设置；`--command-timeout` 可限制整条命令。相同来源和 ref 在命令内共享获取结果，Git 内容读取固定到提交，不切换共享缓存工作区。

安装历史索引仅保存解析出的候选元数据；文件身份、长度或修改时间/变更时间变化会触发重读。无法可靠识别文件变更的平台回退到完整解析。索引命中仍需验证来源内容。

写操作先取得内核文件锁，再读取状态；另一写进程会收到 busy 错误。文件事务执行前校验原状态，保存 before/after 镜像，逐步执行并验证。进程退出会释放锁；下次写操作恢复可证明安全的中断事务。外部 CLI 中断后如果不能证明最终状态，会保留恢复证据并阻止后续写入。

```sh
skillctl doctor
skillctl doctor --fix
skillctl history --json
skillctl rollback OPERATION_ID --dry-run
skillctl rollback OPERATION_ID
```

`doctor --fix` 恢复安全的中断操作，并删除已确认失效的来源条目。回退只接受与记录的执行后状态一致的现场；出现后续修改或损坏的备份时拒绝覆盖。事务尚未完成且外部写入无法确认时，需要先检查 `history/<ID>/` 中的记录和镜像；程序会保留证据，不根据时间强删锁或猜测文件归属。历史可能包含完整目录或仓库，因此会占用磁盘空间。

## 机器接口与命令参考

旧 `--json` 保留 `check/update` 数组和 `list/doctor` envelope。新增 `--json-version 2` 统一输出 `schemaVersion`、`command`、`items`、`diagnostics`，以及适用的 `plan`、`operation`、`operations`、`result`。v2 按内容副本输出，不合并同名不同来源；每项包含身份、来源、管理者、使用关系和操作能力。

检查结果保留独立事实：`local.status` 描述本地内容，`upstream.status` 描述远端检查结果，`update.supported/eligible` 描述支持能力和当前执行资格，`validation` 描述文档校验。旧 `state`、JSON 外层结构和身份算法保留；`contentId` 标识物理安装位置，内容摘要仍使用 `digest`。

摘要分别统计名称和物理安装数量，本地修改与远端更新可同时计数；`--verbose` 展开安装明细。更新前先完成来源检查和计划；各 Provider 复用原有事务与写前校验。独立来源可部分成功，`operations` 返回实际事务，失败保留在项目与诊断中，不宣称跨 Provider 全局原子性。

退出码：`0` 表示查询或执行成功（可包含待更新、固定或明确跳过的项目），`1` 表示诊断/来源/执行失败，`2` 表示参数或配置错误。应结合 `state`、`reasonCode` 与 `diagnostics` 判断业务状态。

全部命令与选项见 [命令参考](docs/commands.md)，模块与事务边界见 [实现说明](docs/architecture.md)。

## 代码结构

根目录的 `main.go` 负责程序入口；`internal/app` 负责命令和生命周期编排。参数解析、Git 缓存、安装历史、Skill 文档、归档与文件操作分别位于 `internal/cli`、`gitstore`、`installhistory`、`skilldoc`、`archive`、`fsutil`。职责与依赖说明见 [实现说明](docs/architecture.md)。

## 开发与验证

```sh
gofmt -w .
go test ./...
go test -race ./...
go test -tags=integration ./... -run '^TestIntegration'
go vet ./...
```

测试还覆盖来源缓存锁的进程退出释放、旧锁迁移、活动锁保护和共享来源只同步一次。既有测试覆盖多 Agent 生命周期、复制保护、同名不同源、共享路径限制、profile 切换、两环境冻结复现、制品篡改、原生插件记录与整包范围、进程中断和恢复失败。Git 集成测试使用本地临时仓库；宿主插件与外部 Provider 更新采用可控 fixture，不会更新开发者的真实插件。

历史性能实验及 `scripts/ablation.py` 对应 `v0.0.4` 源码，需在该 tag 上复现。当前优化分别对比关闭目录哈希复用、安装历史索引、命令内来源共享、来源目录索引和路径名称预比较；实验脚本、原始数据与报告保留在本地，不纳入项目发布。

CI 在 Linux、macOS 和 Windows 运行测试，Windows 另有 junction 集成测试；本机交叉编译不能替代 Windows 原生执行。源码继续使用 Go 单进程、TOML/JSON 文件与原有依赖，无数据库或后台服务。

## License

[MIT](LICENSE)
