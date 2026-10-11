package aurora

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP_WithoutTrustedProxies_ShouldIgnoreForwardedHeaders(t *testing.T) {
	// 创建 engine，不配置 TrustedProxies
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	var capturedIP string
	engine.GET("/test", func(c *Context) {
		capturedIP = c.ClientIP()
		c.String(http.StatusOK, capturedIP)
	})

	// 创建请求，带伪造的 X-Forwarded-For 头
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	t.Logf("Request RemoteAddr: %s", req.RemoteAddr)
	t.Logf("Request X-Forwarded-For: %s", req.Header.Get("X-Forwarded-For"))
	t.Logf("ClientIP() returned: %s", capturedIP)

	// 安全默认：应该忽略 X-Forwarded-For，返回 RemoteAddr 的 IP
	if capturedIP == "1.2.3.4" {
		t.Error("✗ Security issue: ClientIP() trusted X-Forwarded-For without TrustedProxies configured")
		t.Error("  This allows IP spoofing attacks")
	} else if capturedIP == "192.168.1.100" {
		t.Log("✓ Secure: ClientIP() ignored X-Forwarded-For and returned RemoteAddr IP")
	} else {
		t.Logf("Unexpected ClientIP: %s", capturedIP)
	}
}

func TestClientIP_WithTrustedProxies_ShouldUseForwardedHeaders(t *testing.T) {
	// 创建 engine，显式配置 TrustedProxies
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.TrustedProxies = []string{"192.168.1.0/24"}
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	var capturedIP string
	engine.GET("/test", func(c *Context) {
		capturedIP = c.ClientIP()
		c.String(http.StatusOK, capturedIP)
	})

	// 创建请求，RemoteAddr 在 TrustedProxies 范围内
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	t.Logf("Request RemoteAddr: %s", req.RemoteAddr)
	t.Logf("Request X-Forwarded-For: %s", req.Header.Get("X-Forwarded-For"))
	t.Logf("ClientIP() returned: %s", capturedIP)

	// 配置了 TrustedProxies 后，应该使用 X-Forwarded-For
	if capturedIP == "1.2.3.4" {
		t.Log("✓ With TrustedProxies: ClientIP() correctly used X-Forwarded-For")
	} else {
		t.Errorf("Expected ClientIP to be 1.2.3.4 (from X-Forwarded-For), got %s", capturedIP)
	}
}

func TestClientIP_WithTrustedProxies_UntrustedRemoteAddr_ShouldIgnoreForwardedHeaders(t *testing.T) {
	// 创建 engine，配置 TrustedProxies
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.TrustedProxies = []string{"10.0.0.0/8"}
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	var capturedIP string
	engine.GET("/test", func(c *Context) {
		capturedIP = c.ClientIP()
		c.String(http.StatusOK, capturedIP)
	})

	// 创建请求，RemoteAddr 不在 TrustedProxies 范围内
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	t.Logf("Request RemoteAddr: %s", req.RemoteAddr)
	t.Logf("Request X-Forwarded-For: %s", req.Header.Get("X-Forwarded-For"))
	t.Logf("ClientIP() returned: %s", capturedIP)

	// RemoteAddr 不在信任列表，应该忽略 X-Forwarded-For
	if capturedIP == "192.168.1.100" {
		t.Log("✓ Untrusted proxy: ClientIP() correctly ignored X-Forwarded-For")
	} else if capturedIP == "1.2.3.4" {
		t.Error("✗ Security issue: ClientIP() trusted X-Forwarded-For from untrusted proxy")
	} else {
		t.Logf("Unexpected ClientIP: %s", capturedIP)
	}
}
