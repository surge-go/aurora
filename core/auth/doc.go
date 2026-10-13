// Package auth 基于 Redis 管理 JWT access 与 refresh token 的生命周期。
//
// 本包不创建也不关闭 Redis 客户端。应用负责其生命周期，
// 并通过 New 注入 redis.UniversalClient。
package auth
