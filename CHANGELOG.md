# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-10-31

### 🎯 重大变更

这是 Aurora 框架的第一个稳定版本，专注于核心 HTTP 功能和通用中间件。

### ⚠️ BREAKING CHANGES

- **移除 `core/auth` 包**：JWT 认证管理功能已移除
- **移除 `core/ratelimit` 包**：分布式限流功能已移除
- **移除 `middleware.Auth`**：认证中间件已移除
- **移除 `middleware.RateLimit`**：限流中间件已移除
- **移除相关 API**：
  - `Lifecycle.Auth(name)`
  - `Lifecycle.AuthNames()`
  - `Lifecycle.Attempt(name)`
  - `Lifecycle.AttemptNames()`
  - `Lifecycle.RateLimit(name)`
  - `Lifecycle.RateLimitNames()`
  - `ErrAuthInstanceNotFound`
  - `ErrAttemptInstanceNotFound`
  - `ErrRateLimitInstanceNotFound`

### ✨ 新增

- **零依赖架构**：所有中间件现在都基于 Go 标准库，无需外部依赖
- **`middleware.IPFilter`**：IP 白名单/黑名单，支持动态更新和 CIDR 过滤
- **完善的文档**：重写了中间件使用场景和最佳实践

### 🔧 改进

- **`middleware/ipfilter.go`**：添加 `shouldSkipPath` 函数，支持路径跳过
- **配置模板清理**：移除 `lifecycle.go` 中的 auth/attempt/ratelimit 配置块
- **文档优化**：
  - README.md 移除 auth/ratelimit 相关配置和示例
  - middleware/README.md 重写使用场景，简化为 6 个生产场景
  - 移除 110+ 行的认证和限流文档

### 🗑️ 删除

- 删除 41 个文件，约 9,200 行代码
- 移除 Redis 依赖（从必需改为可选）
- 移除 JWT 相关依赖

### 📦 保留的中间件

- **IPFilter**：IP 白名单/黑名单，CIDR 支持
- **CORS**：跨域资源共享配置
- **Gzip**：响应压缩（70-90% 压缩率）
- **Security**：安全响应头（HSTS、CSP、XFO 等）
- **BodyLimit**：请求体大小限制

### 🎨 核心特性

- 🎯 **泛型绑定**：`Bind[Req, Resp]` 自动参数校验和响应
- 🛡️ **安全默认**：不信任代理、不暴露内部错误
- 📦 **依赖注入**：`Config` + `Option` 模式
- 🔭 **可观测性**：内置 RequestID/Tracing/Metrics/AccessLog
- 🧩 **生命周期管理**：优雅启动和关闭
- 📁 **静态文件服务**：`Static`/`StaticFS`/`StaticFile`

### 📊 统计

- **删除代码**：~9,200 行
- **新增代码**：~67 行
- **测试覆盖率**：保持在高水平
- **零外部依赖**：所有中间件

### 🚀 升级指南

如果你的项目使用了 Auth 或 RateLimit 功能：

1. **认证功能**：建议使用第三方 JWT 库（如 `github.com/golang-jwt/jwt`）
2. **限流功能**：建议使用专门的限流库（如 `golang.org/x/time/rate`）
3. **IP 过滤**：可以继续使用 `middleware.IPFilter`

### 📝 文档

- [README.md](./README.md) - 快速开始和核心概念
- [middleware/README.md](./middleware/README.md) - 中间件使用指南
- [DESIGN.md](./DESIGN.md) - 设计文档

### 🔗 链接

- **Repository**: https://github.com/surge-go/aurora
- **Issues**: https://github.com/surge-go/aurora/issues

---

## [0.0.9] - Previous Release

查看之前的版本历史，请参考 git 提交记录。

