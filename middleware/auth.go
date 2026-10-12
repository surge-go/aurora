// Package middleware 提供可复用的 Aurora HTTP 中间件适配器。
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/surge-go/aurora"
	coreauth "github.com/surge-go/aurora/core/auth"
	"golang.org/x/sync/singleflight"
)

const defaultClaimsKey = "aurora.auth.claims"

// Option 用于配置 AuthMiddleware。
type Option func(*options)

type options struct {
	claimsKey                  string
	now                        func() time.Time
	preRefreshWindow           time.Duration
	refreshTokenHeader         string
	accessTokenResponseHeader  string
	refreshTokenResponseHeader string
}

const (
	defaultRefreshTokenHeader         = "X-Refresh-Token"
	defaultAccessTokenResponseHeader  = "X-Access-Token"
	defaultRefreshTokenResponseHeader = "X-Refresh-Token"
)

// WithClaimsKey 修改在请求上下文中存储已验证 claims 的键名。
// 默认键名是稳定的，建议大多数调用方直接使用。
func WithClaimsKey(key string) Option {
	return func(opts *options) {
		if key = strings.TrimSpace(key); key != "" {
			opts.claimsKey = key
		}
	}
}

// WithClock supplies the clock used to decide whether proactive refresh is
// needed. It is primarily useful for deterministic tests and controlled runtimes.
func WithClock(now func() time.Time) Option {
	return func(opts *options) {
		if now != nil {
			opts.now = now
		}
	}
}

// WithPreRefreshWindow enables proactive refresh while the access token has
// less than window remaining. Zero disables proactive refresh.
func WithPreRefreshWindow(window time.Duration) Option {
	return func(opts *options) {
		if window < 0 {
			window = 0
		}
		opts.preRefreshWindow = window
	}
}

// WithRefreshTokenHeader changes the request header containing the refresh token.
func WithRefreshTokenHeader(header string) Option {
	return func(opts *options) {
		if header = strings.TrimSpace(header); header != "" {
			opts.refreshTokenHeader = header
		}
	}
}

// WithTokenResponseHeaders changes the response headers used for rotated tokens.
func WithTokenResponseHeaders(accessHeader, refreshHeader string) Option {
	return func(opts *options) {
		if accessHeader = strings.TrimSpace(accessHeader); accessHeader != "" {
			opts.accessTokenResponseHeader = accessHeader
		}
		if refreshHeader = strings.TrimSpace(refreshHeader); refreshHeader != "" {
			opts.refreshTokenResponseHeader = refreshHeader
		}
	}
}

// AuthMiddleware 校验 Bearer access token，并将 claims 存入 Aurora 请求上下文。
// 它不会主动加载用户或权限信息；Handler 应使用 claims 中的 subject
// 完成当前业务相关的校验。
func AuthMiddleware(manager *coreauth.Manager, opts ...Option) aurora.Middleware {
	config := options{
		claimsKey:                  defaultClaimsKey,
		now:                        time.Now,
		refreshTokenHeader:         defaultRefreshTokenHeader,
		accessTokenResponseHeader:  defaultAccessTokenResponseHeader,
		refreshTokenResponseHeader: defaultRefreshTokenResponseHeader,
	}
	var refreshGroup singleflight.Group
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}

	return func(c *aurora.Context) {
		if manager == nil {
			c.Fail(aurora.ErrServiceUnavailable.WithErr(errors.New("auth manager is nil")))
			return
		}

		raw, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			c.Fail(aurora.ErrUnauthorized)
			return
		}

		claims, err := manager.VerifyAccess(c.Context(), raw)
		if err == nil {
			// Proactive refresh is best effort: the current access token is still
			// valid, so a missing, reused, or unavailable refresh token must not
			// turn this request into an authentication failure.
			if config.preRefreshWindow > 0 && claims.ExpiresAt != nil &&
				!claims.ExpiresAt.Time.After(config.now().Add(config.preRefreshWindow)) {
				if refreshRaw := strings.TrimSpace(c.GetHeader(config.refreshTokenHeader)); refreshRaw != "" {
					if pair, refreshErr := refreshWithSingleflight(&refreshGroup, c.Context(), manager, refreshRaw); refreshErr == nil {
						if refreshedClaims, parseErr := manager.VerifyAccessToken(pair.AccessToken); parseErr == nil {
							claims = refreshedClaims
							setTokenResponseHeaders(c, config, pair)
						}
					}
				}
			}
			c.Set(config.claimsKey, claims)
			c.Next()
			return
		}

		if errors.Is(err, coreauth.ErrTokenExpired) {
			refreshRaw := strings.TrimSpace(c.GetHeader(config.refreshTokenHeader))
			if refreshRaw == "" {
				c.Fail(aurora.ErrUnauthorized)
				return
			}
			pair, refreshErr := refreshWithSingleflight(&refreshGroup, c.Context(), manager, refreshRaw)
			if refreshErr != nil {
				failAuth(c, refreshErr)
				return
			}
			claims, parseErr := manager.VerifyAccessToken(pair.AccessToken)
			if parseErr != nil {
				c.Fail(aurora.ErrUnauthorized.WithErr(parseErr))
				return
			}
			setTokenResponseHeaders(c, config, pair)
			c.Set(config.claimsKey, claims)
			c.Next()
			return
		}

		failAuth(c, err)
	}
}

// refreshWithSingleflight merges concurrent refreshes for the same token in
// one process. The refresh token itself is never used as the in-memory key.
// Redis Lua rotation remains the authority across processes and instances.
func refreshWithSingleflight(group *singleflight.Group, ctx context.Context, manager *coreauth.Manager, raw string) (*coreauth.TokenPair, error) {
	digest := sha256.Sum256([]byte(raw))
	key := hex.EncodeToString(digest[:])
	resultCh := group.DoChan(key, func() (any, error) {
		sharedCtx := context.WithoutCancel(ctx)
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			sharedCtx, cancel = context.WithDeadline(sharedCtx, deadline)
		}
		defer cancel()
		return manager.Refresh(sharedCtx, raw, nil)
	})
	select {
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		pair, ok := result.Val.(*coreauth.TokenPair)
		if !ok || pair == nil {
			return nil, errors.New("auth middleware: invalid refresh result")
		}
		return pair, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func failAuth(c *aurora.Context, err error) {
	if errors.Is(err, coreauth.ErrStoreUnavailable) {
		c.Fail(aurora.ErrServiceUnavailable.WithErr(err))
		return
	}
	c.Fail(aurora.ErrUnauthorized.WithErr(err))
}

func setTokenResponseHeaders(c *aurora.Context, config options, pair *coreauth.TokenPair) {
	if pair == nil {
		return
	}
	if pair.AccessToken != "" {
		c.SetHeader(config.accessTokenResponseHeader, pair.AccessToken)
	}
	if pair.RefreshToken != "" {
		c.SetHeader(config.refreshTokenResponseHeader, pair.RefreshToken)
	}
}

// Auth 是 AuthMiddleware 的简写别名。
func Auth(manager *coreauth.Manager, opts ...Option) aurora.Middleware {
	return AuthMiddleware(manager, opts...)
}

// Claims 返回 AuthMiddleware 已放入请求上下文的 claims。
// 当中间件未执行或使用了自定义键名时返回 false。
func Claims(c *aurora.Context) (*coreauth.Claims, bool) {
	return ClaimsWithKey(c, defaultClaimsKey)
}

// ClaimsWithKey 读取通过 WithClaimsKey 存储的 claims。
func ClaimsWithKey(c *aurora.Context, key string) (*coreauth.Claims, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(key)
	if !ok {
		return nil, false
	}
	claims, ok := value.(*coreauth.Claims)
	return claims, ok && claims != nil
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
