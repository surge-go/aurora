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

func TestAuthMiddlewareSkipPaths(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := coreauth.DefaultConfig()
	cfg.Issuer = "test"
	cfg.Audience = []string{"client"}
	cfg.MaxSessionTTL = time.Hour
	manager, err := coreauth.New(rdb, cfg, coreauth.WithSigningSecret(bytes.Repeat([]byte("k"), 32)))
	require.NoError(t, err)

	tests := []struct {
		name           string
		skipPaths      []string
		requestPath    string
		withAuthHeader bool
		wantStatus     int
	}{
		{
			name:           "exact match skips auth",
			skipPaths:      []string{"/health", "/metrics"},
			requestPath:    "/health",
			withAuthHeader: false,
			wantStatus:     200,
		},
		{
			name:           "no match requires auth",
			skipPaths:      []string{"/health"},
			requestPath:    "/private",
			withAuthHeader: false,
			wantStatus:     401,
		},
		{
			name:           "prefix wildcard matches",
			skipPaths:      []string{"/public/*"},
			requestPath:    "/public/images/logo.png",
			withAuthHeader: false,
			wantStatus:     200,
		},
		{
			name:           "prefix wildcard no match",
			skipPaths:      []string{"/public/*"},
			requestPath:    "/private/data",
			withAuthHeader: false,
			wantStatus:     401,
		},
		{
			name:           "mixed patterns",
			skipPaths:      []string{"/health", "/public/*", "/docs/*"},
			requestPath:    "/docs/api.html",
			withAuthHeader: false,
			wantStatus:     200,
		},
		{
			name:           "empty pattern ignored",
			skipPaths:      []string{"", "/health"},
			requestPath:    "/health",
			withAuthHeader: false,
			wantStatus:     200,
		},
		{
			name:           "nested path with wildcard",
			skipPaths:      []string{"/api/v1/public/*"},
			requestPath:    "/api/v1/public/login",
			withAuthHeader: false,
			wantStatus:     200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := aurora.New(aurora.DefaultConfig())
			require.NoError(t, err)
			engine.Use(AuthMiddleware(manager, WithSkipPaths(tt.skipPaths...)))
			engine.GET("/*path", func(c *aurora.Context) { c.OK(nil) })

			req := httptest.NewRequest("GET", tt.requestPath, nil)
			if tt.withAuthHeader {
				pair, err := manager.Issue(t.Context(), coreauth.IssueRequest{Principal: coreauth.Principal{Subject: "test-user"}})
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			}
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)
			require.Equal(t, tt.wantStatus, resp.Code)
		})
	}
}

func TestShouldSkipAuth(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		skipPaths []string
		want      bool
	}{
		{
			name:      "exact match",
			path:      "/health",
			skipPaths: []string{"/health"},
			want:      true,
		},
		{
			name:      "no match",
			path:      "/api/users",
			skipPaths: []string{"/health"},
			want:      false,
		},
		{
			name:      "prefix match with wildcard",
			path:      "/public/images/logo.png",
			skipPaths: []string{"/public/*"},
			want:      true,
		},
		{
			name:      "prefix match at boundary",
			path:      "/public/",
			skipPaths: []string{"/public/*"},
			want:      true,
		},
		{
			name:      "similar but not matching prefix",
			path:      "/publicapi/test",
			skipPaths: []string{"/public/*"},
			want:      false,
		},
		{
			name:      "empty skip paths",
			path:      "/any",
			skipPaths: []string{},
			want:      false,
		},
		{
			name:      "empty pattern in list",
			path:      "/test",
			skipPaths: []string{"", "/health"},
			want:      false,
		},
		{
			name:      "multiple patterns first match",
			path:      "/health",
			skipPaths: []string{"/health", "/metrics", "/public/*"},
			want:      true,
		},
		{
			name:      "multiple patterns last match",
			path:      "/public/data",
			skipPaths: []string{"/health", "/metrics", "/public/*"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipAuth(tt.path, tt.skipPaths)
			require.Equal(t, tt.want, got)
		})
	}
}
