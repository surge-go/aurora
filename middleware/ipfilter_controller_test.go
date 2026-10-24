package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/surge-go/aurora"
)

func TestIPFilterController_AddToBlockList(t *testing.T) {
	ctrl := &IPFilterController{}
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	engine.Use(IPFilter(IPFilterConfig{
		Controller: ctrl,
	}))

	engine.GET("/test", func(c *aurora.Context) {
		c.OK("success")
	})

	// 初始状态：所有 IP 都能访问
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// 动态封禁 IP
	if err := ctrl.AddToBlockList("1.2.3.4"); err != nil {
		t.Fatalf("Failed to add to block list: %v", err)
	}

	// 再次请求：应该被拒绝
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403 after blocking, got %d", w.Code)
	}

	// 验证黑名单内容
	blockList := ctrl.GetBlockList()
	if len(blockList) != 1 || blockList[0] != "1.2.3.4" {
		t.Errorf("Expected block list [1.2.3.4], got %v", blockList)
	}
}

func TestIPFilterController_RemoveFromBlockList(t *testing.T) {
	ctrl := &IPFilterController{}
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	engine.Use(IPFilter(IPFilterConfig{
		BlockList:  []string{"1.2.3.4"},
		Controller: ctrl,
	}))

	engine.GET("/test", func(c *aurora.Context) {
		c.OK("success")
	})

	// 初始状态：IP 被封禁
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403 initially, got %d", w.Code)
	}

	// 动态解封 IP
	if err := ctrl.RemoveFromBlockList("1.2.3.4"); err != nil {
		t.Fatalf("Failed to remove from block list: %v", err)
	}

	// 再次请求：应该通过
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 after unblocking, got %d", w.Code)
	}

	// 验证黑名单为空
	blockList := ctrl.GetBlockList()
	if len(blockList) != 0 {
		t.Errorf("Expected empty block list, got %v", blockList)
	}
}

func TestIPFilterController_SetBlockList(t *testing.T) {
	ctrl := &IPFilterController{}
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	engine.Use(IPFilter(IPFilterConfig{
		Controller: ctrl,
	}))

	engine.GET("/test", func(c *aurora.Context) {
		c.OK("success")
	})

	// 动态设置黑名单
	if err := ctrl.SetBlockList([]string{"1.2.3.4", "5.6.7.8"}); err != nil {
		t.Fatalf("Failed to set block list: %v", err)
	}

	// 测试封禁的 IP
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "1.2.3.4:12345"
	w1 := httptest.NewRecorder()
	engine.ServeHTTP(w1, req1)

	if w1.Code != http.StatusForbidden {
		t.Errorf("Expected 1.2.3.4 to be blocked, got status %d", w1.Code)
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "5.6.7.8:12345"
	w2 := httptest.NewRecorder()
	engine.ServeHTTP(w2, req2)

	if w2.Code != http.StatusForbidden {
		t.Errorf("Expected 5.6.7.8 to be blocked, got status %d", w2.Code)
	}

	// 测试未封禁的 IP
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "9.9.9.9:12345"
	w3 := httptest.NewRecorder()
	engine.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("Expected 9.9.9.9 to be allowed, got status %d", w3.Code)
	}
}

func TestIPFilterController_AddToAllowList(t *testing.T) {
	ctrl := &IPFilterController{}
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	engine.Use(IPFilter(IPFilterConfig{
		AllowList:  []string{"10.0.0.0/8"},
		Controller: ctrl,
	}))

	engine.GET("/test", func(c *aurora.Context) {
		c.OK("success")
	})

	// 初始状态：10.x.x.x 可访问，其他拒绝
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 192.168.1.1 to be blocked initially, got %d", w.Code)
	}

	// 动态添加到白名单
	if err := ctrl.AddToAllowList("192.168.1.1"); err != nil {
		t.Fatalf("Failed to add to allow list: %v", err)
	}

	// 再次请求：应该通过
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 after adding to allow list, got %d", w.Code)
	}
}

func TestIPFilterController_WithCIDR(t *testing.T) {
	ctrl := &IPFilterController{}
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	engine.Use(IPFilter(IPFilterConfig{
		Controller: ctrl,
	}))

	engine.GET("/test", func(c *aurora.Context) {
		c.OK("success")
	})

	// 动态封禁整个网段
	if err := ctrl.AddToBlockList("192.168.1.0/24"); err != nil {
		t.Fatalf("Failed to add CIDR to block list: %v", err)
	}

	// 测试网段内的 IP
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	w1 := httptest.NewRecorder()
	engine.ServeHTTP(w1, req1)

	if w1.Code != http.StatusForbidden {
		t.Errorf("Expected 192.168.1.100 to be blocked, got %d", w1.Code)
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.255:12345"
	w2 := httptest.NewRecorder()
	engine.ServeHTTP(w2, req2)

	if w2.Code != http.StatusForbidden {
		t.Errorf("Expected 192.168.1.255 to be blocked, got %d", w2.Code)
	}

	// 测试网段外的 IP
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "192.168.2.1:12345"
	w3 := httptest.NewRecorder()
	engine.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("Expected 192.168.2.1 to be allowed, got %d", w3.Code)
	}
}

func TestIPFilterController_Concurrent(t *testing.T) {
	ctrl := &IPFilterController{}
	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)

	engine.Use(IPFilter(IPFilterConfig{
		Controller: ctrl,
	}))

	engine.GET("/test", func(c *aurora.Context) {
		c.OK("success")
	})

	// 并发修改和访问
	done := make(chan bool, 10)

	// 5 个 goroutine 并发添加到黑名单
	for i := range 5 {
		go func(id int) {
			for range 10 {
				ctrl.AddToBlockList("1.2.3.4")
			}
			done <- true
		}(i)
	}

	// 5 个 goroutine 并发请求
	for i := range 5 {
		go func(id int) {
			for range 10 {
				req := httptest.NewRequest("GET", "/test", nil)
				req.RemoteAddr = "1.2.3.4:12345"
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, req)
			}
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for range 10 {
		<-done
	}

	// 验证最终状态
	blockList := ctrl.GetBlockList()
	if len(blockList) != 1 || blockList[0] != "1.2.3.4" {
		t.Errorf("Expected block list [1.2.3.4], got %v", blockList)
	}
}
