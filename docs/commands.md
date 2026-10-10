# 命令参考

参数可放在位置参数前后；用 `--` 结束选项解析。名称可以替换为 `list --json-version 2` 中的 `id` 或 `contentId`。所有写操作支持 `--dry-run`；查询命令中 `check --dry-run` 可避免保存来源证据。

每个命令都有独立帮助，列出该命令的用途、可选项和示例：

```sh
skillctl COMMAND --help
skillctl help COMMAND
```

安装目标的选择顺序：`--host` 优先；未提供时使用检测到的 Agent；一个也没检测到时，交互式终端列出全部可安装目标供选择，非交互环境直接失败并列出可用名称。`config hosts` 列出全部可安装 Agent，与 `--host` 接受的范围一致。

退出码：`list` 是查询命令，报告成功即返回 0，即使存在损坏或无效条目；加 `--strict` 可让这类条目导致失败，供 CI 使用。`check`、`update` 与 `doctor` 在仍有未解决问题时返回 1。

`--quiet` 隐藏 `doctor` 的 INFO 级输出；`doctor` 默认把大量「可选目录不存在」合并为一行汇总，逐条明细保留在 `--json` 中。

`doctor --fix` 会删除指向已删除内容的悬空链接，前提是目标父目录仍然存在。父目录也不存在时（例如外置卷未挂载），链接会被保留并给出警告，避免误删真实配置。该修复记录在操作历史中，可用 `rollback` 撤销。

`config gc` 回收 store 中不再被绑定、lock 或历史引用的版本，同时清理来源 clone 的临时目录。clone 采用「临时目录 + rename 发布」，因此进程被中断时清理不会执行、目录会遗留；gc 只回收闲置超过一天的临时目录，正在进行的 clone 不受影响。

`doctor --fix --reinstall` 在此之上尝试重新安装内容已消失的 Skill。来源取自可信记录：`sources.json` 的 tracked source、Vercel lock 中的仓库与 `skillPath`、以及 inventory 中已记录的包来源。行为边界：

- 已在位的 Skill 不会被覆盖，避免覆盖用户改动。
- 多个来源互相冲突时只报告，不猜测。
- 完全没有来源记录时明确报告 `reinstall_source_missing`，不会静默删除。
- 每个 Skill 独立作为一个操作执行，部分失败不影响已成功的部分。
- 与 `--dry-run` 组合时不写入任何内容。

## 安装选择与确认

来源可以内联指定 Skill：`owner/repo@skill` 等价于 `--skill skill`。`splitInlineSkill` 只拆分手写简写和普通路径，`git@host:path` 与含凭据的 URL 保持原样。

省略 `--skill` 时安装来源中的全部有效 Skill。数量达到 10 个及以上且未显式选择时会先确认；`--yes` 跳过。非交互环境或 JSON 输出下无法询问，此时直接失败并给出 `--skill`/`--yes` 提示，避免静默批量安装。

同名 Skill 冲突时错误会列出全部候选路径，`--skill-path` 可直接取用。

`search` 的结果末尾给出可直接执行的 `install SOURCE --skill NAME` 命令。

安装输出的状态标签会把「尚未检查来源」显示为 `installed (source not checked)`，`--json` 中的 `state`/`reasonCode` 保持原值不变。

配置迁移：`config.toml` 若仍是早期版本生成的默认内容（Agent 列表较旧），会按当前默认配置读取，使新增 Agent 立即可见；用户自定义过的配置不做改动。文件本身不被重写。

## 查看与检查

| 命令 | 用法与结果 |
|---|---|
| `list [SKILL...]` | 本地清单；`--host`、`--scope`、`--project`、`--path` 可筛选。共享内容保留全部 bindings。列出全部消费该内容的 Agent，路径优先显示 Agent 实际读取的位置 |
| `show [SKILL...]` | 来源、版本、本地漂移、绑定与 capabilities；同名副本分别列出 |
| `check [SKILL...]` | 按来源检查；自动恢复可信安装记录，`--no-history` 可关闭。`--offline` 只使用本地证据 |
| `diff [SKILL...]` | 比较安装目录与来源内容，JSON 的 `result` 包含每项 diff。没有可比较来源时给出诊断 |
| `doctor [--fix]` | 检查目录、异常项、来源记录、工具和中断操作；普通诊断只读。`--fix` 可修复指向已删除内容的悬空链接，以及过期的来源记录。`--reinstall` 在来源有记录时重新安装这些 Skill，而非只清理链接 |
| `search QUERY` | 查询 skills.sh 官方 CLI 使用的 `/api/search` 接口 |
| `search [QUERY] --source SOURCE` | 在指定本地/Git/well-known/下载来源中查找；`--offline` 适用于本地或已有 Git 缓存 |
| `search QUERY --offline` | 查询已发现的本地资产，不访问公共搜索服务 |

搜索结果只是发现入口，不表示来源已验证或质量已评估。接口契约依据 [Vercel Skills 的实现](https://github.com/vercel-labs/skills/blob/main/src/find.ts)。

## 安装与使用关系

```sh
skillctl install owner/repository --skill example --host codex
skillctl install https://github.com/owner/repository.git --ref v1.0 --skill-path skills/example --host claude
skillctl install git+ssh://git@example.com/team/skills.git --skill example --host claude
skillctl install ./example --host codex --host claude --copy
skillctl install ./skills.zip --skill example --project ./project --host claude
skillctl install https://example.com/skills --skill example --host claude
```

支持本地目录、Git URL、`owner/repo`、GitHub tree URL、ZIP、tar.gz、独立 SKILL.md 与 well-known v0.2 来源。`--skill` 可重复；没有选择时安装来源中发现的全部有效 skills。`--skill-path` 选择来源内相对目录。`git+` 可消除本地 Git 仓库路径或 HTTP 来源的类型歧义。归档拒绝目录穿越、链接和不支持的条目，并限制文件数及展开大小。

默认安装到检测到的 Agent；没有检测到时要求 `--host`。同一身份重复安装不会重写已一致的内容。名称相同但来源/ref 不同的副本需要不同目标，或通过 profile 在该环境独占的目标间切换。

```sh
skillctl enable example --host claude
skillctl disable example --host claude
skillctl remove example --host claude
skillctl config gc --dry-run
```

`enable` 恢复已停用绑定，也可为 Skill 增加 Agent。原有本地内容保留原位置；停用实际目录时保存快照。`remove` 只移除选择的绑定，共享存储由 `config gc` 管理。当前 GC 保留历史引用，不自动缩短恢复期限。

## 更新、固定与历史

```sh
skillctl update example --dry-run
skillctl update example
skillctl pin example
skillctl unpin example
skillctl history
skillctl history OPERATION_ID --json-version 2
skillctl rollback OPERATION_ID --dry-run
skillctl rollback OPERATION_ID
```

`pin` 固定当前安装版本。要安装其他 ref，使用 `install --ref REF` 创建独立身份；不会把 `pin --ref` 当成隐式更新。`rollback latest` 选择最近已提交操作，包括此前的回退操作。

既有 Git 仓库以仓库为更新单元，可能同时改变其中的其他 Skill 和文件；历史记录列出范围。仓库包含本地修改、分叉、领先提交或没有 upstream 时不会拉取覆盖；仓库根目录不在允许扫描范围内时也会跳过。

插件操作必须显式选择并附带 `--package`：

```sh
skillctl disable plugin-skill --host claude --package --dry-run
skillctl disable plugin-skill --host claude --package
skillctl update plugin-skill --host claude --package
skillctl remove plugin-skill --host codex --package
```

操作作用于整个插件及其组件。不同宿主操作能力以 `show` 返回的 capabilities 为准。当前 Codex 原生接口没有独立更新/启停命令；Claude 的移除使用 `--keep-data`。程序不会添加用于批准插件任意命令的自动确认选项。宿主要求的额外操作或认证会作为具体错误返回。

## 环境与 profile

```sh
skillctl sync --file ./skillctl.toml
skillctl sync --file ./skillctl.toml --frozen --offline
skillctl update --file ./skillctl.toml
skillctl update example --file ./skillctl.toml
skillctl profile list --file ./skillctl.toml
skillctl profile use default --file ./skillctl.toml
skillctl export example --output ./shared/skillctl.toml
skillctl import ./shared/skillctl.toml --frozen
```

manifest 使用 `version = 1` 和 `[[skills]]`：

| 字段 | 规则 |
|---|---|
| `id` | 声明内唯一，省略时采用 `name` |
| `name` | Skill 文档中的合法名称 |
| `source` | 无凭据远端来源，或相对 manifest 目录的本地来源 |
| `kind` | 可选的 `git`、`local`、`archive`、`well-known`；省略时解析来源 |
| `skill_path` | 可选，来源内的相对目录 |
| `ref` | 可选，跟踪分支、tag 或 commit |
| `hosts` | 必需，明确目标 Agent |
| `scope` | `project`（默认）或 `user` |
| `mode` | `link`（默认）或 `copy`，链接不可用时降级为复制 |

`[profiles.NAME]` 的 `skills` 数组引用声明 ID。省略 profile 时采用本机激活的 profile；没有激活记录时使用全部声明。`--profile all` 或 `profile use all` 选择全部声明。空数组允许停用该 profile 原来独占的全部使用关系。

lock 固定实际 Git 提交或制品版本及内容摘要。已锁定条目不会因普通 sync 而跟随分支推进。声明变更需要普通 sync 或 update；frozen 会拒绝。离线恢复必须有可验证的精确内容，缓存中“有个同名 Skill”不满足条件。

## 辅助与兼容

```sh
skillctl config show
skillctl config path
skillctl config hosts
skillctl config init
skillctl completion bash
skillctl completion zsh
skillctl completion fish
skillctl completion powershell
skillctl version
```

本机发现配置使用 `--config`；项目环境使用 `--file`。两者不是同一个配置文件。配置初始化是显式命令，普通查询不会写入默认配置。

`--timeout 30s` 控制每个来源或宿主操作的时间；`--json` 保留既有机器接口。v2 使用 `--json-version 2`，其中 `items[].bindings` 是 Agent 使用状态的依据，`capabilities` 说明支持的动作及其事务单元。来源检查成功并不意味着所有绑定都已启用。
