package ratelimit

import "errors"

var (
	// ErrInvalidConfig 表示限流器配置无效。
	ErrInvalidConfig = errors.New("ratelimit: invalid config")

	// ErrRedisRequired 表示未提供 Redis 客户端。
	ErrRedisRequired = errors.New("ratelimit: redis client required")

	// ErrKeyRequired 表示限流 key 为空。
	ErrKeyRequired = errors.New("ratelimit: key is required")

	// ErrStoreUnavailable 表示 Redis 存储不可用。
	ErrStoreUnavailable = errors.New("ratelimit: store unavailable")
)
