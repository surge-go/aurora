package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/surge-go/aurora"
)

func TestIPFilter_AllowList(t *testing.T) {
	tests := []struct {
		name       string
		allowList  []string
		clientIP   string
		wantStatus int
	}{
		{
			name:       "allowed single IP",
			allowList:  []string{"192.168.1.100"},
			clientIP:   "192.168.1.100",
			wantStatus: 200,
		},
		{
			name:       "blocked IP not in allowlist",
			allowList:  []string{"192.168.1.100"},
			clientIP:   "192.168.1.101",
			wantStatus: 403,
		},
		{
			name:       "allowed CIDR range",
			allowList:  []string{"10.0.0.0/8"},
			clientIP:   "10.1.2.3",
			wantStatus: 200,
		},
		{
			name:       "blocked outside CIDR range",
			allowList:  []string{"10.0.0.0/8"},
			clientIP:   "192.168.1.1",
			wantStatus: 403,
		},
		{
			name:       "multiple allowlist entries",
			allowList:  []string{"192.168.1.0/24", "10.0.0.100"},
			clientIP:   "10.0.0.100",
			wantStatus: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := aurora.New(aurora.DefaultConfig())
			require.NoError(t, err)

			config := DefaultIPFilterConfig()
			config.AllowList = tt.allowList
			config.IPExtractor = func(c *aurora.Context) string {
				return tt.clientIP
			}

			engine.Use(IPFilter(config))
			engine.GET("/test", func(c *aurora.Context) {
				c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)

			require.Equal(t, tt.wantStatus, resp.Code)
		})
	}
}

func TestIPFilter_BlockList(t *testing.T) {
	tests := []struct {
		name       string
		blockList  []string
		clientIP   string
		wantStatus int
	}{
		{
			name:       "blocked single IP",
			blockList:  []string{"192.168.1.100"},
			clientIP:   "192.168.1.100",
			wantStatus: 403,
		},
		{
			name:       "allowed IP not in blocklist",
			blockList:  []string{"192.168.1.100"},
			clientIP:   "192.168.1.101",
			wantStatus: 200,
		},
		{
			name:       "blocked CIDR range",
			blockList:  []string{"10.0.0.0/8"},
			clientIP:   "10.1.2.3",
			wantStatus: 403,
		},
		{
			name:       "allowed outside CIDR range",
			blockList:  []string{"10.0.0.0/8"},
			clientIP:   "192.168.1.1",
			wantStatus: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := aurora.New(aurora.DefaultConfig())
			require.NoError(t, err)

			config := DefaultIPFilterConfig()
			config.BlockList = tt.blockList
			config.IPExtractor = func(c *aurora.Context) string {
				return tt.clientIP
			}

			engine.Use(IPFilter(config))
			engine.GET("/test", func(c *aurora.Context) {
				c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)

			require.Equal(t, tt.wantStatus, resp.Code)
		})
	}
}

func TestIPFilter_AllowListAndBlockList(t *testing.T) {
	// 白名单优先：即使在白名单中，如果也在黑名单中仍会被拒绝
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	config := DefaultIPFilterConfig()
	config.AllowList = []string{"192.168.1.0/24"}
	config.BlockList = []string{"192.168.1.100"}
	config.IPExtractor = func(c *aurora.Context) string {
		return "192.168.1.100"
	}

	engine.Use(IPFilter(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	require.Equal(t, 403, resp.Code)
}

func TestIPFilter_SkipPaths(t *testing.T) {
	tests := []struct {
		name       string
		skipPaths  []string
		requestURL string
		clientIP   string
		wantStatus int
	}{
		{
			name:       "skip exact path",
			skipPaths:  []string{"/health"},
			requestURL: "/health",
			clientIP:   "1.2.3.4",
			wantStatus: 200,
		},
		{
			name:       "skip prefix path",
			skipPaths:  []string{"/public/*"},
			requestURL: "/public/images/logo.png",
			clientIP:   "1.2.3.4",
			wantStatus: 200,
		},
		{
			name:       "no skip for protected path",
			skipPaths:  []string{"/health"},
			requestURL: "/api/users",
			clientIP:   "1.2.3.4",
			wantStatus: 403,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := aurora.New(aurora.DefaultConfig())
			require.NoError(t, err)

			config := DefaultIPFilterConfig()
			config.AllowList = []string{"192.168.1.100"} // 只允许这个 IP
			config.SkipPaths = tt.skipPaths
			config.IPExtractor = func(c *aurora.Context) string {
				return tt.clientIP
			}

			engine.Use(IPFilter(config))
			engine.GET("/*path", func(c *aurora.Context) {
				c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			req := httptest.NewRequest(http.MethodGet, tt.requestURL, nil)
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)

			require.Equal(t, tt.wantStatus, resp.Code)
		})
	}
}

func TestIPFilter_ExtractClientIP(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		xForwardedFor  string
		xRealIP        string
		trustedProxies []string
		wantIP         string
	}{
		{
			name:       "direct connection",
			remoteAddr: "192.168.1.100:12345",
			wantIP:     "192.168.1.100",
		},
		{
			name:           "trusted proxy with X-Forwarded-For",
			remoteAddr:     "10.0.0.1:12345",
			xForwardedFor:  "203.0.113.1, 10.0.0.1",
			trustedProxies: []string{"10.0.0.1"},
			wantIP:         "203.0.113.1",
		},
		{
			name:           "untrusted proxy ignores X-Forwarded-For",
			remoteAddr:     "192.168.1.100:12345",
			xForwardedFor:  "203.0.113.1",
			trustedProxies: []string{"10.0.0.1"},
			wantIP:         "192.168.1.100",
		},
		{
			name:           "trusted proxy with X-Real-IP",
			remoteAddr:     "10.0.0.1:12345",
			xRealIP:        "203.0.113.1",
			trustedProxies: []string{"10.0.0.1"},
			wantIP:         "203.0.113.1",
		},
		{
			name:       "no port in remote addr",
			remoteAddr: "192.168.1.100",
			wantIP:     "192.168.1.100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var extractedIP string

			engine, err := aurora.New(aurora.DefaultConfig())
			require.NoError(t, err)

			config := DefaultIPFilterConfig()
			config.TrustedProxies = tt.trustedProxies
			config.AllowList = []string{"1.2.3.4"} // 只允许这个 IP，其他都会被拒绝
			config.OnRejected = func(c *aurora.Context, ip string) {
				extractedIP = ip
				c.JSON(http.StatusForbidden, map[string]string{"ip": ip})
			}

			engine.Use(IPFilter(config))
			engine.GET("/test", func(c *aurora.Context) {
				c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}

			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)

			require.Equal(t, http.StatusForbidden, resp.Code, "should be rejected")
			require.Equal(t, tt.wantIP, extractedIP)
		})
	}
}

func TestIPFilter_CustomOnRejected(t *testing.T) {
	customCalled := false
	var rejectedIP string

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	config := DefaultIPFilterConfig()
	config.AllowList = []string{"192.168.1.100"}
	config.IPExtractor = func(c *aurora.Context) string {
		return "1.2.3.4"
	}
	config.OnRejected = func(c *aurora.Context, ip string) {
		customCalled = true
		rejectedIP = ip
		c.JSON(http.StatusForbidden, map[string]string{"error": "custom rejection", "ip": ip})
	}

	engine.Use(IPFilter(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	require.True(t, customCalled)
	require.Equal(t, "1.2.3.4", rejectedIP)
	require.Equal(t, 403, resp.Code)
}

func TestParseIPList(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		wantLen  int
		testIP   string
		wantMatch bool
	}{
		{
			name:      "single IP",
			input:     []string{"192.168.1.100"},
			wantLen:   1,
			testIP:    "192.168.1.100",
			wantMatch: true,
		},
		{
			name:      "CIDR notation",
			input:     []string{"10.0.0.0/8"},
			wantLen:   1,
			testIP:    "10.5.10.20",
			wantMatch: true,
		},
		{
			name:      "mixed formats",
			input:     []string{"192.168.1.1", "10.0.0.0/8", "172.16.0.0/12"},
			wantLen:   3,
			testIP:    "172.20.1.1",
			wantMatch: true,
		},
		{
			name:      "empty strings ignored",
			input:     []string{"", "192.168.1.1", "  ", "10.0.0.0/8"},
			wantLen:   2,
			testIP:    "192.168.1.1",
			wantMatch: true,
		},
		{
			name:      "IPv6 address",
			input:     []string{"2001:db8::1"},
			wantLen:   1,
			testIP:    "2001:db8::1",
			wantMatch: true,
		},
		{
			name:      "IPv6 CIDR",
			input:     []string{"2001:db8::/32"},
			wantLen:   1,
			testIP:    "2001:db8::100",
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixes := parseIPList(tt.input)
			require.Equal(t, tt.wantLen, len(prefixes))

			if tt.testIP != "" {
				addr, err := netip.ParseAddr(tt.testIP)
				require.NoError(t, err)
				matched := matchesAnyPrefix(addr, prefixes)
				require.Equal(t, tt.wantMatch, matched)
			}
		})
	}
}
