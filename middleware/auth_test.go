package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/surge-go/aurora"
	coreauth "github.com/surge-go/aurora/core/auth"
)

func TestAuthMiddlewareStoresClaims(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg, coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager))
	engine.GET("/private", func(c *aurora.Context) {
		claims, ok := Claims(c)
		require.True(t, ok)
		require.Equal(t, "user-1", claims.Subject)
		c.OK(nil)
	})
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 200, resp.Code)
}

func TestAuthMiddlewareRejectsMissingAndInvalidTokens(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg, coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.NoError(t, err)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(Auth(manager))
	engine.GET("/private", func(c *aurora.Context) { c.OK(nil) })
	for _, header := range []string{"", "Basic abc", "Bearer invalid"} {
		req := httptest.NewRequest("GET", "/private", nil)
		req.Header.Set("Authorization", header)
		resp := httptest.NewRecorder()
		engine.ServeHTTP(resp, req)
		require.Equal(t, 401, resp.Code, header)
	}
}

func TestBearerTokenParsing(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{" Bearer   abc ", "abc", true},
		{"Bearer", "", false},
		{"Basic abc", "", false},
		{"Bearer a b", "", false},
	} {
		got, ok := bearerToken(tc.header)
		require.Equal(t, tc.want, got)
		require.Equal(t, tc.ok, ok)
	}
}

func TestAuthMiddlewareProactivelyRefreshesAndContinues(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.AccessTTL = 30 * time.Second
	cfg.RefreshTTL = time.Hour
	cfg.MaxSessionTTL = 2 * time.Hour
	manager, err := coreauth.New(rdb, cfg,
		coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		coreauth.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager, WithClock(func() time.Time { return now }), WithPreRefreshWindow(time.Minute)))
	engine.GET("/private", func(c *aurora.Context) {
		claims, ok := Claims(c)
		require.True(t, ok)
		require.Equal(t, "user-1", claims.Subject)
		c.OK(nil)
	})
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-Refresh-Token", pair.RefreshToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 200, resp.Code)
	require.NotEmpty(t, resp.Header().Get("X-Access-Token"))
	require.NotEmpty(t, resp.Header().Get("X-Refresh-Token"))
	require.NotEqual(t, pair.AccessToken, resp.Header().Get("X-Access-Token"))
}

func TestAuthMiddlewareReturnsServiceUnavailableWhenRedisIsDown(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg, coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)
	mr.Close()

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager))
	engine.GET("/private", func(c *aurora.Context) { c.OK(nil) })
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 503, resp.Code)
}

func TestAuthMiddlewarePreRefreshFailureDoesNotRejectValidAccess(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.AccessTTL = time.Minute
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg, coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager, WithPreRefreshWindow(time.Hour)))
	engine.GET("/private", func(c *aurora.Context) { c.OK(nil) })
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-Refresh-Token", "invalid")
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 200, resp.Code)
}

func TestAuthMiddlewareRefreshesExpiredAccessAndContinues(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.AccessTTL = time.Minute
	cfg.RefreshTTL = time.Hour
	cfg.MaxSessionTTL = 2 * time.Hour
	manager, err := coreauth.New(rdb, cfg,
		coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		coreauth.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager))
	engine.GET("/private", func(c *aurora.Context) {
		claims, ok := Claims(c)
		require.True(t, ok)
		require.Equal(t, "user-1", claims.Subject)
		c.OK(nil)
	})
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-Refresh-Token", pair.RefreshToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 200, resp.Code)
	require.NotEmpty(t, resp.Header().Get("X-Access-Token"))
	require.NotEmpty(t, resp.Header().Get("X-Refresh-Token"))
}

func TestAuthMiddlewareExpiredAccessRequiresRefreshToken(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.AccessTTL = time.Minute
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg,
		coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		coreauth.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager))
	engine.GET("/private", func(c *aurora.Context) { c.OK(nil) })
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 401, resp.Code)
}

func TestAuthMiddlewareRevokedAccessCannotRefresh(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.AccessTTL = time.Minute
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg,
		coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		coreauth.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)
	claims, err := manager.VerifyAccess(t.Context(), pair.AccessToken)
	require.NoError(t, err)
	require.NoError(t, manager.RevokeSession(t.Context(), claims.SessionID))
	now = now.Add(2 * time.Minute)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager))
	engine.GET("/private", func(c *aurora.Context) { c.OK(nil) })
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-Refresh-Token", pair.RefreshToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 401, resp.Code)
}

func TestAuthMiddlewareRejectsReplayedRefreshToken(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.AccessTTL = time.Minute
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg,
		coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)),
		coreauth.WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "user-1"}})
	require.NoError(t, err)
	_, err = manager.Refresh(t.Context(), pair.RefreshToken, nil)
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(AuthMiddleware(manager))
	engine.GET("/private", func(c *aurora.Context) { c.OK(nil) })
	req := httptest.NewRequest("GET", "/private", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-Refresh-Token", pair.RefreshToken)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	require.Equal(t, 401, resp.Code)
}
