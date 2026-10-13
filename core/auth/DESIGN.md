# core/auth JWT 认证设计

## 1. 设计范围

`core/auth` 为 Aurora 提供与 HTTP 框架和业务账号模型无关的认证 token 生命周期管理。
首期使用 `github.com/golang-jwt/jwt/v5` 生成和解析 access token、refresh token，
并使用注入的 Redis 客户端管理 session、refresh rotation 和吊销状态。

认证包只接收标准库 `context.Context`，不依赖 Gin、Aurora `Context`、数据库、logger
或业务模块。Gin/Aurora 中间件和账号密码登录流程由上层单独装配；Redis 是本包的必需运行时依赖。

首期核心流程：

- 签发 access token 和 refresh token。
- 校验 token 签名、标准 claims、token 用途、issuer、audience 和过期时间。
- 通过 Redis session 校验 access token 是否仍有效。
- 使用 refresh token 轮换新的 token pair。
- 原子消费旧 refresh token，防止并发刷新和 refresh 重放。
- 吊销 session 或主体下的全部 session。

## 2. 目标与非目标

### 目标

- 以稳定、通用的主体模型服务后台、前台和第三方登录场景。
- access token 和 refresh token 均使用 JWT v5 生成，明确区分 token 用途。
- 默认启用 refresh token rotation。
- access token 默认有状态校验，吊销 session 后立即失效。
- 将并发刷新的一致性责任放在 Redis Lua 原子操作中。
- 支持按主体和客户端平台限制并发 session，并支持平台互斥与可配置冲突策略。
- 保持配置可序列化，密钥、Redis client 和回调等运行时依赖通过构造参数或 Option 注入。
- 使用 sentinel error 让 HTTP 层和业务层可以稳定映射错误。

### 非目标

- 不校验用户名、密码、短信验证码或 OAuth provider 凭证。
- 不查询用户表、角色、权限、菜单或账号锁定状态。
- 不负责 IP 登录限流、密码错误计数或业务账号锁定。
- 不实现 Gin/Aurora middleware；HTTP 适配器另行设计。
- 不提供内存 store；Redis 是 session 和 refresh 状态的唯一存储。
- 不把角色权限默认写入 token；权限变化不应依赖等待 JWT 过期。
- 不在包内写审计日志、发送通知或维护 WebSocket 在线状态。
- 首期只支持 HMAC-SHA256；非对称密钥和密钥轮换作为后续扩展。

## 3. 包结构与依赖

```text
core/auth/
  doc.go          # 包边界和生命周期说明
  DESIGN.md       # 认证设计和契约
  config.go       # 配置、默认值和校验
  types.go        # Principal、Claims、TokenPair、Session
  manager.go      # Issue、Verify、Refresh、Revoke
  token.go        # jwt/v5 parser、signer 和 claims 校验
  redis.go        # Redis key、Lua 脚本和状态操作
  errors.go       # 包级 sentinel errors
  *_test.go
```

依赖方向：

```text
application bootstrap
        |
        v
core/auth.Manager ----> jwt/v5
        |
        +----> go-redis UniversalClient
                         ^
                         |
                  core/redis.NewClient
```

`core/auth` 不创建、不关闭 Redis 客户端。应用组合根使用 `core/redis` 创建客户端，再注入
`auth.New`；认证包只负责使用 Redis 保存和清理自身 key。这样不需要额外维护内存 token
过期任务，也不会改变现有 Redis 生命周期所有权。

首期新增依赖：

```text
github.com/golang-jwt/jwt/v5
```

## 4. 核心模型

### Principal

认证主体使用字符串 ID 和可选维度，避免绑定某一种用户表或整数主键。上层完成账号认证后，
将主体交给本包签发和管理 token；本包不验证账号凭证：

```go
type Principal struct {
    Subject  string // 对应 JWT sub，必填
    Type     string // admin、member、service 等业务类型，可选
    TenantID string // 租户维度，可选
    Provider string // local、oauth、wechat 等来源，可选
}
```

`Principal` 只表达“谁”，不表达“当前拥有哪些权限”。业务层应根据 Claims.Subject
重新查询当前权限。

### Claims

JWT 使用 `jwt.RegisteredClaims`，自定义字段只保留认证生命周期需要的内容：

```go
type Claims struct {
    jwt.RegisteredClaims
    TokenUse   string `json:"token_use"`
    SessionID  string `json:"sid"`
    PrincipalType string `json:"principal_type,omitempty"`
    TenantID   string `json:"tenant_id,omitempty"`
    Provider   string `json:"provider,omitempty"`
    Platform   string `json:"platform,omitempty"`
    AuthVersion int64 `json:"auth_version,omitempty"`
}
```

约定：

- `sub` 保存 `Principal.Subject`。
- `jti` 每次签发都必须随机且唯一。
- `sid` 标识一次登录 session；同一 session 可以经历多轮 refresh。
- `token_use` 只能是 `access` 或 `refresh`。
- access token 和 refresh token 使用不同 TTL、不同用途校验。
- `iss`、`aud`、`iat`、`exp` 由 `jwt.RegisteredClaims` 管理。
- `roles`、`permissions` 等业务授权信息不放入默认 claims。

### TokenPair

```go
type TokenPair struct {
    TokenType        string    `json:"token_type"`
    AccessToken      string    `json:"access_token"`
    RefreshToken     string    `json:"refresh_token"`
    AccessExpiresAt  time.Time `json:"access_expires_at"`
    RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}
```

`TokenType` 固定为 `Bearer`。HTTP 层决定 token 放入响应体、Header 或 Cookie，认证包不
规定传输方式。

### Session

```go
type Session struct {
    ID                  string
    Principal           Principal
    AuthVersion         int64
    CurrentRefreshHash  string
    IssuedAt            time.Time
    LastRefreshedAt     time.Time
    ExpiresAt           time.Time
    AbsoluteExpiresAt   time.Time
    RevokedAt           time.Time
    Platform            string
    DeviceID            string
}
```

存储层只保存 refresh token 的 HMAC-SHA256 hash，不保存 access/refresh 明文。`CurrentRefreshHash`
用于原子校验和 rotation；`RevokedAt` 非零表示 session 已失效。

## 5. 配置与构造

可序列化配置：

```go
type Config struct {
    KeyPrefix       string        `json:"key_prefix" yaml:"key_prefix" mapstructure:"key_prefix"`
    SigningSecret   string        `json:"signing_secret" yaml:"signing_secret" mapstructure:"signing_secret"`
    Issuer          string        `json:"issuer" yaml:"issuer" mapstructure:"issuer"`
    Audience        []string      `json:"audience" yaml:"audience" mapstructure:"audience"`
    AccessTTL       time.Duration `json:"access_ttl" yaml:"access_ttl" mapstructure:"access_ttl"`
    RefreshTTL      time.Duration `json:"refresh_ttl" yaml:"refresh_ttl" mapstructure:"refresh_ttl"`
    MaxSessionTTL   time.Duration `json:"max_session_ttl" yaml:"max_session_ttl" mapstructure:"max_session_ttl"`
    ClockSkew       time.Duration `json:"clock_skew" yaml:"clock_skew" mapstructure:"clock_skew"`
    RefreshCompensationTimeout time.Duration `json:"refresh_compensation_timeout" yaml:"refresh_compensation_timeout" mapstructure:"refresh_compensation_timeout"`
    RevokeBatchSize int           `json:"revoke_batch_size" yaml:"revoke_batch_size" mapstructure:"revoke_batch_size"`
    IdempotencyTTL  time.Duration `json:"idempotency_ttl" yaml:"idempotency_ttl" mapstructure:"idempotency_ttl"`
    Policy          SessionPolicy `json:"policy" yaml:"policy" mapstructure:"policy"`
}
```

默认值和必填项：

```text
AccessTTL     15m
RefreshTTL    168h
MaxSessionTTL 无默认值，必须显式配置且至少为 1s
ClockSkew     0
RevokeBatchSize 100，范围为 1-100
IdempotencyTTL  10s，用于登录幂等回放
RefreshCompensationTimeout 5s，用于限制 refresh validator 失败后的补偿吊销，最大 1m
```

`AccessTTL`、`RefreshTTL` 和 `MaxSessionTTL` 必须至少为 `1s`，因为 JWT 的 NumericDate 和
Redis 的 `EXPIRE` 都按秒保存。`MaxSessionTTL` 的零值和负值都必须被视为配置错误；不能把零解释为“不限制”。这样即使
调用方持续使用合法的 refresh token，也只能在绝对过期时间前续期。配置文件示例：

```yaml
auth:
  key_prefix: aurora:auth:{auth}
  access_ttl: 15m
  refresh_ttl: 168h
  max_session_ttl: 720h # 必填，至少为 1s
  revoke_batch_size: 100
  idempotency_ttl: 10s
```

实现应在配置解析阶段拒绝缺失、小于 `1s` 和无法解析的 `max_session_ttl`，而不是等到
签发或 refresh 时才失败。`RevokeBatchSize` 是单次分页操作的上限，取值必须为 `1-100`。

签名密钥现在从 `signing_secret` 配置字段读取，配置文件必须使用安全的文件权限并由部署系统
保护。Redis client 等运行时依赖不放入配置文件。`key_prefix` 可以放入配置文件；如果同时
传入 `WithSigningSecret` 或 `WithKeyPrefix`，Option 会覆盖配置中的对应值：

```go
import "github.com/redis/go-redis/v9"

func New(rdb redis.UniversalClient, cfg Config, opts ...Option) (*Manager, error)

func WithSigningSecret(secret []byte) Option
func WithKeyPrefix(prefix string) Option
```

`New` 必须拒绝：

- `signing_secret` 为空且没有通过 `WithSigningSecret` 提供密钥，或密钥少于 32 字节。
- access/refresh/session TTL 小于 `1s`，或 refresh TTL 小于 access TTL。
- `MaxSessionTTL` 小于 `1s`；Aurora 不支持无限期 refresh session。
- `ClockSkew` 小于零或超过合理上限。
- `RevokeBatchSize` 小于等于零或超过 `100`。
- Redis client 为空。
- `key_prefix` 为空时使用 `aurora:auth:{auth}`；自定义前缀必须包含一个非空 Redis hash tag。
- issuer、audience 中存在空值。

首期固定 `HS256`，解析时使用 `jwt.WithValidMethods([]string{"HS256"})`，禁止根据 token
header 动态选择算法。后续支持 RS256/EdDSA 时必须通过显式 key provider 和算法白名单扩展。

session 的绝对过期时间固定为 `AbsoluteExpiresAt = IssuedAt + MaxSessionTTL`；每次 refresh
最多将 `ExpiresAt` 延长到 `min(now + RefreshTTL, AbsoluteExpiresAt)`。access token 的过期时间
为 `min(now + AccessTTL, ExpiresAt)`。因此 refresh 永远不能突破 `MaxSessionTTL`。

## 6. Redis 状态与原子操作

JWT 本身不能表达吊销、rotation 和并发消费状态，因此 Redis 是认证生命周期的权威来源。
认证包使用 Redis TTL 自动清理过期状态，不启动后台过期删除 goroutine。

默认 key 前缀使用 `aurora:auth:{auth}`。自定义前缀必须包含稳定的 Redis hash tag，包在
构造时校验并拒绝无法保证同一 Cluster slot 的前缀。主要 key 家族如下：

```text
<prefix>:session:<session_id>
<prefix>:subject_sessions:<subject_key>
<prefix>:platform_sessions:<subject_key>:<platform>
<prefix>:device_sessions:<subject_key>:<device_id>
<prefix>:session_refresh:<session_id>
<prefix>:idempotency:<request_digest>
```

`session` 使用 Redis Hash 保存 Session 字段；`session_refresh` 保存当前 refresh hash；其余索引保存
主体、平台和设备对应的 session ID，用于登录互斥和吊销。索引使用按签发时间排序的 ZSET，
member 为 session ID、score 为签发时间；索引 TTL 随 session TTL 延长。Redis 中不保存任何
明文 token。登录幂等记录只在短 TTL 内保存 AES-GCM 加密的 token pair，并绑定主体、平台、
设备和请求参数指纹。session 过期时，Redis 自动删除 session 状态；Issue、Refresh 和 RevokeSubject
在访问索引时应清理已经不存在的 session member。

必须使用 Lua 原子完成以下操作：

- Issue：读取主体索引中的 session metadata，计算平台和互斥冲突，按策略原子拒绝或吊销旧
  session，然后写 session、当前 refresh hash 以及主体、平台和设备索引并设置 TTL；如果
  `IdempotencyKey` 已存在，则原子回放首次 token pair 或返回幂等冲突。
- Refresh：校验 session、主体和旧 hash，替换 refresh hash、更新 session TTL 和索引 TTL，
  并重新写入主体、平台和设备索引 member，以便在索引被清理或过期后自动修复。
- RevokeSession：删除 session、session_refresh 和主体索引。
- RevokeSubjectPage：按签发时间从旧到新最多扫描并清理指定数量的索引 member，删除对应的
  session、refresh 状态和索引成员，并返回是否还有下一页。

Refresh rotation 不采用“先 GET、再 SET”的多次调用，而是在单个 Lua 脚本中完成状态校验和
更新。旧 JWT 已通过签名、session ID 和主体校验后，如果 Redis 中当前 hash 与旧 hash 不匹配，返回 `ErrRefreshTokenReused`；
并发请求中只能有一个 Lua 调用成功。

Redis 错误必须映射为 `ErrStoreUnavailable`，认证请求不能在 Redis 不可用时降级放行。

当全局 session 上限、平台上限和平台互斥规则都未配置时，Issue 不执行无界的主体索引扫描；
索引仍由 TTL 自动清理，并在 Refresh 或吊销流程中按需修复和清理。

## 7. Manager API

```go
type RefreshValidator func(context.Context, Principal) error
```

登录并发策略由 `Config` 提供，不绑定 admin/member 等业务类型：

```go
type SessionPolicy struct {
    MaxSessions           int
    DefaultPlatformPolicy PlatformPolicy
    PlatformPolicies      map[string]PlatformPolicy
}

type PlatformPolicy struct {
    Enabled         bool
    MaxSessions     int
    RequireDeviceID bool
    KickoutStrategy KickoutStrategy
    ExclusiveWith   []string
}

type KickoutStrategy string

const (
    KickoutOldest KickoutStrategy = "oldest"
    KickoutLatest KickoutStrategy = "latest"
    KickoutAll    KickoutStrategy = "all"
)
```

策略语义：

- `MaxSessions=0` 表示不限制；负数配置无效。
- 平台级策略可以限制单个平台 session 数、禁用平台或要求设备 ID。
- `ExclusiveWith` 声明当前平台登录时需要互斥吊销的平台，例如 Android 与 iOS。
- `KickoutOldest` 踢掉最早的冲突 session；`KickoutAll` 踢掉所有命中的冲突 session。
- `KickoutLatest` 默认拒绝本次登录并返回 `ErrLoginConflict`；请求方在完成用户确认后可以
  显式设置 `ForceLogin` 覆盖旧 session。
- 冲突收集、策略判定、旧 session/refresh 清理、新 session 创建和索引更新必须放在同一个
  Lua 脚本中，避免并发登录突破 session 上限。
- 同一主体的索引 key 必须共享 Redis Cluster hash tag。平台互斥是有方向的配置；若要求
  Android 和 iOS 双向互斥，两个平台策略都要声明对方。

主体索引按完整 `Principal` 隔离。建议规范化后的主体 key 由 `Type + TenantID + Subject + Provider`
组成，避免不同租户、主体类型或认证来源之间互相踢下线。平台与设备 ID 需校验长度和字符集，
不能直接把不受限的请求值拼入 Redis key。

```go
type Manager struct{}

func (m *Manager) Issue(context.Context, IssueRequest) (*TokenPair, error)
func (m *Manager) VerifyAccess(context.Context, string) (*Claims, error)
func (m *Manager) VerifyAccessToken(string) (*Claims, error)
func (m *Manager) Refresh(context.Context, string, RefreshValidator) (*TokenPair, error)
func (m *Manager) RevokeSession(context.Context, string) error
func (m *Manager) RevokeSubject(context.Context, Principal) error
func (m *Manager) RevokeSubjectPage(context.Context, Principal, int) (RevokePage, error)
```

```go
type RevokePage struct {
    Revoked  int
    HasMore  bool
}
```

`RevokeSubjectPage` 的 `limit` 必须大于零且不超过配置中的 `RevokeBatchSize`；该配置默认
为 `100`，实现可以下调但不能超过 `100`。单次 Lua 执行最多读取和处理 `limit` 个 session
索引 member，不会因为某个主体拥有大量 session 而长时间阻塞 Redis。这里的 `limit` 同时限制本次 Lua
脚本读取和删除的索引 member 数量；已经过期但仍残留在 ZSET 中的 member 也计入该上限，脚本
应在清理时一并移除。`Revoked` 只统计实际删除的有效 session，`HasMore` 表示索引清理后
是否仍有 member 待处理。

这是基于删除已处理 member 的无 cursor 分页：每次调用都从当前索引的最旧 member 开始，成功
处理的 member 不会出现在下一页，因此调用方只需保存 `HasMore`，不需要保存或信任一个可能
过期的 Redis cursor。

调用方可以根据 `HasMore` 重复调用；`RevokeSubject` 是便利入口，使用默认批量大小循环调用
page API，且每轮都沿用调用方 context。该入口必须由调用方设置 deadline 或 cancellation；如果
分页期间持续创建新 session，操作可能持续返回 `HasMore=true`，直到登录被禁止、索引清空或
context 结束。需要严格冻结吊销范围的管理操作应先禁止该主体登录，或由业务层提供额外的
吊销版本号。

`IssueRequest`：

```go
type IssueRequest struct {
    Principal   Principal
    AuthVersion int64
    Platform    string
    DeviceID    string
    ForceLogin  bool
    IdempotencyKey string
}
```

`Issue` 流程：

1. 校验主体和认证版本。
2. 校验可选的 `IdempotencyKey`，并计算主体、平台、设备和请求参数指纹。
3. 生成 session ID、access JTI、refresh JTI。
4. 计算 session、access、refresh 的过期时间，并应用 `MaxSessionTTL` 上限。
5. 使用 HS256 签发两个不同 `token_use` 的 JWT。
6. 调用原子 Issue Lua 脚本；相同幂等键回放首次结果，不同参数返回 `ErrIdempotencyConflict`，否则处理冲突、创建 session 并更新索引。
7. 返回 `TokenPair`。

如果 token 签发失败，不能留下不可用 session；应先生成并签名 token，再提交 session，或
在失败路径执行补偿删除。

`VerifyAccessToken` 只做纯 JWT 校验：签名、算法、issuer、audience、token_use、claims
完整性和时间窗口。它不访问 Redis，适合已经完成 rotation 后解析新 access token。

`VerifyAccess` 还必须：

1. 读取 `sid` 对应 session。
2. 检查 session 未吊销且未过期。
3. 比较 `sub`、主体类型、租户、Provider、AuthVersion 和 session ID。
4. Redis 不可用时返回存储错误，不能降级为只信任 JWT。

`Refresh` 流程：

1. 解析 JWT，并确认 `token_use=refresh`。
2. 校验签名、issuer、audience、session ID、subject 和过期时间。
3. 读取 session，计算旧 refresh token hash。
4. 调用调用方提供的可选 `RefreshValidator`，由业务层检查账号是否仍可登录。
5. 生成新 access/refresh token，并通过 Lua 原子替换旧 hash。
6. rotation 成功后再次执行 validator；如果状态已变化，吊销 session 并返回验证错误。
7. 旧 refresh token 再次使用返回 `ErrRefreshTokenReused`。

refresh rotation 首期不实现 grace retry；中间件只在单进程内使用 `singleflight` 合并并发刷新。
token 签发通过 `IssueRequest.IdempotencyKey` 支持短期幂等回放，默认 TTL 为 10 秒；幂等键只
应在同一次 token 签发操作的网络重试中复用，新的主动签发必须生成新键。

登录互斥策略只管理成功登录后的 session 数量和平台冲突，不等价于登录保护。IP 请求速率、
密码失败计数、验证码和账号锁定属于登录入口或业务认证服务职责，可由独立 limiter 实现，
不写入 `core/auth` 的 session 策略。

## 8. 错误契约

建议提供以下 sentinel errors：

```go
ErrInvalidConfig
ErrSigningKeyRequired
ErrRedisRequired
ErrTokenRequired
ErrTokenMalformed
ErrTokenExpired
ErrTokenUseMismatch
ErrInvalidSignature
ErrSessionNotFound
ErrSessionExpired
ErrSessionRevoked
ErrRefreshTokenInvalid
ErrRefreshTokenReused
ErrStoreUnavailable
ErrRefreshValidation
```

对外错误不得包含签名密钥、完整 token、refresh hash、Redis key 或底层敏感配置。业务层使用
`errors.Is`/`errors.As` 做 HTTP 状态和业务码映射。

## 9. 安全约定

- HS256 密钥最少 32 字节，生产环境应保护配置文件或使用 `WithSigningSecret` 注入。
- 解析时固定允许算法，不能信任 JWT header 的 `alg`。
- access 和 refresh 必须校验不同的 `token_use`，防止 refresh token 被当作 access token 使用。
- refresh token 必须 rotation；旧 token 使用一次后失效。
- access token 使用短 TTL，并通过 Redis session 校验以支持即时吊销。
- 不把密码、完整 token、refresh hash、Cookie 或 Authorization Header 写入日志、trace 或 metric。
- `sub`、tenant 和 provider 只用于识别主体，不自动代表权限。
- token 中的角色权限是可选扩展，不作为长期授权缓存。
- 所有 Redis 操作必须使用调用方 context，并由 HTTP/任务入口设置 deadline。
- Redis 中的 session 和 refresh 状态必须支持原子更新，避免并发 refresh 签发多个有效 token。

## 10. HTTP/Aurora 集成边界

`core/auth` 不读取 Header，也不写响应。当前由根目录独立 `middleware` 包提供 Aurora HTTP 适配：

```go
func AuthMiddleware(manager *auth.Manager, opts ...Option) aurora.Middleware
```

中间件职责：

- 读取 `Authorization: Bearer <access-token>`。
- 调用 `VerifyAccess`。
- 将 `*auth.Claims` 放入 Aurora request context。
- 把认证错误映射为 `aurora.ErrUnauthorized` 或业务错误。
- access token 过期时读取独立的 refresh token，并调用 `Refresh` 完成 rotation。
- 可选地配置预刷新窗口；窗口内 refresh 失败不能阻断仍然有效的 access token。
- 刷新成功后通过响应头返回新的 access/refresh token，并继续执行当前请求。
- access token 已过期时刷新失败必须终止请求；并发 refresh 造成的旧 token 重放也必须终止该请求。

中间件可以在单进程内使用 `singleflight` 合并相同 refresh token 的并发刷新，减少同一实例
的重复 Redis 请求；这只是性能优化，不能替代 `Manager.Refresh` 内置的 Redis Lua 原子
rotation，因为多实例部署仍可能同时收到同一个 refresh token。

建议的 HTTP 约定是使用 `X-Refresh-Token` 请求头，以及 `X-Access-Token` 和
`X-Refresh-Token` 响应头。预刷新默认关闭，由 HTTP 层按客户端行为设置窗口；跨域部署时，
响应头还必须加入 CORS 的 `Access-Control-Expose-Headers`。

业务 Handler 仍负责根据 Claims.Subject 查询用户、权限和业务状态；登录凭证校验、登录路由和
业务状态检查不属于本包。

## 11. 测试与验收

### 配置和 token

- 配置默认值、非法 TTL、短密钥、空 Redis client。
- HS256 签发和解析。
- 错误算法、错误签名、错误 issuer/audience。
- access token 被当作 refresh token 使用，反之亦然。
- 过期、时钟偏差和缺少必要 claims。

### 生命周期

- Issue 后 VerifyAccess 成功。
- RevokeSession 后 VerifyAccess 立即失败。
- Refresh 成功产生新 token pair。
- 旧 refresh token 重放失败。
- RefreshValidator 前置失败不消费 refresh token。
- rotation 后 validator 失败会补偿吊销 session。
- RefreshTTL、MaxSessionTTL 和绝对过期边界。
- MaxSessions、平台级上限、平台禁用、设备 ID 必填和平台互斥。
- KickoutOldest、KickoutAll、KickoutLatest 拒绝和 ForceLogin 覆盖。
- 并发 Issue 不得突破主体或平台 session 上限。
- 相同 `IdempotencyKey` 的并发 Issue 只能创建一个 session 并回放同一 token pair。
- 相同幂等键但参数指纹不同必须返回 `ErrIdempotencyConflict`。
- 主体类型、租户、Provider 隔离的 session 不互相吊销。
- RevokeSubjectPage 的批量上限、分页结果和大主体不会阻塞 Redis。

### 并发与 Redis

- 同一 refresh token 并发调用只能有一个 rotation 成功。
- Redis Lua 脚本的原子性测试。
- Redis 不可用时 VerifyAccess 失败关闭。
- context 取消和 timeout 能终止存储操作。
- middleware 在 access 过期、预刷新失败、session 吊销和 refresh 重放时返回正确结果。
- RevokeSubject 不影响其他主体。

## 12. 实施阶段

### 第一阶段

1. 添加 `github.com/golang-jwt/jwt/v5`。
2. 实现 `core/auth` 的 Config、Claims、Manager、Redis Lua 脚本和错误。
3. 使用 miniredis 或测试 Redis 覆盖 token、rotation、吊销和并发测试。

### 第二阶段

1. 将应用组合根中由 `core/redis` 创建的客户端注入 `core/auth.New`，并纳入统一配置与生命周期
   管理；`core/auth` 本身不拥有该客户端。
2. 增加 Redis 集群 hash tag、TTL 边界、批量分页和失败关闭测试。
3. 增加 Redis/miniredis 集成测试和 race 测试。

### 第三阶段

1. 已实现 Aurora middleware 适配器，支持 access 校验、过期刷新和预刷新。
2. 已接入统一错误、request context、刷新响应 Header 和单进程 singleflight 合并。
3. 再评估 RS256/EdDSA、kid 密钥轮换和 refresh grace retry。

## 13. 待确认决策

- 首期强制注入 Redis client；纯 JWT access-only 模式不属于首期生命周期管理能力。
- `Principal.Subject` 是否统一使用字符串，还是提供整数 ID 的辅助构造函数。
- refresh validator 轮换后的第二次校验是否需要拆成独立策略。
- 是否需要为 session 吊销提供调用方事件回调。
- Redis key 前缀和主体 session 索引是否需要纳入 Aurora 默认配置模板。
- 是否需要将认证事件作为独立接口加入首期 API。
