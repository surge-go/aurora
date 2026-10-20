# Aurora Auth

`core/auth` 提供 JWT access token、refresh token 和 Redis session 的生命周期管理。
它只负责 token 认证与会话状态，不负责登录凭证、权限、HTTP Header 或 HTTP 响应。

## 职责边界

本包负责：

- 签发和校验 access token / refresh token；
- Redis session 状态、refresh rotation 和吊销；
- 并发 refresh 防重放；
- 登录幂等键和 session 数量、平台策略；
- 主体或单个 session 的吊销。

调用方负责：

- 用户名密码、验证码和 OAuth 凭证校验；
- 登录接口、HTTP 中间件和响应格式；
- 权限查询、账号锁定、登录限流和审计。

Redis 客户端由应用创建并注入，`auth.Manager` 不创建或关闭客户端。

## 初始化

```go
import (
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/surge-go/aurora/core/auth"
)

cfg := auth.DefaultConfig()
cfg.Issuer = "aurora"
cfg.Audience = []string{"aurora-client"}
cfg.MaxSessionTTL = 30 * 24 * time.Hour

rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
manager, err := auth.New(
	rdb,
	cfg,
	auth.WithSigningSecret([]byte("at-least-32-random-bytes")),
)
if err != nil {
	// 处理配置、密钥或 Redis client 错误
}
```

生产环境应使用随机且至少 32 字节的签名密钥，并通过安全配置或密钥管理系统注入。
应用仍然拥有 `rdb` 的关闭责任。

## 配置

```yaml
auth:
  admin:
    key_prefix: aurora:auth:{admin}
    signing_secret: replace-with-at-least-32-random-bytes
    issuer: aurora
    audience: [aurora-client]
    access_ttl: 15m
    refresh_ttl: 168h
    max_session_ttl: 720h
    clock_skew: 0s
    refresh_compensation_timeout: 5s
    revoke_batch_size: 100
    idempotency_ttl: 10s
```

以上是 Lifecycle 的命名实例配置示例，通过 `lifecycle.Auth("admin")` 获取 Manager。
每个 `auth.<name>` 节点对应独立的 `auth.Config`；直接调用 `auth.New` 时传入该节点解码后的配置即可。
Lifecycle 未指定 `key_prefix` 时按名称生成 `aurora:auth:{<name>}`，并拒绝实例间前缀冲突。
直接调用 `auth.New` 的默认前缀仍为 `aurora:auth:{auth}`，手动创建多个实例时应分别配置前缀。

主要默认值和限制：

- `AccessTTL` 默认 `15m`；`RefreshTTL` 默认 `168h`；两者至少为 `1s`，且 refresh 不能短于 access。
- `MaxSessionTTL` 没有默认值，必须显式设置且至少为 `1s`。
- `RefreshCompensationTimeout` 默认 `5s`，范围为 `1s` 到 `1m`。
- `RevokeBatchSize` 默认 `100`，范围为 `1-100`。
- `IdempotencyTTL` 默认 `10s`。
- `KeyPrefix` 默认 `aurora:auth:{auth}`；自定义前缀必须包含非空 Redis hash tag。

Session 的绝对过期时间为 `IssuedAt + MaxSessionTTL`。后续 refresh 不能突破该时间。

## 签发 Token

上层完成账号认证后，将主体交给 `Issue`：

```go
pair, err := manager.Issue(ctx, auth.IssueRequest{
	Principal: auth.Principal{
		Subject:  "user-123",
		Type:     "member",
		TenantID: "tenant-a",
		Provider: "local",
	},
	Platform: "web",
	DeviceID: "browser-1",
})
if err != nil {
	// 处理 ErrLoginConflict、ErrPlatformDisabled 等错误
}

// pair.AccessToken、pair.RefreshToken
```

`TokenPair.TokenType` 固定为 `Bearer`。access token 和 refresh token 使用不同的
`token_use`，并分别校验 issuer、audience、签名、issued-at 和过期时间。

### 登录幂等

网络重试或重复点击登录时，为同一次逻辑登录请求复用同一个 `IdempotencyKey`：

```go
pair, err := manager.Issue(ctx, auth.IssueRequest{
	Principal:      auth.Principal{Subject: "user-123"},
	Platform:       "web",
	IdempotencyKey: requestID,
})
```

相同主体、平台、设备和请求参数会在 `IdempotencyTTL` 内回放同一个 token pair；参数不同
返回 `ErrIdempotencyConflict`。新的主动登录必须生成新的幂等键。幂等记录在 Redis 中使用
AES-GCM 加密保存，Redis 不保存明文 token。

## 校验 Access Token

`VerifyAccessToken` 只校验 JWT 本身，不访问 Redis；适合只需要解析 token 的内部场景。
HTTP 请求认证通常应使用 `VerifyAccess`，因为它还会检查 session 是否存在、未过期、未吊销，
以及主体、租户、Provider、平台和认证版本是否匹配：

```go
claims, err := manager.VerifyAccess(ctx, rawAccessToken)
if err != nil {
	// errors.Is(err, auth.ErrTokenExpired) 等
	return err
}

subject := claims.Subject
sessionID := claims.SessionID
```

Redis 不可用时返回 `ErrStoreUnavailable`，调用方不能降级为只信任 JWT。

## Refresh Rotation

refresh 时旧 token 只允许成功消费一次：

```go
pair, err := manager.Refresh(ctx, rawRefreshToken, func(ctx context.Context, p auth.Principal) error {
	// 可选：重新检查账号是否仍允许登录
	return nil
})
```

成功后返回新的 access/refresh token，旧 refresh token 立即失效。并发使用同一个旧 token
时，Redis Lua rotation 只允许一个请求成功，其余请求返回 `ErrRefreshTokenReused`。

`RefreshValidator` 会在 rotation 前和 rotation 后各执行一次：

- 前置校验失败不会消费旧 refresh token；
- rotation 已提交后，后置校验失败会吊销整个 session；
- 补偿吊销使用 `RefreshCompensationTimeout` 限时，不会无限等待 Redis。

## 吊销 Session

吊销单个 session：

```go
err := manager.RevokeSession(ctx, sessionID)
```

吊销主体下全部 session：

```go
err := manager.RevokeSubject(ctx, auth.Principal{
	Subject: "user-123",
	TenantID: "tenant-a",
	Type:     "member",
	Provider: "local",
})
```

大主体可以使用分页接口控制单次 Redis Lua 操作规模：

```go
page, err := manager.RevokeSubjectPage(ctx, principal, 100)
if err == nil && page.HasMore {
	// 继续调用 RevokeSubjectPage
}
```

主体由 `Type`、`TenantID`、`Subject` 和 `Provider` 共同隔离；这些字段不同的主体不会互相
吊销 session。

## Session 策略

`Config.Policy` 可以限制主体或平台的 session 数量：

```go
cfg.Policy.MaxSessions = 3
cfg.Policy.PlatformPolicies = map[string]auth.PlatformPolicy{
	"web": {
		Enabled:         true,
		MaxSessions:     1,
		KickoutStrategy: auth.KickoutLatest,
	},
}
```

支持 `KickoutOldest`、`KickoutLatest` 和 `KickoutAll`。`KickoutLatest` 默认拒绝冲突登录，
调用方确认后可在 `IssueRequest.ForceLogin` 中显式覆盖。`ExclusiveWith` 可声明平台互斥，
`RequireDeviceID` 可要求调用方提供设备 ID。

## 错误处理

错误通过 sentinel error 暴露，使用 `errors.Is` 判断：

```go
if errors.Is(err, auth.ErrTokenExpired) {
	// access token 过期，可尝试使用 refresh token
}
if errors.Is(err, auth.ErrStoreUnavailable) {
	// Redis 故障，通常映射为服务暂不可用
}
```

常见错误包括：`ErrTokenMalformed`、`ErrInvalidSignature`、`ErrTokenUseMismatch`、
`ErrSessionNotFound`、`ErrSessionRevoked`、`ErrRefreshTokenReused`、`ErrLoginConflict`、
`ErrIdempotencyConflict` 和 `ErrStoreUnavailable`。

## 安全与部署

- 固定使用 HS256，不接受 token header 动态指定算法。
- 不在日志、trace 或 metric 中记录完整 token、Authorization Header 或 refresh hash。
- Redis Cluster、Sentinel 和 Standalone 均通过注入的 `redis.UniversalClient` 使用；自定义 key
  前缀必须保留 hash tag，保证相关 Key 位于同一 Cluster slot。
- 所有公开方法都接收 `context.Context`，调用方应设置请求或任务超时。
- 本包不提供内存存储；Redis 是 session 和 refresh 状态的唯一存储。

更完整的状态转换、Redis Key 结构和 Lua 原子操作说明见 [`DESIGN.md`](DESIGN.md)。
