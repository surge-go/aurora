# 登录尝试限制

`core/auth/attempt` 提供基于 Redis 的账号登录失败计数、临时锁定和人工解除能力。

它不校验用户名密码，不查询账号状态，不签发 Token，也不负责管理员权限和审计日志。
应用完成凭据校验后，应继续使用 `core/auth.Manager` 签发 Token。

## 线程安全

`Limiter` 实例可以安全地在多个 goroutine 间共享。每个 `Attempt` 句柄必须调用一次 `RecordFailure` 或 `RecordSuccess` 完成结算，之后句柄失效。并发结算同一句柄会导致其中一个返回 `ErrAttemptInvalidated`。

## 使用示例

```go
cfg := attempt.DefaultConfig()
cfg.HMACKey = os.Getenv("AUTH_ATTEMPT_HMAC_KEY")
cfg.KeyPrefix = "aurora:auth:attempt:{admin}"

limiter, err := attempt.New(rdb, cfg)
if err != nil {
	// 配置或 Redis client 错误
}

try, status, err := limiter.Begin(ctx, accountID)
if err != nil {
	// Redis 故障或账号标识无效
}
if status.Locked {
	// 对未登录客户端继续返回统一的登录失败响应
}

if !passwordMatches {
	status, err = limiter.RecordFailure(ctx, try)
	return
}
if err := limiter.RecordSuccess(ctx, try); err != nil {
	// ErrAttemptInvalidated 表示该尝试已被人工解锁或其他成功登录作废
}
```

管理员解锁应由业务层提供受保护的管理接口，并记录审计日志：

```go
result, err := limiter.Unlock(ctx, accountID)
```

`Unlock` 是幂等的，并会使解锁前创建的尝试句柄失效。它只清除登录失败限制，不会启用已禁用账号、重置密码或撤销已有 Token/Session。

## 配置约束

- `HMACKey` 必须至少 32 字节
- `FailureLimit` 必须在 1-100 之间
- `AttemptTTL` 不能超过 `Window`（防止句柄在窗口重置后仍有效）
- `LockDuration` 应不小于 `Window`（否则锁定可能在窗口内过期）
- `KeyPrefix` 必须包含 Redis hash tag（如 `{admin}`、`{user}`）用于多租户隔离

Redis key 中不保存账号明文，而是使用 `HMACKey` 生成摘要。每个 limiter 应使用独立的 hash-tag 前缀，例如 `aurora:auth:attempt:{admin}` 和 `aurora:auth:attempt:{user}`。
