# skillctl 来源识别与安全更新：同类产品分析和优化方案

> 调研日期：2026-08-14
>
> 范围：只讨论来源识别、所有权、`check`、`update` 和安全更新。覆盖 Vercel Skills CLI、GitHub CLI `gh skill`、OpenAI Codex skills/plugins、Anthropic Claude Code plugins/marketplaces。
> 边界：本文先记录同类产品的事实，再提出可供评审的优化候选。它不是最终架构决定。

## 一、同类产品事实与证据

### 1. Agent Skills 规范只定义内容格式，不定义分发来源

Agent Skills 规范定义了 skill 目录、`SKILL.md`、必填的 `name` 和 `description`、可选的 `metadata` 等内容。`metadata` 明确是由客户端使用的任意键值，没有标准化 `source`、安装器、锁文件、版本解析、更新执行者或本地修改策略。[Agent Skills specification](https://agentskills.io/specification)

因此，下面几种元数据都符合各自产品的实现，但彼此不兼容：

- Vercel Skills CLI 使用全局 `.skill-lock.json` 和项目 `skills-lock.json`。
- `gh skill` 把 `github-*` 或 `local-path` 写进已安装的 `SKILL.md` frontmatter。
- Codex standalone skill 主要依靠发现路径和宿主范围；plugin 依靠 marketplace、plugin manifest 和版本缓存。
- Claude Code plugin 同时依靠 marketplace registration、marketplace entry、plugin manifest、scope settings 和版本缓存。

结论是：`skillctl` 不能把“符合 Agent Skills 规范”当成“来源已知”，也不能假设所有安装器会产生同一种 lock 文件。这是由规范范围和各产品官方实现共同得出的结论。

### 2. 横向对照

| 产品 | 官方支持的来源 | 权威持久化元数据 | 版本或完整性 | 更新执行者 | 本地修改和冲突策略 |
| --- | --- | --- | --- | --- | --- |
| Vercel Skills CLI | GitHub、GitLab、任意 Git URL、本地目录、well-known HTTP index、直接 `SKILL.md`/压缩包；项目同步还会记录 `node_modules` | 全局 v3 `.skill-lock.json`；项目 v1 `skills-lock.json` | GitHub tree SHA、目录内容 SHA-256、well-known digest、ref | `skills check/update`；更新内部重新执行精确到 skill 的 `skills add` | 安装前可提示覆盖；更新会清空并重建目标目录，本地改动和额外文件不保留 |
| GitHub CLI `gh skill` | GitHub/GitHub Enterprise 仓库、本地目录 | 注入 `SKILL.md` 的 `metadata.github-*` 或 `metadata.local-path` | Git ref、commit SHA、skill tree SHA、可选 pin | `gh skill update` | pin 默认跳过；可 dry-run；当前 trunk 使用同文件系统 staging/backup 原子替换，旧内容不合并 |
| OpenAI Codex | standalone 本地/repo/user/admin/system skill；plugin 支持公共目录、local marketplace、Git、Git subdir、npm | skill 的发现路径；plugin 的 marketplace.json、`.codex-plugin/plugin.json`、`~/.codex/plugins/cache/.../$VERSION`、`config.toml` | plugin manifest version、Git ref/SHA、npm version、缓存版本目录；官方文档未定义 standalone skill 的来源 hash | standalone 可由 `$skill-installer` 安装；marketplace 用 `codex plugin marketplace upgrade` 刷新；插件安装由宿主完成 | SYSTEM/ADMIN 与本地作者目录所有权不同；plugin cache 是安装副本；plugin hooks 安装后仍需单独信任 |
| Claude Code | marketplace catalog 可来自 GitHub、任意 Git、远程 URL、本地 file/directory、settings；plugin payload 可来自相对目录、GitHub、Git、Git subdir、npm、HTTPS zip、command 产物；另有 skills-dir 和 session overlay | `known_marketplaces.json`、marketplace entry、plugin manifest、scope settings、版本 cache | 显式 version、Git SHA、archive SHA-256、command 内容 hash；某些 npm/local 为 `unknown` | `claude plugin update`、marketplace update、按 marketplace 策略后台更新；command 每会话重算 | marketplace cache 是 provider-owned 版本副本，不做三方合并；skills-dir/`--plugin-dir` 才是原地开发；managed/seed 禁止普通用户更新 |

### 3. Vercel Skills CLI（`vercel-labs/skills`）

#### 3.1 支持的来源不是只有 GitHub

官方 README 直接列出 GitHub shorthand/URL/子路径、GitLab URL、任意 Git URL和本地目录；还支持直接下载单个 `SKILL.md` 或 `.zip`、`.tar`、`.tar.gz`、`.tgz`。[README source formats and direct downloads](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/README.md#L28-L115)

源码中的 `ParsedSource.type` 明确是：

```text
github | gitlab | git | local | well-known | download
```

见 [source type definition](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/types.ts#L103-L111)。此外，项目 lock 的实现还接受 `node_modules`，说明“来源类型”和“Git provider”不是同一个概念。[project lock schema](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/local-lock.ts#L5-L45)

#### 3.2 `.agents/.skill-lock.json` v3 是安装 provenance，不是普通缓存

全局 lock 当前 schema version 为 3。默认路径是 `~/.agents/.skill-lock.json`；设置 `XDG_STATE_HOME` 时改为 `$XDG_STATE_HOME/skills/.skill-lock.json`。每个条目包含：

- `source`
- `sourceType`
- `sourceUrl`
- `ref`
- `skillPath`
- `skillFolderHash`
- `installedAt`
- `updatedAt`
- 可选 `pluginName`、`sourceBaseUrl`、`wellKnownDigest`

见 [v3 global lock schema and path](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/skill-lock.ts#L8-L105)。

项目安装另有 `./skills-lock.json` v1，记录 `source/sourceUrl/ref/sourceType/skillPath/computedHash`，并明确用所有文件内容和相对路径计算 SHA-256。[project lock and content hash](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/local-lock.ts#L5-L62)

这两个 lock 不能按文件名相似就当成一个 schema。全局 v3 的 GitHub hash 是 tree SHA；项目 v1 的 `computedHash` 是本地内容 SHA-256。

#### 3.3 更新判定和执行

全局更新按来源分组。GitHub 优先取 repository tree，然后用 `skillPath` 找 folder tree SHA；API 不可用时回退到 Git clone。非 GitHub Git 来源用 clone 后的目录 hash。well-known 来源用单独的 digest 流程。[global update check implementation](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/update.ts#L481-L641)

发现更新后，CLI 不是直接修改 lock 指向的文件，而是重新调用自身的 `add`，并传入来源、skill 名、原 ref、global/project scope 和 `-y`。[global update execution](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/update.ts#L665-L717) [project update execution](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/update.ts#L805-L921)

#### 3.4 本地修改策略和风险

安装器的替换函数先递归删除目标目录，再重新创建和复制。注释明确说这样会移除上游已重命名或删除的文件。[directory replacement implementation](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/installer.ts#L155-L170)

所以 Vercel 的语义是“安装器拥有这份安装副本”，不是在安装目录内做用户改动的三方合并。`skillctl` 可以借鉴其多来源 lock 和精确到 `skillPath` 的记录，不能照搬以下行为：

- 不能在没有识别所有权、没有检查本地 drift 时清空目录。
- 不能把旧 lock schema 静默当成空。当前 v3 读取代码对旧版本返回空 lock，这本身不适合作为通用迁移策略。[old lock handling](https://github.com/vercel-labs/skills/blob/c6f69c631292444cc541ac6d91e2226b0ff247da/src/skill-lock.ts#L78-L105)
- 不能把 direct download、local、well-known 和 Git 强行压成同一种版本判断。

#### 3.5 对 skillctl 最直接的适配点

- 把 v3 lock 作为 `vercel-skills` provider 的权威证据。
- 用 `sourceType` 路由更新，不要只判断 `sourceUrl` 是否像 Git。
- `skillFolderHash` 是“安装时上游 revision”，不是当前安装目录 hash。要另外计算 observed local hash 才能判断用户是否改过文件。
- 更新最好委托原安装器；至少要保持其 lock、链接和 canonical copy 一致。

### 4. GitHub CLI `gh skill`

#### 4.1 来源范围较窄，但来源元数据紧贴 artifact

`gh skill install` 官方支持 GitHub repository 和 `--from-local` 本地目录。未指定版本时先选最新 repository release/tag，再回退到 default branch HEAD；`--pin` 可指定 tag 或 commit SHA。[gh skill install manual](https://cli.github.com/manual/gh_skill_install)

它不声称支持任意 GitLab、archive 或 npm。不能因为目标 skill 位于 `.agents/skills` 就推断它一定由 `gh skill` 安装。

#### 4.2 安装 provenance 写入 `SKILL.md`

远程安装会把这些键注入 `metadata`：

- `github-repo`
- `github-ref`
- `github-tree-sha`
- `github-path`
- 可选 `github-pinned`

本地安装会移除 GitHub 安装键并写 `local-path`。见 [frontmatter metadata injection](https://github.com/cli/cli/blob/fdca974187d95f1020cf3378f8c269703c9cab5b/internal/skills/frontmatter/frontmatter.go#L65-L124)。

这套方案的优点是：复制 skill 时 provenance 跟着 `SKILL.md` 走，不依赖一个按名称索引的全局 lock。缺点是：它修改了 artifact 本身；发布时 `gh skill publish` 还要专门剥离 `metadata.github-*`。[gh skill publish manual](https://cli.github.com/manual/gh_skill_publish)

#### 4.3 `check/update` 行为

`gh skill update` 会扫描多个 agent host 的 user/project 目录，并比较 frontmatter 中的 tree SHA 与远端 tree SHA。pinned skill 默认跳过；`--dry-run` 只报告；没有 GitHub metadata 的 skill 在交互模式下可询问 GitHub repository，非交互模式跳过。[gh skill update manual](https://cli.github.com/manual/gh_skill_update)

当前官方 trunk 的更新实现使用同文件系统的 staging 和 backup，通过 rename 替换 skill 目录内的所有条目。失败时恢复原内容，并保留 skill 目录 inode，使现有 symlink/mount/外部引用继续指向同一个目录。[atomic in-place update source](https://github.com/cli/cli/blob/fdca974187d95f1020cf3378f8c269703c9cab5b/pkg/cmd/skills/update/update.go#L403-L523)

这里有一个必须保留的版本边界：当前 manual 描述 `--force` 会覆盖已修改文件但“不删除额外本地文件”，而当前 trunk 源码的 `swapDirectoryContents` 会移走目标目录中的全部旧条目，只放入 staged 内容。它们并不完全一致。因此 `skillctl` 不能只凭产品名复制冲突策略；provider adapter 必须声明自己适配的 CLI/version，并优先调用已安装版本的官方命令。

#### 4.4 可借鉴和不可照搬

可借鉴：

- provenance 跟 artifact 走，解决同名 skill 在不同目录中的歧义。
- `repo + ref + path + tree SHA + pinned` 是 GitHub 来源的最小充分集合。
- `dry-run`、pin、staging、backup、失败恢复是安全更新的重要基础。
- 没有 metadata 时交互补录，非交互 fail closed。

不可照搬：

- `gh skill update` 是 GitHub 专用 updater，不应拿来更新 GitLab、npm、archive、Claude marketplace 或 Codex built-in。
- frontmatter 中的 `repository` 或用户输入只能在显式确认后变成 authoritative provenance。
- 存储的 tree SHA 不等于当前本地内容 hash；仍需单独检查 local drift。

### 5. OpenAI Codex skills/plugins

#### 5.1 standalone skill 的所有权首先来自 scope 和路径

OpenAI 官方文档列出这些 skill scope：

- repo skills：`$CWD/.agents/skills`、父目录或 repo root 的 `.agents/skills`
- user skills：`$HOME/.agents/skills`
- admin skills：`/etc/codex/skills`
- system skills：Codex 由 OpenAI 内置

Codex 也支持 symlinked skill folder。[OpenAI Build skills](https://learn.chatgpt.com/docs/build-skills)

这意味着 `.codex/skills/.system` 或当前宿主暴露的 SYSTEM skill 应分类为“宿主内置/宿主管理”，不是 `source unknown`。同样，repo/user skill 的路径只能证明 scope 和发现方式，不能单独证明它来自哪个 repository。

官方文档提供 `$skill-installer` 安装 curated skills，也可以让 installer 从其他 repository 下载；但文档没有为 standalone skill 定义统一的 lock、upstream version、hash 或 `check/update` 协议。[OpenAI Build skills](https://learn.chatgpt.com/docs/build-skills)

因此，对 standalone skill：

- SYSTEM/ADMIN 可以识别所有权，但不应由 `skillctl update` 修改。
- repo/user/local 目录可以识别 scope，但若无 provider metadata，upstream 仍可能未知。
- 不应根据 skill 名称或 `agents/openai.yaml` 的展示信息猜 repository。

#### 5.2 plugin 把“分发来源”和“plugin 内容”分开

Codex/ChatGPT plugin 使用 `.codex-plugin/plugin.json`，其中有 `name`、`version`、可选 `repository` 和组件路径。local marketplace 使用 `.agents/plugins/marketplace.json`，entry 可以是：

- `local`
- Git repository root 的 `url`
- repository 子目录的 `git-subdir`
- npm registry package

Git entry 可用 `ref` 或 `sha`，npm 可用 version/range/tag 和 HTTPS registry。npm 下载明确不运行 lifecycle scripts。[OpenAI Package your plugin](https://developers.openai.com/plugins/build/plugins)

marketplace 可通过 `codex plugin marketplace add` 登记 GitHub shorthand、HTTP/HTTPS Git URL、SSH Git URL或本地目录；`codex plugin marketplace upgrade` 刷新 marketplace。官方文档还说明 unresolved plugin entry 会被跳过，而不是让整个 marketplace 失败。[OpenAI Package your plugin](https://developers.openai.com/plugins/build/plugins)

安装后的 plugin 位于：

```text
~/.codex/plugins/cache/$MARKETPLACE_NAME/$PLUGIN_NAME/$VERSION/
```

local plugin 的 `$VERSION` 为 `local`，启用状态保存在 `~/.codex/config.toml`。[OpenAI Package your plugin](https://developers.openai.com/plugins/build/plugins)

#### 5.3 官方文档的明确边界

OpenAI 当前官方文档明确了 marketplace source、plugin manifest、version cache 和 marketplace upgrade，但没有公开 standalone skill 的通用 provenance lock，也没有在上述文档中定义一个可供第三方直接调用的 standalone `skill update` 协议。

所以 `skillctl` 可识别：

- Codex SYSTEM/ADMIN skill：host-owned，report-only。
- Codex plugin cache 内 skill：plugin-owned，更新应路由 plugin/marketplace 宿主。
- local marketplace plugin：local-provider-owned，不等于手工复制 skill。
- 普通 repo/user skill：只有在其他 provider 证据存在时才确定 upstream。

不能照搬的做法：

- `.codex-plugin/plugin.json.repository` 是发布者描述，不足以单独授权更新。
- marketplace refresh 不等于所有已安装 plugin 已更新。
- 不直接改 plugin cache；它是宿主安装副本，cache path 和 manifest 应一起解释。

### 6. Anthropic Claude Code plugins/marketplaces

#### 6.1 必须分开 marketplace catalog 来源和 plugin payload 来源

Claude Code 官方文档明确说，marketplace source 和 plugin source 可以是不同 repository，并独立 pin。[marketplace source vs plugin source](https://code.claude.com/docs/en/plugin-marketplaces#plugin-sources)

catalog 可从 GitHub、任意 Git、local path、remote `marketplace.json` URL，以及 settings 中的配置登记。[discover marketplaces](https://code.claude.com/docs/en/discover-plugins#add-marketplaces) [extraKnownMarketplaces](https://code.claude.com/docs/en/settings#extraknownmarketplaces)

单个 plugin payload 可来自：

- marketplace 内相对目录
- GitHub repository
- 任意 Git URL，包括 GitLab、Bitbucket 和自托管 Git
- `git-subdir`
- npm package/registry
- HTTPS zip `archive`
- 本地 `command` 生成的目录

见 [Claude plugin sources](https://code.claude.com/docs/en/plugin-marketplaces#plugin-sources)。

此外还有两类不经过 marketplace install 的本地形态：

- `~/.claude/skills/<name>` 或 `<cwd>/.claude/skills/<name>` 内有 `.claude-plugin/plugin.json` 时，作为 `<name>@skills-dir` 原地发现。
- `--plugin-dir`/`--plugin-url` 是 session overlay。

skills-dir plugin 与普通 `<skills-dir>/<name>/SKILL.md` 是两种不同 artifact。[skills-directory plugins](https://code.claude.com/docs/en/plugins-reference#skills-directory-plugins) [local plugin testing](https://code.claude.com/docs/en/plugins#test-your-plugins-locally)

#### 6.2 持久化元数据、身份和 scope

marketplace state 按用户保存在 `~/.claude/plugins/known_marketplaces.json`。官方 seed 目录镜像也明确采用：

```text
known_marketplaces.json
marketplaces/<name>/...
cache/<marketplace>/<plugin>/<version>/...
```

见 [container plugin seed structure](https://code.claude.com/docs/en/plugin-marketplaces#pre-populate-plugins-for-containers)。

plugin 的稳定操作身份是 `plugin-name@marketplace-name`。安装 scope 包括 user、project、local 和 managed；配置分别进入对应 settings scope。[plugin installation scopes](https://code.claude.com/docs/en/plugins-reference#plugin-installation-scopes)

`enabledPlugins` 表示启用意图，不等于 payload 已安装。对外部 source，团队 settings 中启用 plugin 后，每个用户仍需执行安装。[configure team marketplaces](https://code.claude.com/docs/en/discover-plugins#configure-team-marketplaces)

#### 6.3 版本和完整性是多种机制

Claude Code 的版本解析顺序是：

1. `plugin.json.version`
2. marketplace entry `version`
3. Git source commit SHA
4. archive SHA-256
5. npm 或非 Git local directory 为 `unknown`

command source 总是根据产物计算内容 hash；有 manifest version 时组合为 `<version>-<hash>`。[Claude version management](https://code.claude.com/docs/en/plugins-reference#version-management)

显式 version 是 cache key 和 update signal。如果代码变了但 version 没有 bump，更新仍会判为已是最新。这说明 `ref/HEAD changed` 和 `provider says update available` 不是同一个判断。[Claude version management](https://code.claude.com/docs/en/plugins-reference#version-management)

archive 支持强制 SHA-256 校验；不匹配时拒绝安装。只允许 HTTPS，并拒绝 loopback、link-local、cloud metadata host，每次 redirect 都重新校验。[Claude zip archives](https://code.claude.com/docs/en/plugin-marketplaces#zip-archives)

#### 6.4 更新执行者、自动更新和本地修改

手动更新使用 `claude plugin update`；marketplace update 只刷新 catalog。自动更新在启动后后台执行：官方 marketplace 默认开启，第三方和本地开发 marketplace 默认关闭。当前 session 继续使用启动时加载的旧版本，reload 或下次启动再切换。[Claude auto updates](https://code.claude.com/docs/en/discover-plugins#configure-auto-updates)

command source 每个 session 独立重跑，不依赖 marketplace auto-update；用户首次必须审核并接受完整 command，后续只能执行已经接受的命令。[Claude command sources](https://code.claude.com/docs/en/plugin-marketplaces#command-sources)

marketplace plugin 会复制进 `~/.claude/plugins/cache`。每个 resolved version 是单独目录；更新后旧版本标为 orphan，约 14 天后后台清理。这是 provider-owned immutable copy 模型，不是供用户在 cache 内编辑的 Git worktree。[Claude plugin caching](https://code.claude.com/docs/en/plugins-reference#plugin-caching-and-file-resolution)

managed/seed ownership 更强：seed 只读、自动更新关闭，remove/update 会被拒绝，必须由管理员更新 seed。[Claude seed-managed plugins](https://code.claude.com/docs/en/plugin-marketplaces#pre-populate-plugins-for-containers)

#### 6.5 对 skillctl 的适配点

- 使用 `claude plugin list --json` 和 `claude plugin marketplace list --json` 这类官方查询接口优先于猜 cache。
- `known_marketplaces.json`、marketplace entry、plugin manifest 和 cache 要联合解释；其中任何一个都不能独立代表完整 provenance。
- plugin cache 内 hash 不符应报告 `provider cache modified`，不应当成用户可合并修改。
- skills-dir plugin 应识别为 `local-authoring/in-place`，不能显示 `source unknown`，也不能执行 marketplace update。
- managed/seed 应显示 owner 和管理员处理方式，不能尝试更新。

## 二、跨产品得到的共同结论

### 1. “来源”至少有三层

一个 `source` 字符串不足以表达真实情况。至少要分开：

1. `distribution_source`：catalog/marketplace/registry 从哪里来。
2. `payload_source`：skill/plugin 的真实内容从哪里下载或生成。
3. `install_provenance`：哪一个安装器、哪个 scope、在何时把哪个 revision 放到当前路径。

Claude marketplace 最能说明这三者可能不同；Codex plugin 也有同样结构。Git worktree 只是其中一种 payload/install 形态。

### 2. 来源、所有权、状态是三件不同的事

- 来源回答“内容从哪里来”。
- 所有权回答“谁可以安全地修改或更新这份安装”。
- 状态回答“registered/installed/enabled/active/pinned/drifted/broken/update-available”中的哪一种成立。

`enabledPlugins=true` 不证明 payload 已安装；发现一个 cache 目录也不证明它当前 enabled；看到一个 Git parent 也不证明该 skill 由这个 repository 安装。

### 3. 版本不能统一成 commit SHA

实际存在：

- Git commit SHA
- Git tree SHA
- tag/branch/ref
- semver 或 registry range
- archive SHA-256
- 目录内容 hash
- well-known digest
- generated command output hash
- built-in host version
- provider 明确返回 `unknown`

`skillctl` 应存 `revision_kind + revision_value`，并把本地观测 hash 单独存为 `observed_content_hash`。不能把二者塞进一个 `hash` 字段。

### 4. 原安装器通常才是正确的更新执行者

- Vercel lock 应由 Vercel CLI 保持。
- `metadata.github-*` 应由 `gh skill` 保持。
- Claude plugin 应由 Claude plugin manager 保持。
- Codex plugin cache 应由 Codex/ChatGPT plugin host 保持。
- Git worktree 才适合执行 fast-forward Git 更新。
- 只有显式 `skillctl track` 的复制目录，才由 skillctl 自己做受保护的目录替换。

直接对 provider cache 执行 `git pull` 或复制文件，可能破坏 lock、link、cache version、scope 或启用状态。

### 5. 各管理器元数据不兼容，必须用 provider adapter

Vercel 的 v3 JSON、`gh` 的 frontmatter、Codex marketplace/plugin cache、Claude marketplace/cache 既不共享 schema，也不共享更新语义。合适的扩展点是 provider adapter，而不是继续在一个 Git detector 中增加名称猜测。

## 三、skillctl 优化候选

以下是可评审的候选设计，不是最终架构决定。

### 1. 候选数据模型

```text
ArtifactIdentity
  provider              vercel-skills | gh-skill | codex | claude | git | skillctl | unknown
  kind                  skill | plugin-skill | plugin | built-in | generated
  qualified_name        provider/marketplace/namespace/name
  scope                 user | project | local | admin | system | managed | session
  install_path

Provenance
  distribution_source  catalog/marketplace/registry URI
  payload_source        git/local/npm/archive/well-known/command/built-in URI
  source_type
  ref
  subpath
  evidence_kind         provider-cli | provider-lock | injected-metadata | manifest | git | explicit-track | heuristic
  evidence_path
  confidence            authoritative | corroborated | hint | unknown

Revision
  resolved_version
  revision_kind         git-commit | git-tree | semver | archive-sha256 | content-sha256 | digest | host-version | unknown
  revision_value
  expected_content_hash
  observed_content_hash

Ownership
  owner                 provider | host | admin | user | repository | skillctl | unknown
  mutation_policy       provider-only | admin-only | fast-forward-only | replace-if-clean | local-editable | report-only
  update_executor       provider command/adapter identifier

State
  registered
  installed
  enabled
  active
  pinned
  drifted
  broken
  update_available
```

重点不是字段名，而是不要再让一个 `source` 字段同时承担来源、版本、所有权和更新命令。

### 2. 候选证据优先级

建议按下面顺序认定 provenance：

1. provider 官方 CLI 的结构化输出。
2. provider 官方、已版本化的 lock/registry 文件。
3. provider 注入的 install metadata，并与实际路径相互验证。
4. plugin marketplace entry + manifest + cache path 的组合证据。
5. 当前目录本身是 Git worktree，且 skill path 位于其 root 内。
6. 用户显式 `skillctl track`。
7. local authoring path/scope，只认所有权和 scope，不猜 upstream。
8. 名称、manifest `repository`、父目录 Git 等 heuristic 只能产生 hint。

两个 authoritative 证据冲突时，不自动选一个；状态应为 `ambiguous provenance`，输出冲突的证据路径。

### 3. Provider adapter 的最小集合

| Adapter | 识别证据 | check | update |
| --- | --- | --- | --- |
| `vercel-skills-v3` | `~/.agents/.skill-lock.json` 或 XDG path，schema v3，install path 对应 | 按 sourceType/ref/path/hash 检查；同时算 local observed hash | 优先调用已安装的 Vercel Skills CLI；本地 drift 时停止 |
| `gh-skill` | `SKILL.md metadata.github-*` / `local-path` | 调用 `gh skill update --dry-run` 或按 metadata 查询 | 调用匹配版本的 `gh skill update`；pin 和版本边界由 gh 处理 |
| `codex-host` | SYSTEM/ADMIN scope、plugin marketplace、plugin manifest/cache | built-in 只报告；plugin 通过宿主状态判断 | built-in 禁止；plugin 路由 Codex/ChatGPT 宿主；standalone 无官方更新协议时 report-only |
| `claude-plugin` | 官方 CLI JSON、marketplace registration、qualified ID、scope/cache | 区分 catalog、payload、installed/enabled/active | `claude plugin update`；managed/seed 禁止；skills-dir 不更新 |
| `git-worktree` | skill 的真实路径位于 Git root，且无更高优先级 provider ownership | fetch 后比较 upstream，检查 dirty/diverged | 仅 fast-forward，保持当前安全规则 |
| `skillctl-track-v1` | `sources.json` 的显式登记 | 比较 tracked revision、remote、local hash | 只有 local clean 才 staged replacement；失败 rollback |
| `local-authoring` | 明确的 local/skills-dir source 或用户声明 | 只算 drift/诊断，不查 upstream | 不自动覆盖 |

### 4. 候选识别流程

```text
扫描实例路径
  -> 解析 link/junction，保留 link path 和 target path
  -> 收集宿主 scope、provider lock、frontmatter、plugin manifest/cache、Git、track 证据
  -> provider adapters 各自返回 Claim
  -> 按证据优先级合并；冲突不猜
  -> 生成 qualified artifact identity
  -> 再执行 check；未确定 owner 时不执行 update
```

扫描对象必须按“实例”而不是只按 skill name 建模。相同 name 可以出现在不同 provider、scope 和 path。

### 5. `check` 输出候选

默认文本至少包含 provider/owner；同名或异常时显示路径：

```text
lark-doc [vercel-skills, user]: up to date
skill-creator [codex-system, system]: managed by Codex, skipped
obsidian-cli [local-authoring, user] C:\...\.codex\skills\obsidian-cli: no upstream configured
obsidian-cli [vercel-skills, user] C:\...\.agents\skills\obsidian-cli: update available
kami C:\...\.claude\skills\kami: broken junction -> C:\...\.agents\skills\kami
```

建议稳定区分：

- `managed by <provider>`
- `local authoring; upstream not configured`
- `source metadata missing`
- `ambiguous provenance`
- `provider cache modified`
- `broken link -> <target>`
- `update available`
- `pinned`

`unmanaged (source unknown)` 只用于已经完成 provider 探测、仍没有来源或所有权证据的实例，不能作为“没有 `.git`”的同义词。

### 6. 安全更新候选流程

1. 确定 authoritative owner 和 update executor。无法确定就停止。
2. 检查 scope/policy：SYSTEM、ADMIN、managed、seed、session overlay 默认禁止修改。
3. 只刷新 metadata/catalog，先不写安装目录。
4. 同时比较 upstream revision、expected installed revision 和 observed local hash。
5. provider-owned cache 发生 drift 时标记 tampered/modified，默认 repair prompt，不静默覆盖。
6. local-authoring 只报告，不覆盖。
7. explicit tracked copy 只有 clean 时进入 staging replacement。
8. staging 必须与目标同文件系统；验证 skill name、路径不逃逸、manifest、文件数量/大小和 symlink policy。
9. 使用 backup + atomic rename；失败恢复原目录。
10. 最后由 provider 写回它自己的 lock/metadata。provider 更新失败时不得伪造成功状态。

这套流程借鉴 `gh` 的 staging/rollback，但不照搬其“安装副本全部替换”语义。是否保留额外文件必须由 `mutation_policy` 决定。

### 7. 本机快照应如何映射

根据本次任务的已确认快照：77 个实例、73 个名称；Vercel v3 lock 有 38 条；Codex SYSTEM 有 6 条；存在 4 个重复名称；`kami` 是失效 junction。对应的候选分类是：

- 38 条 v3 lock：由 `vercel-skills-v3` adapter 认领，不应要求逐一 `track`。
- 6 条 Codex SYSTEM：`owner=host`、`mutation_policy=provider/admin-only`，不显示 `source unknown`。
- 4 个重复名称：保留为不同 instance，用 provider/scope/path 区分，不去重成一个 artifact。
- `kami`：先报告 broken link 和 target；来源识别是次要问题，不能对失效 target 执行 update。

### 8. 分阶段落地候选

#### P0：修正识别和输出，不改变更新行为

- 引入 instance identity，重复名称显示路径。
- 识别 broken link target。
- 识别 Codex SYSTEM/ADMIN ownership。
- 读取 Vercel v3 lock，只用于 `check` 分类。
- 增加 `--json` 结构化输出，暴露 evidence/owner/provider/path。

验证标准：本机 38 条 v3 lock 不再显示 `source unknown`；6 条 SYSTEM 显示 host-managed；4 个重复名称可区分；broken junction 显示 target。

#### P1：provider-native check

- 实现 `vercel-skills-v3`、`gh-skill`、`claude-plugin` 的 read-only check adapter。
- Codex plugin 和 standalone skill 按官方能力边界分类。
- 加入 ambiguous provenance 和 local drift 检查。

验证标准：`check` 不写 provider lock/cache；同一实例的 provider、revision kind、owner 和 update executor 可解释。

#### P2：安全更新路由

- provider-owned artifact 委托原 updater。
- Git worktree 保持 fast-forward-only。
- explicit track 保持 staged replace + rollback。
- managed/built-in/local-authoring/ambiguous 默认拒绝自动更新。

验证标准：每次成功更新都能证明由正确 executor 完成，并能验证 provider metadata、目标内容和回滚边界。

## 四、明确不建议做的事

- 不根据 skill 名称搜索 GitHub 后自动绑定来源。
- 不根据 manifest 的 `repository` 字段自动授权更新。
- 不把父目录存在 `.git` 当作 plugin cache 的 owner。
- 不把所有 hash 当成同一种 revision。
- 不直接修改其他产品未公开 schema 的内部 JSON。
- 不把 registered、installed、enabled、active 合并成一个布尔值。
- 不对 `source unknown`、managed、built-in、session overlay 执行自动更新。
- 不在 routine update 中隐式迁移或删除旧路径。
- 不因为 provider cache 有本地修改就尝试三方合并；先按 owner 和 mutation policy 分类。

## 五、最终优化方案

### 1. 结论

`skillctl` 不应继续扩展成“更多 Git 判断”。它应成为一个小型的**来源识别与更新编排器**：先识别每个安装实例由谁管理、依据是什么、用哪种 revision 判断更新，再把更新交给正确的 executor。

首版不做动态插件 SDK。adapter 采用 Go 内置实现，覆盖已经存在且可验证的来源类型；这已经能形成稳定扩展点，又不会为了未来可能出现的 provider 引入进程协议、插件发现、兼容性和安全边界。

### 2. 第一性原理

1. **skill 名称不是唯一标识。** 唯一对象是安装实例：canonical path + alias paths + declared name + host/scope。
2. **来源、所有权、版本和本地状态是四件事。** Git URL 不能同时代表谁能更新、当前安装版本和本地是否被修改。
3. **没有权威证据就不猜。** 名称相同、manifest 中出现 repository、父目录存在 `.git`，都不能自动授权覆盖文件。
4. **更新必须服从 owner。** provider 安装的内容由 provider updater 更新；Git worktree 由 Git 更新；用户本地创作只报告；SYSTEM/ADMIN/built-in 默认只读。
5. **`check` 必须只读。** 它可以读取元数据、刷新远端信息和写 skillctl 自己的临时 cache，但不能修改 skill、provider lock 或 provider cache。
6. **provider revision 和本地内容 hash 分开。** commit、tree SHA、semver、archive digest 和 host version 保留原语义；统一的 directory hash 只用于判断本地 drift。
7. **冲突时失败关闭。** 两个 authoritative claim 指向不同来源或 owner 时，输出 `ambiguous provenance`，不执行更新。

### 3. 模块边界

CLI 只负责解析参数、加载配置和渲染结果。业务入口收敛成一个深模块；当前只有一个实现，不额外定义单实现 interface：

```go
func Run(context.Context, Request) (Report, error)
```

Engine 内部按固定流水线工作：

```text
Discover instances and aliases
  -> Collect claims from every applicable adapter
  -> Reconcile claims and determine owner
  -> Plan read-only checks or updates
  -> Execute with the selected owner/executor
  -> Return one normalized report
```

这条 seam 隐藏扫描、link 解析、provider schema、Git、hash、staging、rollback 和状态迁移。CLI 不再知道“Git skill”和“copied skill”两个分支，新增 provider 时也不需要继续扩大 `main.go` 的条件树。

首批内置 adapter：

- `vercel-skills-lock-v3`
- `gh-skill-metadata`
- `codex-host`
- `claude-plugin`
- `git-worktree`
- `skillctl-track-v1`
- `local-authoring`

adapter 返回 `Claim` 和 capabilities，不直接打印、不自行决定证据优先级，也不直接修改别的 provider 的 state。

### 4. 证据合并规则

证据不是简单地“取优先级最高的一条”，而是先全部收集再协调：

1. 用户显式 `skillctl track` 可以覆盖 heuristic/hint，但不能静默夺取 provider 已经权威认领的路径。两者冲突时初版直接标记 ambiguous；以后若确有需求，再设计显式 ownership transfer。
2. provider CLI、版本化 lock、注入 metadata 和宿主 manifest/cache 是 authoritative claim。
3. 指向同一来源、ref 和 subpath 的 claim 合并，保留全部 evidence paths。
4. authoritative claim 冲突时标记 ambiguous，不退回名称猜测。
5. ancestor Git 只有在 `SKILL.md` 确实被该仓库跟踪时才能认领实例，例如通过 `git ls-files --error-unmatch` 验证；它不能覆盖更精确的 provider claim。
6. Vercel 全局 lock 的条目只绑定它声明的 install root 下对应路径，不绑定其他 host 中的同名副本。
7. 只有 hint 时，将实例归类为 `local/untracked`，允许用户显式 adoption，但不自动建立来源。

### 5. 配置和状态

目录继续由配置文件管理，不把外部产品路径散落硬编码在扫描逻辑中。保留现有 `paths` 兼容性，同时增加带语义的 roots/manifests：

```toml
[[roots]]
path = "~/.agents/skills"
host = "universal"
scope = "user"

[[roots]]
path = "~/.codex/skills"
host = "codex"
scope = "user"

[[manifests]]
kind = "vercel-skills-lock-v3"
path = "~/.agents/.skill-lock.json"
install_root = "~/.agents/skills"

[[managed_roots]]
path = "~/.codex/skills/.system"
owner = "codex"
```

默认配置可以列出常用位置，但运行逻辑只消费配置后的 root 描述。外部 provider lock 始终是它自己的权威来源；skillctl 可以保存 evidence digest、上次检查结果和本地安全 baseline，但不能复制并篡改成另一套来源真相。现有 `sources.json` v1 只表示用户显式 track，后续 schema 升级必须无损迁移。

### 6. 各来源的 check 和 update 策略

| 来源 | `check` | `update` |
| --- | --- | --- |
| Vercel v3 lock | 原生解析 v3，按 `(source, ref)` 批量获取一次，比较 provider revision 和 local hash | 初版委托已安装且版本受支持的 `skills update <exact-skill>`；更新前检查 drift，更新后重新发现并核验 lock 和内容；不直接重写其 lock |
| `gh skill` | 解析注入 metadata，遵守 pin；可调用匹配版本的 dry-run | 委托 `gh skill update`，再核验 metadata 和内容 |
| Codex SYSTEM/ADMIN | 报告 host-managed 和 host version/capability | 不修改；交给 Codex 自己升级 |
| Codex plugin | 以 marketplace、manifest、cache 和宿主状态确定 plugin identity；一个 plugin 下多个 skills 合并检查 | 只调用当前 Codex 版本明确提供的宿主/marketplace 命令；没有 installed-plugin 更新能力时 report-only |
| Claude plugin | 以 marketplace、manifest、cache 和官方 CLI 状态确定 plugin identity；一个 plugin 下多个 skills 合并检查 | 每个 plugin 只调用一次 `claude plugin update`；managed/seed/session overlay 不更新 |
| Git worktree | 每个 repo fetch 一次，比较 upstream；检查 dirty/ahead/diverged | 保持 `--ff-only`，且 repository root 必须落在允许更新的边界 |
| skillctl explicit track | 每个 `(source, ref)` fetch 一次，比较远端内容和安装 baseline | local clean 才 staging replace；保存 state 失败时 rollback |
| local/manual/archive without provenance | 报告 `local/untracked (no update source)` | 不更新；用户可显式 track/adopt |

若 provider executable 不存在、版本不受支持或行为与对应 schema 不匹配，`check` 仍可报告 update 状态；`update` 必须说明“有更新，但没有安全 executor”，不能回退成自写的近似更新。

adapter 还必须声明 executor 的更新粒度，例如 instance、同名集合、plugin 或全局。用户指定单个实例，而 provider CLI 只能更新更大范围时，skillctl 必须先算出受影响集合；不能精确限制且不是默认全量更新时就拒绝委托，避免顺带改动用户没有选择的 skill。

OpenAI standalone installer 当前不会为复制出来的 skill 写统一 provenance，因此这类副本无法仅凭名称安全恢复来源。即使在官方 catalog 找到同名、同内容候选，也只能提示用户显式 adoption，不能自动绑定。

### 7. 对当前代码的必要调整

只做与目标直接相关的改动：

1. 将 scanner 输出从 `skill{Name, Path}` 升级为 instance，保留 canonical path、link/junction alias、scan root、host 和 scope。
2. 将 broken link 作为诊断实例返回，并显示 link target；不要只向 stderr 输出后丢弃。
3. 用 claim collection/reconciliation 替换 `processGit` 中“tracked 或 Git，否则 unmanaged”的二选一。
4. Git adapter 增加 `SKILL.md` 被仓库跟踪的验证，避免无关父工作区误认 owner。
5. 把 `processTracked` 的逐 skill `syncSource` 改成 run-scope source session；同一 `(source, ref)` 每次命令只 fetch/resolve 一次。
6. 输出按 instance 生成。名称重复或状态异常时显示路径；正常唯一实例保持简洁。
7. 添加稳定的 `--json` report schema，使 provider、owner、evidence、revision、drift 和 executor 可被脚本验证。

暂不引入并发、动态 adapter、在线按名称搜索、通用 registry、OCI、签名体系或 GUI。

### 8. 分阶段实施

#### v0.2：先让识别结果正确

- instance/alias/broken-link 模型。
- 结构化 roots/manifests 配置，兼容现有 `paths`。
- Vercel v3 lock、Codex managed roots、Git tracked-file 和 explicit track claim。
- Vercel v3 的 provider-native read-only check；38 条 lock entry 必须能真正判断更新，而不只是换一个分类名称。
- claim 冲突处理、重复名输出和 `--json`。
- source session 去重，同一源只 fetch 一次。

完成标准：本机 38 条 v3 lock 不再是 `source unknown`；6 条 SYSTEM 显示 Codex-managed；4 个重复名称可按路径区分；`kami` 显示失效 target；`check` 不改 skill/provider state。

#### v0.3：安全接入原 updater

- Vercel 和 `gh skill` 的版本探测、精确委托及事后验证。
- Codex/Claude plugin 的 plugin-level ownership 和委托更新。
- provider cache drift、pin、ambiguous provenance 的拒绝路径。

完成标准：每次成功更新都能给出 authoritative evidence、executor 和更新后 revision；任何本地修改、owner 冲突或 executor 缺失都不会覆盖文件。

#### 后续候选

只有出现真实需求和稳定协议后，再考虑新 provider、动态 adapter 或额外分发格式。并发继续延后；先保证同一来源只请求一次，已能解决当前主要性能浪费。

### 9. 验收测试

至少覆盖以下行为：

- Vercel v3 fixture、同源多 skill 和一次 fetch。
- provider claim 高于 ancestor Git；冲突 claim fail closed。
- Git 只认 tracked `SKILL.md`。
- 重复名、junction/symlink alias、broken target。
- Codex SYSTEM/ADMIN report-only。
- pin、local drift、provider executable 缺失和 provider update 失败。
- explicit track 的 staging、state 保存失败 rollback。
- `check` 前后 skill 目录和 provider metadata 无变化。
- Windows junction；macOS/Linux symlink。
- `go test ./...`、`go vet ./...` 和六个目标的交叉编译；三系统真实运行仍由后续 CI 分别验证，不能用交叉编译代替。
