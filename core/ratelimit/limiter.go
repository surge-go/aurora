package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Limiter 提供基于 Redis 的分布式滑动窗口限流能力。
// 线程安全，可以在多个 goroutine 间共享使用。
type Limiter struct {
	rdb       redis.UniversalClient
	prefix    string
	hmacKey   []byte
	limit     int
	windowSec int64
}

// New 创建限流器实例。
func New(rdb redis.UniversalClient, cfg Config) (*Limiter, error) {
	if isNilUniversalClient(rdb) {
		return nil, ErrRedisRequired
	}

	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &Limiter{
		rdb:       rdb,
		prefix:    cfg.KeyPrefix,
		hmacKey:   []byte(cfg.HMACKey),
		limit:     cfg.Limit,
		windowSec: int64(cfg.Window / time.Second),
	}, nil
}

// Allow 检查是否允许请求通过。
// key 是限流维度标识（如 IP 地址、用户 ID）。
//
// 返回值:
//   - allowed: 是否允许请求通过
//   - remaining: 剩余配额（0 表示已耗尽）
//   - retryAfter: 下次可用时间（仅 allowed=false 时有意义）
//   - err: 错误（Redis 故障、配置错误等）
func (l *Limiter) Allow(ctx context.Context, key string) (allowed bool, remaining int, retryAfter time.Duration, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, 0, 0, ErrKeyRequired
	}

	hash, err := l.keyHash(key)
	if err != nil {
		return false, 0, 0, err
	}

	stateKey := l.stateKey(hash)
	reqID := uuid.NewString()

	result, err := allowScript.Run(ctx, l.rdb, []string{stateKey},
		time.Now().UnixMilli(),        // 毫秒时间戳
		l.windowSec*1000,              // 窗口转为毫秒
		l.limit,
		reqID,
	).Result()

	if err != nil {
		return false, 0, 0, storeError(err)
	}

	parts, err := resultStrings(result)
	if err != nil {
		return false, 0, 0, fmt.Errorf("%w: invalid script result: %w", ErrStoreUnavailable, err)
	}

	if len(parts) < 3 {
		return false, 0, 0, fmt.Errorf("%w: invalid script result format", ErrStoreUnavailable)
	}

	status := parts[0]
	remainingStr := parts[1]
	retryAfterStr := parts[2]

	remainingVal, _ := strconv.Atoi(remainingStr)
	retryAfterSec, _ := strconv.ParseInt(retryAfterStr, 10, 64)

	if status == "ALLOWED" {
		return true, remainingVal, 0, nil
	}

	// retryAfterSec 现在是毫秒，转为 time.Duration
	return false, 0, time.Duration(retryAfterSec) * time.Millisecond, nil
}

// Reset 重置指定 key 的限流状态。
func (l *Limiter) Reset(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrKeyRequired
	}

	hash, err := l.keyHash(key)
	if err != nil {
		return err
	}

	stateKey := l.stateKey(hash)

	_, err = resetScript.Run(ctx, l.rdb, []string{stateKey}).Result()
	if err != nil {
		return storeError(err)
	}

	return nil
}

// Limit 返回限流器的限流阈值。
func (l *Limiter) Limit() int {
	return l.limit
}

// Window 返回限流器的时间窗口。
func (l *Limiter) Window() time.Duration {
	return time.Duration(l.windowSec) * time.Second
}

// keyHash 使用 HMAC-SHA256 对 key 进行摘要。
func (l *Limiter) keyHash(key string) (string, error) {
	h := hmac.New(sha256.New, l.hmacKey)
	_, _ = h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stateKey 返回 Redis key。
func (l *Limiter) stateKey(hash string) string {
	return l.prefix + ":" + hash
}

// isNilUniversalClient 检查 Redis 客户端是否为 nil。
func isNilUniversalClient(rdb redis.UniversalClient) bool {
	if rdb == nil {
		return true
	}
	v := reflect.ValueOf(rdb)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// storeError 包装 Redis 错误。
func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrStoreUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
}

// resultStrings 解析 Lua 脚本返回值。
func resultStrings(result interface{}) ([]string, error) {
	switch value := result.(type) {
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, v := range value {
			switch item := v.(type) {
			case string:
				parts = append(parts, item)
			case []byte:
				parts = append(parts, string(item))
			case int64:
				parts = append(parts, strconv.FormatInt(item, 10))
			default:
				return nil, fmt.Errorf("unsupported type %T", item)
			}
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("expected array, got %T", result)
	}
}

// allowScript 实现滑动窗口限流逻辑（毫秒级时间戳）。
var allowScript = redis.NewScript(`
local now_ms = tonumber(ARGV[1])         -- 当前时间（毫秒）
local window_ms = tonumber(ARGV[2])      -- 时间窗口（毫秒）
local limit = tonumber(ARGV[3])          -- 限流阈值
local req_id = ARGV[4]                   -- 请求 ID

-- 1. 清除窗口外的请求
local cutoff = now_ms - window_ms
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", cutoff)

-- 2. 计算窗口内请求数
local count = redis.call("ZCARD", KEYS[1])

-- 3. 判断是否超限
if count >= limit then
    -- 获取窗口最早请求的时间
    local oldest = redis.call("ZRANGE", KEYS[1], 0, 0, "WITHSCORES")
    local retry_after_ms = 0
    if #oldest > 0 then
        local oldest_time = tonumber(oldest[2])
        retry_after_ms = (oldest_time + window_ms) - now_ms
        if retry_after_ms < 0 then retry_after_ms = 0 end
    end
    return {"DENIED", tostring(0), tostring(retry_after_ms)}
end

-- 4. 记录本次请求（使用毫秒时间戳作为 score）
redis.call("ZADD", KEYS[1], now_ms, req_id)
-- 5. 设置 TTL（向上取整到秒 + 1 秒缓冲）
local ttl_sec = math.ceil(window_ms / 1000) + 1
redis.call("EXPIRE", KEYS[1], ttl_sec)

-- 6. 返回允许 + 剩余配额
local remaining = limit - count - 1
return {"ALLOWED", tostring(remaining), "0"}
`)

// resetScript 重置限流状态。
var resetScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
    return 0
end
redis.call("DEL", KEYS[1])
return 1
`)
