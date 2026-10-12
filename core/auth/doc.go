// Package auth manages JWT access and refresh token lifecycles backed by Redis.
//
// The package does not create or close the Redis client. Applications own that
// lifecycle and inject a redis.UniversalClient into New.
package auth
