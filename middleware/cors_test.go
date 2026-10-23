package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/surge-go/aurora"
)

func TestCORS_DefaultConfig(t *testing.T) {
	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(DefaultCORSConfig()))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	// 测试带 Origin 的 GET 请求
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Access-Control-Allow-Origin: *, got %s", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_AllowSpecificOrigins(t *testing.T) {
	config := CORSConfig{
		AllowOrigins: []string{"https://example.com", "https://app.example.com"},
		AllowMethods: []string{http.MethodGet, http.MethodPost},
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	tests := []struct {
		name           string
		origin         string
		expectedOrigin string
		expectedVary   string
	}{
		{
			name:           "allowed origin",
			origin:         "https://example.com",
			expectedOrigin: "https://example.com",
			expectedVary:   "Origin",
		},
		{
			name:           "another allowed origin",
			origin:         "https://app.example.com",
			expectedOrigin: "https://app.example.com",
			expectedVary:   "Origin",
		},
		{
			name:           "disallowed origin",
			origin:         "https://evil.com",
			expectedOrigin: "",
			expectedVary:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", w.Code)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.expectedOrigin {
				t.Errorf("expected Access-Control-Allow-Origin: %s, got %s", tt.expectedOrigin, got)
			}
			if got := w.Header().Get("Vary"); got != tt.expectedVary {
				t.Errorf("expected Vary: %s, got %s", tt.expectedVary, got)
			}
		})
	}
}

func TestCORS_WildcardSubdomain(t *testing.T) {
	config := CORSConfig{
		AllowOrigins: []string{"https://*.example.com"},
		AllowMethods: []string{http.MethodGet},
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	tests := []struct {
		name           string
		origin         string
		expectedOrigin string
	}{
		{
			name:           "subdomain matches",
			origin:         "https://app.example.com",
			expectedOrigin: "https://app.example.com",
		},
		{
			name:           "nested subdomain matches",
			origin:         "https://api.app.example.com",
			expectedOrigin: "https://api.app.example.com",
		},
		{
			name:           "main domain does not match",
			origin:         "https://example.com",
			expectedOrigin: "",
		},
		{
			name:           "different domain does not match",
			origin:         "https://evil.com",
			expectedOrigin: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)

			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.expectedOrigin {
				t.Errorf("expected Access-Control-Allow-Origin: %s, got %s", tt.expectedOrigin, got)
			}
		})
	}
}

func TestCORS_PreflightRequest(t *testing.T) {
	config := CORSConfig{
		AllowOrigins: []string{"https://example.com"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut},
		AllowHeaders: []string{"Content-Type", "Authorization"},
		MaxAge:       1 * time.Hour,
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
	engine.POST("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	// 预检请求
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	// 预检请求应该返回 204 No Content
	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", w.Code)
	}

	// 检查响应头
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("expected Access-Control-Allow-Origin: https://example.com, got %s", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT" {
		t.Errorf("expected Access-Control-Allow-Methods: GET, POST, PUT, got %s", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization" {
		t.Errorf("expected Access-Control-Allow-Headers: Content-Type, Authorization, got %s", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "3600" {
		t.Errorf("expected Access-Control-Max-Age: 3600, got %s", got)
	}
}

func TestCORS_WithCredentials(t *testing.T) {
	config := CORSConfig{
		AllowOrigins:     []string{"https://example.com"},
		AllowMethods:     []string{http.MethodGet},
		AllowCredentials: true,
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("expected Access-Control-Allow-Credentials: true, got %s", got)
	}
}

func TestCORS_ExposeHeaders(t *testing.T) {
	config := CORSConfig{
		AllowOrigins:  []string{"*"},
		AllowMethods:  []string{http.MethodGet},
		ExposeHeaders: []string{"X-Total-Count", "X-Page-Number"},
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.SetHeader("X-Total-Count", "100")
		c.SetHeader("X-Page-Number", "1")
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "X-Total-Count, X-Page-Number" {
		t.Errorf("expected Access-Control-Expose-Headers: X-Total-Count, X-Page-Number, got %s", got)
	}
}

func TestCORS_NoOriginHeader(t *testing.T) {
	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(DefaultCORSConfig()))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	// 没有 Origin 头的请求（非跨域）
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	// 不应设置 CORS 头
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin header, got %s", got)
	}
}

func TestCORS_PrivateNetwork(t *testing.T) {
	config := CORSConfig{
		AllowOrigins:        []string{"https://example.com"},
		AllowMethods:        []string{http.MethodGet},
		AllowPrivateNetwork: true,
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]string{"message": "ok"})
	})

	// 预检请求带 Access-Control-Request-Private-Network
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Errorf("expected Access-Control-Allow-Private-Network: true, got %s", got)
	}
}

func TestCORS_PanicOnWildcardWithCredentials(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when AllowCredentials=true with wildcard origin")
		}
	}()

	config := CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowCredentials: true,
	}

	engine, _ := aurora.New(aurora.DefaultConfig())
	engine.Use(CORS(config))
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name     string
		origin   string
		pattern  string
		expected bool
	}{
		{
			name:     "subdomain matches",
			origin:   "https://app.example.com",
			pattern:  "https://*.example.com",
			expected: true,
		},
		{
			name:     "nested subdomain matches",
			origin:   "https://api.app.example.com",
			pattern:  "https://*.example.com",
			expected: true,
		},
		{
			name:     "exact domain does not match",
			origin:   "https://example.com",
			pattern:  "https://*.example.com",
			expected: false,
		},
		{
			name:     "different domain does not match",
			origin:   "https://evil.com",
			pattern:  "https://*.example.com",
			expected: false,
		},
		{
			name:     "multiple wildcards not supported",
			origin:   "https://app.example.com",
			pattern:  "https://*.*",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchWildcard(tt.origin, tt.pattern)
			if got != tt.expected {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tt.origin, tt.pattern, got, tt.expected)
			}
		})
	}
}
