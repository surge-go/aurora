// Package ratelimit 提供基于 Redis 的分布式滑动窗口限流器。
//
// 该包实现了精确的滑动窗口限流算法，适用于 IP 限流、API 配额等场景。
// 使用 HMAC-SHA256 对限流 key（如 IP 地址）进行摘要，防止 Redis key 泄露原始标识。
//
// # 基本用法
//
//	cfg := ratelimit.Config{
//	    KeyPrefix: "aurora:login:ip:{limiter}",
//	    HMACKey:   os.Getenv("HMAC_KEY"), // 至少 32 字节
//	    Limit:     10,                      // 每个窗口 10 次请求
//	    Window:    time.Minute,             // 滑动窗口 1 分钟
//	}
//
//	limiter, err := ratelimit.New(redisClient, cfg)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// 检查是否允许请求
//	allowed, remaining, retryAfter, err := limiter.Allow(ctx, "192.168.1.1")
//	if err != nil {
//	    // 处理 Redis 故障
//	}
//	if !allowed {
//	    // 触发限流，retryAfter 指示下次可用时间
//	}
//
// # 并发安全
//
// Limiter 是并发安全的，可以在多个 goroutine 间共享使用。
// 限流状态存储在 Redis 中，支持跨进程、跨实例的分布式限流。
//
// # 隐私保护
//
// 所有限流 key（如 IP 地址、用户 ID）在存储到 Redis 前会使用 HMAC-SHA256 进行摘要。
// 这确保即使 Redis 数据泄露，也无法反推原始标识。
//
// # Redis 集群支持
//
// KeyPrefix 必须包含 Redis hash tag（如 {limiter}），以确保同一限流器的所有 key
// 落在同一个 Redis 集群节点上，保证 Lua 脚本的原子性。
package ratelimit
