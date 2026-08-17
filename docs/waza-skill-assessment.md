# Waza Skill 评估

## 结论

Waza 是一个包含 8 个通用工程工作流 skill 的仓库。结合当前 `skillctl` 的使用场景，建议优先使用 4 个：

1. `think`：开发前做方案和架构判断。
2. `hunt`：排查错误、回归和性能问题。
3. `check`：代码审查、合并前检查和发布前检查。
4. `health`：检查 Codex、Claude 以及 skill 的配置、来源和供应链。

`read` 和 `learn` 有辅助价值，按需安装。`write` 和 `ui` 不属于当前 CLI 工具的核心需求，暂不需要。

这不是 Waza 的代码质量评分，而是判断哪些 skill 对当前 `skillctl` 用户有实际帮助。Waza 仓库本身只提供通用工作流，不提供 skill 更新器，也不能替代 `skillctl` 的扫描、来源识别和 Git 更新能力。

## 调研范围和版本

本次只使用 Waza 官方仓库的内容：

- 仓库：<https://github.com/tw93/Waza>
- 调研提交：[`30bf563ccba94652081b53a0d574ef91c32516ee`](https://github.com/tw93/Waza/commit/30bf563ccba94652081b53a0d574ef91c32516ee)
- 版本文件：[`VERSION`](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/VERSION)，内容为 `3.34.0`
- 许可证：[`LICENSE`](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/LICENSE)，MIT

阅读范围包括 `README.md`、`AGENTS.md`、`package.json`、`plugins/waza/plugin.json`、`skills/RESOLVER.md`、8 个 `SKILL.md`、全部 agent 定义、各 skill 的 references/scripts 目录，以及安装和打包脚本。

## Skill 清单和建议

| Skill | 官方定义 | 对当前项目的价值 | 建议 |
|---|---|---|---|
| `think` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/think/SKILL.md) | 在写代码前明确目标、约束、方案、风险和验证步骤。适合 `skillctl` 的架构判断和范围控制。 | **优先使用** |
| `hunt` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/hunt/SKILL.md) | 要求先复现并定位根因，再修改代码。适合处理 `skillctl check` 变慢、来源识别错误和更新失败。 | **优先使用** |
| `check` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/check/SKILL.md) | 覆盖代码审查、CLI 表面、生成物、安装路径、提交和发布前验证。适合维护 Go CLI。 | **优先使用** |
| `health` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/health/SKILL.md) | 检查 Codex/Claude 配置、指令、hooks、MCP、skill 来源和 AI 可维护性。和 `skillctl` 的实际问题高度相关，但它是审计工作流，不是更新器。 | **优先使用** |
| `read` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/read/SKILL.md) | 读取 GitHub URL、网页和 PDF，便于核对 skill 官方来源和安装说明。 | **按需使用** |
| `learn` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/learn/SKILL.md) | 六阶段深度研究流程，适合调研同类工具，但对日常 CLI 开发偏重。 | **按需使用** |
| `write` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/write/SKILL.md) | 中文和英文 prose 改写、发布文案和本地化。不能替代技术 README 的事实核对。 | **暂不需要** |
| `ui` | [SKILL.md](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/ui/SKILL.md) | 前端页面、组件、排版和截图审美迭代。当前项目明确是 CLI，没有 UI。 | **不需要** |

## 推荐安装集合

如果只为维护当前 `skillctl`，建议安装：

```text
think
hunt
check
health
```

如果同时需要调研 GitHub 来源和陌生技术，再加：

```text
read
learn
```

不建议仅为了 `skillctl` 安装 `write`、`ui`。它们是通用 agent 能力，不是当前 CLI 的运行依赖。

## 官方安装和更新方式

Waza README 提供的统一安装方式会安装 8 个 skill：

```bash
npx skills add tw93/Waza -a claude-code codex cursor antigravity-cli -g -y
```

官方说明该命令会在共享 `~/.agents/skills` 中保留一个规范副本，并为 Claude Code 等工具创建链接。后续更新使用：

```bash
npx skills update -g -y
```

官方 README 同时提供原生插件方式：

- Claude Code：`/plugin marketplace add tw93/Waza`，再执行 `/plugin install waza@waza`
- Codex：`codex plugin marketplace add tw93/Waza`，再执行 `codex plugin add waza@waza`
- Claude Desktop：下载 release ZIP 后导入
- Pi：`pi install npm:@tw93/waza`

来源：[`README.md` 安装说明](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/README.md#install)。

## 对 skillctl 的跟踪建议

### 直接安装的 skill

对于 `npx skills add` 安装的标准副本，建议记录：

```text
source: https://github.com/tw93/Waza.git
skillPath: skills/<skill-name>
```

例如：

```text
source: https://github.com/tw93/Waza.git
skillPath: skills/check
```

`think`、`hunt`、`check`、`health`、`read`、`learn`、`write`、`ui` 都使用同一个 Git 仓库，只是 `skillPath` 不同。不要把整个 Waza 仓库当成一个单独的 skill，也不要把 `rules/`、`scripts/`、`tests/` 或 `plugins/waza/` 下的辅助文件误报为独立 skill。

### 插件安装的副本

官方 `AGENTS.md` 说明 `plugins/waza/` 是为 Codex 插件生成的镜像，源码仍以 `skills/` 为主。若本机副本来自原生插件，必须先比较 `SKILL.md`、references 和 scripts 的内容，再决定使用 `skills/<name>` 还是插件镜像对应路径，不能仅凭目录名称写入来源。

特别是仓库当前提交中，`skills/write/` 与 `plugins/waza/skills/write/` 存在内容差异。这说明插件镜像不能无条件当作标准 skill 副本处理，`skillctl` 应保留内容匹配失败时的明确提示。

### 共享副本和链接

Waza 官方安装说明明确使用共享目录和链接。`skillctl` 扫描时应将真实目录作为规范实体，把 Claude、Trae、Cursor 等目录中的链接视为别名，避免同一个 Waza skill 被重复跟踪或重复更新。

## 不是独立 Skill 的目录

以下目录属于 Waza 的实现或分发支持面，不应作为用户 skill 单独跟踪：

1. `plugins/waza/`：Codex 插件及生成镜像。
2. `rules/`：跨 skill 的共享规则，例如反模式、语言和路由规则。
3. `scripts/`：打包、安装、元数据生成、验证和 statusline 脚本。
4. `skills/*/references/`：按条件加载的参考资料，不是独立入口。
5. `skills/*/scripts/`：skill 使用的确定性辅助脚本，不是独立入口。
6. `skills/*/agents/`：`check` 和 `health` 的专用审查/检查提示，不是独立入口。
7. `tests/`：仓库测试，不是可安装 skill。

入口和路由集中在 [`skills/RESOLVER.md`](https://github.com/tw93/Waza/blob/30bf563ccba94652081b53a0d574ef91c32516ee/skills/RESOLVER.md)。它要求多个 skill 同时匹配时先阅读各自定义，并通过人工步骤串联，不会自动把多个 skill 合并成一个更新单元。

## Agent 定义清单

Waza 只有两个 skill 提供专用 agent 定义：

1. `check/agents/reviewer-architecture.md`：检查模块耦合、接口契约、抽象泄漏、依赖方向和新增的可扩展性瓶颈。
2. `check/agents/reviewer-security.md`：检查注入、认证绕过、凭据泄露、输入校验和信任边界。
3. `health/agents/inspector-context.md`：检查上下文、指令、skill 路由、来源和安全扫描。
4. `health/agents/inspector-control.md`：检查 hooks、权限、MCP、验证和重复行为模式。
5. `health/agents/inspector-maintainability.md`：检查可维护性、验证覆盖、文档引用和生成物漂移。

这些定义被 `check` 或 `health` 内部调用，不能单独安装或单独更新。

## 最终建议

对于当前 `skillctl` 的用户目录，优先确认并跟踪以下 4 个 Waza skill：

```text
https://github.com/tw93/Waza.git#skills/think
https://github.com/tw93/Waza.git#skills/hunt
https://github.com/tw93/Waza.git#skills/check
https://github.com/tw93/Waza.git#skills/health
```

再根据实际需要添加 `read` 和 `learn`。不把 `write`、`ui`、agent 定义、references、scripts、rules 和插件镜像当作当前项目必需 skill。后续若 `skillctl` 能识别安装器或插件的来源，应优先记录安装器的规范实体和明确 `skillPath`，而不是通过名称猜测来源。
