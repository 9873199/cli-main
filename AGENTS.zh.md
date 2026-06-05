# AGENTS.md（中文版）

## 目标（每个 PR 选择一项）

- 改善 CLI 体验：优化用户体验、错误消息、帮助文本、命令行标志和输出清晰度。
- 提升可靠性：修复 Bug、边界情况和回归问题，并补充测试。
- 提高开发效率：简化代码路径、降低复杂度、保持行为显式化。
- 强化质量门禁：加强测试/Lint/检查，不增加繁重流程。

## 构建与测试

```bash
make build          # 构建（先运行 fetch_meta） 
make unit-test      # PR 前必须通过（带 -race 竞态检测）
make test           # 完整测试：vet + 单元测试 + 集成测试
```

## 通知静默配置

`lark-cli` 会在 JSON 信封的 `_notice` 字段中发出两种通知，用于引导 AI 代理进行修复：

- `_notice.update` — npm 上有更新的二进制文件可用
- `_notice.skills` — 本地安装的 skills 与当前运行的二进制文件不同步

在非 CI 脚本中静默这些通知（CI 环境自动跳过）：

| 环境变量 | 效果 |
|---------|------|
| `LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1` | 静默 `_notice.update` |
| `LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1` | 静默 `_notice.skills` |

两种通知推荐相同的修复命令：`lark-cli update`。skills 通知的 `current` 字段在 skills 从未同步时（冷启动）为 `""`，在同步过旧版本二进制文件时（漂移）为版本字符串。

## PR 前检查（与 CI 门禁一致）

1. `make unit-test`
2. `go vet ./...`
3. `gofmt -l .` — 必须无输出
4. `go mod tidy` — 不能改变 `go.mod`/`go.sum`
5. `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6 run --new-from-rev=origin/main`
6. 如果依赖有变更：`go run github.com/google/go-licenses/v2@v2.0.1 check ./... --disallowed_types=forbidden,restricted,reciprocal,unknown`

## 提交与 PR

- 使用英文的约定式提交：`feat:`、`fix:`、`docs:`、`test:`、`refactor:`、`chore:`、`ci:`
- PR 标题使用相同格式。完整填写 `.github/pull_request_template.md`。
- 绝不提交密钥、Token 或内部敏感数据。

## 源码结构

| 路径 | 功能说明 |
|------|---------|
| `cmd/root.go` | 入口点、命令注册、严格模式裁剪 |
| `cmd/profile/` | 多配置文件管理（add/list/use/rename/remove） |
| `cmd/config/` | 配置初始化、显示、严格模式 |
| `cmd/service/` | 从嵌入元数据自动注册的 API 命令 |
| `shortcuts/common/runner.go` | 快捷命令执行管线、Flag.Input（@file/stdin）解析 |
| `shortcuts/` | 领域特定的快捷命令实现 |
| `internal/cmdutil/factory.go` | 工厂模式 — 身份解析、凭证、配置 |
| `internal/cmdutil/factory_default.go` | 生产环境工厂装配 |
| `internal/credential/` | 凭证提供者链（扩展 → 默认） |
| `extension/credential/` | 面向插件的凭证接口和环境变量提供者 |
| `internal/client/client.go` | APIClient：DoSDKRequest、DoStream |
| `internal/core/config.go` | 多配置文件的配置加载/保存 |
| `internal/vfs/` | 文件系统抽象（使用 `vfs.*` 替代 `os.*`） |
| `internal/validate/path.go` | 路径安全校验 |

## 谁在使用这个 CLI

本 CLI 的主要消费者包括 AI 代理（Claude Code、Cursor、Gemini CLI）。你的代码会被机器读取 — 错误消息、输出格式和标志设计都直接影响代理的成功率。

必须牢记的一条规则：**你写的每一条错误消息都会被 AI 解析以决定下一步操作。** 让错误结构化、可操作、具体明确。

## 代码约定

### 命令中的结构化错误

`RunE` 函数必须返回 `output.Errorf` / `output.ErrWithHint` — 绝不能使用裸 `fmt.Errorf`。AI 代理将 stderr 作为 JSON 解析；裸错误会破坏这一约定。

### stdout 是数据，stderr 是其他一切

程序输出（JSON 信封）写入 stdout。进度、警告、提示写入 stderr。混用会破坏管道链。

### 使用 `vfs.*` 替代 `os.*`

所有文件系统访问通过 `internal/vfs` 进行。这样可以支持测试 Mock。

### 读取前校验路径

CLI 参数不可信（它们来自 AI 代理）。在任何文件 I/O 之前调用 `validate.SafeInputPath`。

### 测试

- 每个行为变更都需要附带对应的测试。
- 使用 `cmdutil.TestFactory(t, config)` 创建测试工厂。
- 使用 `t.Setenv("LARKSUITE_CLI_CONFIG_DIR", t.TempDir())` 隔离配置状态。

### E2E 测试

**Dry-run E2E（每次快捷命令变更必须提供）**
- 验证请求结构，不调用真实 API
- 放置在 `tests/cli_e2e/dryrun/` 或对应的领域目录中
- 设置环境变量 `LARKSUITE_CLI_APP_ID`/`APP_SECRET`/`BRAND`，使用 `--dry-run`，断言 method/URL/params
- 无需密钥 — 可在 Fork PR 上运行
- 先用 `lark-cli <domain> --help` 和 `lark-cli schema` 探索正确参数

**Live E2E（新流程或行为变更必须提供）**
- 验证真实 API 往返
- 放置在 `tests/cli_e2e/<domain>/`
- 必须自包含：创建 → 使用 → 清理
- 需要 Bot 凭证（CI 密钥，Fork PR 自动跳过）
- 参考：`tests/cli_e2e/task/task_status_workflow_test.go`

| 变更类型 | Dry-run E2E | Live E2E |
|---------|:-----------:|:--------:|
| 新增快捷命令 | 必需 | 必需 |
| 修改快捷命令标志/参数 | 必需 | 行为变更时需要 |
| 快捷命令 Bug 修复 | 必需 | 有回归风险时需要 |
| 内部重构（不影响快捷命令） | 不需要 | 不需要 |
