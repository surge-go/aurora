package auth

import "errors"

// 本包暴露的稳定错误集合。调用方使用 errors.Is 判断类别，
// 错误消息文本不作为 API 约定。
var (
	ErrInvalidConfig         = errors.New("auth: invalid config")            // 配置不合法
	ErrSigningKeyRequired    = errors.New("auth: signing key required")      // 签名密钥缺失或长度不足
	ErrRedisRequired         = errors.New("auth: redis client required")     // 未注入 Redis 客户端
	ErrTokenRequired         = errors.New("auth: token required")            // 请求未携带 token
	ErrTokenMalformed        = errors.New("auth: malformed token")           // token 结构或声明不合法
	ErrTokenExpired          = errors.New("auth: token expired")             // token 已过期
	ErrTokenUseMismatch      = errors.New("auth: token use mismatch")        // access/refresh token 用途错误
	ErrInvalidSignature      = errors.New("auth: invalid signature")         // 签名校验失败
	ErrInvalidIssuer         = errors.New("auth: invalid issuer")            // issuer 不匹配
	ErrInvalidAudience       = errors.New("auth: invalid audience")          // audience 不匹配
	ErrSessionNotFound       = errors.New("auth: session not found")         // 会话不存在或已被清理
	ErrSessionExpired        = errors.New("auth: session expired")           // 会话已过期
	ErrSessionRevoked        = errors.New("auth: session revoked")           // 会话被显式撤销
	ErrSessionMismatch       = errors.New("auth: session mismatch")          // token 与会话状态不一致
	ErrRefreshTokenInvalid   = errors.New("auth: invalid refresh token")     // 非 refresh token 用于刷新
	ErrRefreshTokenReused    = errors.New("auth: refresh token reused")      // refresh token 被重放
	ErrStoreUnavailable      = errors.New("auth: store unavailable")         // Redis 不可用或脚本异常
	ErrRefreshValidation     = errors.New("auth: refresh validation failed") // 业务侧 refresh 校验失败
	ErrLoginConflict         = errors.New("auth: login conflict")            // 与现有会话冲突且策略为 latest
	ErrPlatformDisabled      = errors.New("auth: platform disabled")         // 该平台被禁用
	ErrDeviceRequired        = errors.New("auth: device id required")        // 策略要求设备 ID 但请求未提供
	ErrInvalidPrincipal      = errors.New("auth: invalid principal")         // Principal 字段不合法
	ErrInvalidBatchSize      = errors.New("auth: invalid revoke batch size") // 撤销分页大小越界
	ErrInvalidIdempotencyKey = errors.New("auth: invalid idempotency key")   // 幂等键格式不合法
	ErrIdempotencyConflict   = errors.New("auth: idempotency key conflict")  // 相同幂等键但参数不一致
)
