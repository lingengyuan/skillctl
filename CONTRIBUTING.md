# 贡献指南

## 开发环境

需要 Go 1.27 和 Git。测试使用真实的临时 Git 仓库，不需要网络。

```sh
go build -o ./dist/skillctl .   # Windows 输出 ./dist/skillctl.exe
go test ./...
go vet ./...
```

提交前建议跑完整门禁，与 CI 一致：

```sh
gofmt -l .                                          # 必须无输出
go test ./... -count=1 -timeout 60s
go test -tags=ablation ./... -run '^$'              # 消融基准可编译
go test -race ./... -count=1 -timeout 120s
go test -tags=integration ./... -run '^TestIntegration' -count=1
go vet ./...
staticcheck -checks=all -tags=ablation ./...
```

`staticcheck` 与 `govulncheck` 由 CI 的 `analyze` job 执行。

## 代码结构

依赖方向是 `main` → `internal/app` → 各职责包，职责包不反向依赖 `app`。

| 目录 | 职责 |
|---|---|
| `internal/app` | 清单与来源归属、生命周期计划、事务恢复、命令编排 |
| `internal/cli` | 参数解析与校验、分命令帮助、Agent 别名 |
| `internal/gitstore` | Git 来源缓存、锁、远端同步 |
| `internal/installhistory` | 从安装记录提取候选来源 |
| `internal/skilldoc` | SKILL.md 解析与校验 |
| `internal/archive` | 归档校验与展开限制 |
| `internal/fsutil` | 跨平台路径身份、摘要、文件替换、内核锁 |

涉及安装状态与事务的一致性规则集中在 `app`，不要把这些状态暴露给多个包。

## 修改约定

**写操作必须走事务。** 任何改变文件的动作都通过 `prepareOperation` + `apply`，它负责前置校验、镜像备份、执行后校验和历史记录。不要直接 `os.Remove`/`os.WriteFile` 修改受管内容。

**新增命令要同时更新三处**：`internal/cli/command_help.go` 的 `commandHelpText`、`internal/cli/usage.go` 的总览。测试 `TestGlobalUsageCoversEveryCommandOption` 会检查命令页里的每个 flag 都出现在总览中。

**不要绕过能力声明。** 一个条目能否执行某动作由 `assetCapabilities` 判定；宿主未提供的操作应显式拒绝并给出原因，而不是尝试后失败。

**诊断要按问题去重。** 同一物理问题可能被多个扫描根观察到，报告前先按路径归并。见 `reportDanglingLinks`。

**修复前先验证。** `doctor --fix` 只做能验证的改动。删除悬空链接前会检查目标父目录是否存在；父目录也缺失时保留并警告，因为无法区分「内容已删除」与「外置卷未挂载」。

## 测试

测试贴近被测包：参数、文档、归档、缓存、文件操作各自独立测试；跨模块的命令与生命周期测试放在 `internal/app`。

`internal/app` 的测试通过 `run(args, stdout, stderr)` 调用与可执行文件相同的入口，不要为测试另建入口。

新增行为需要能捕获消费者可见的缺陷：行为、边界、不变量、状态迁移、优先级、错误。不要为 wiring、转发、mock 回声或源码文本写测试。

修复缺陷时优先留下回归测试。若不可行（例如依赖外部服务），在 PR 中说明验证方式。

## 提交

提交信息用祈使句，说明改动结果而非过程：

```
fix: keep retract effective by publishing above the retracted range
feat: report candidates when a source holds duplicate skill names
```

一个提交只做一件事。重构与行为变更分开提交，便于审查。
