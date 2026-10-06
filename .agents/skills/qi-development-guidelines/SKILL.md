---
name: qi-development-guidelines
description: "在 qi 仓库中实现或重构 Go 代码时，先遵循适用的设计文档、目录结构、架构边界和验证要求。"
---

# qi 开发规范

在本仓库实现功能、修复问题、重构代码或新增模块时使用本 skill。它不替代代码审查或发布流程，重点是让实现与仓库已有设计保持一致。

## 核心原则

- **设计文档优先**：开始编码前，查找并阅读任务涉及目录及其父目录中的 `DESIGN.md`、`README.md`、架构文档和接口说明。设计文档明确规定的目录结构、包边界、数据流、生命周期、错误语义和安全要求，视为实现契约。
- **先确认结构再写代码**：先确定功能属于 `internal/core`、`internal/shared` 还是未来的业务模块；优先扩展已有包和设计文档规定的文件，不要因为方便而创建平行目录或重复抽象。
- **遵守边界**：基础设施包只提供基础能力，业务规则由业务层负责；不要让 token/session、数据库、Redis、MQ 或 HTTP 细节越过设计规定的边界。
- **兼容当前模块**：模块名为 `qi`，内部导入使用 `qi/internal/...`。不得重新引入历史路径 `fox`、`internal/platform` 或文档中已经不存在的包路径。
- **最小且完整的变更**：保持现有 API 和行为，除非需求或设计文档明确要求破坏性变化；同时补齐错误路径、清理路径、并发路径和配置校验。

## 实施流程

1. 阅读根目录 `AGENTS.md` 和最近的目录级指导文件。
2. 使用 `rg --files` 查找适用的设计文档，并按 [设计文档工作流](references/design-document-workflow.md) 提炼约束。
3. 建立简短的实现映射：需求 -> 目标包/文件 -> 设计依据 -> 测试位置。若文档与现状冲突，先保留设计意图，结合当前代码消解路径差异，并在交付说明中指出冲突。
4. 按既有目录结构实现。新增目录或跨层依赖必须有明确设计依据；没有依据时优先放入最接近职责的现有包。
5. 复用现有配置、错误、日志、上下文、Redis key、MQ 契约和测试辅助设施，不重复实现同类机制。
6. 对外部资源、goroutine、锁、定时器、连接和 Redis 状态明确所有权、关闭方式、超时和失败语义。
7. 完成后运行格式化、针对性测试和仓库级验证；根据变更范围选择竞态检测、构建和静态检查。

## 本仓库重点约束

- `internal/shared/auth/DESIGN.md` 是认证会话内核的设计依据。认证包负责 token、refresh token、Redis session、并发策略和认证事件；不负责账号密码、权限菜单、HTTP 响应或 WebSocket 在线状态。
- `internal/core/mq` 是公共消息队列契约；Redis Streams 和 RabbitMQ 只能实现该契约，不应把代理细节泄漏给业务层。
- `internal/core/database`、`internal/core/redis`、`internal/core/logger` 和 `internal/core/observability` 是可复用基础设施，配置验证和资源关闭应沿用现有模式。
- `configs/core.yaml` 只承载 `internal/core` 各基础设施包的配置，可以包含 `server`
  等 core 包配置；不能在其中添加认证策略或业务模块配置。运行时 Provider、
  客户端连接和其他不可序列化依赖也不应写入 YAML。

## 验证要求

至少运行：

```bash
gofmt -w <修改过的 Go 文件>
go test ./...
go build ./...
```

涉及共享状态、并发刷新、Lua/Redis 原子操作、goroutine 或资源生命周期时，再运行：

```bash
go test -race ./...
```

最终说明实际执行的命令、未执行检查及原因，以及设计文档与当前代码存在的已知差异。
