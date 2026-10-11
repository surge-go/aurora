package aurora

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/surge-go/aurora/core/logger"
)

func TestAnyMethod(t *testing.T) {
	cfg := Config{
		Mode: "test",
		Addr: ":0",
	}
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	called := false
	handler := func(c *Context) {
		called = true
		c.String(http.StatusOK, "ok")
	}

	engine.Any("/test", handler)

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions,
		http.MethodConnect,
		http.MethodTrace,
	}

	for _, method := range methods {
		called = false
		req := httptest.NewRequest(method, "/test", nil)
		w := httptest.NewRecorder()

		engine.ServeHTTP(w, req)

		if !called {
			t.Errorf("Method %s: handler not called", method)
		}

		if method != http.MethodHead && w.Code != http.StatusOK {
			t.Errorf("Method %s: expected status 200, got %d", method, w.Code)
		}
	}
}

func TestRouterGroupAnyMethod(t *testing.T) {
	cfg := Config{
		Mode: "test",
		Addr: ":0",
	}
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	group := engine.Group("/api")

	called := false
	handler := func(c *Context) {
		called = true
		c.String(http.StatusOK, "ok")
	}

	group.Any("/test", handler)

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions,
		http.MethodConnect,
		http.MethodTrace,
	}

	for _, method := range methods {
		called = false
		req := httptest.NewRequest(method, "/api/test", nil)
		w := httptest.NewRecorder()

		engine.ServeHTTP(w, req)

		if !called {
			t.Errorf("Method %s: handler not called", method)
		}

		if method != http.MethodHead && w.Code != http.StatusOK {
			t.Errorf("Method %s: expected status 200, got %d", method, w.Code)
		}
	}
}

// ===== aurora_multi_engine_test.go =====

// TestMultipleEngines 测试创建多个 Engine 实例不会互相影响
func TestMultipleEngines(t *testing.T) {
	// 创建第一个 Engine（测试模式）
	cfg1 := Config{
		Mode: "test",
		Addr: ":8001",
	}
	engine1, err := New(cfg1)
	if err != nil {
		t.Fatalf("Failed to create engine1: %v", err)
	}

	engine1.GET("/test1", func(c *Context) {
		c.String(http.StatusOK, "engine1")
	})

	// 创建第二个 Engine（debug 模式）
	cfg2 := Config{
		Mode: "debug",
		Addr: ":8002",
	}
	engine2, err := New(cfg2)
	if err != nil {
		t.Fatalf("Failed to create engine2: %v", err)
	}

	engine2.GET("/test2", func(c *Context) {
		c.String(http.StatusOK, "engine2")
	})

	// 测试 engine1 的路由
	req1 := httptest.NewRequest(http.MethodGet, "/test1", nil)
	w1 := httptest.NewRecorder()
	engine1.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("engine1: expected status 200, got %d", w1.Code)
	}
	if w1.Body.String() != "engine1" {
		t.Errorf("engine1: expected body 'engine1', got '%s'", w1.Body.String())
	}

	// 测试 engine2 的路由
	req2 := httptest.NewRequest(http.MethodGet, "/test2", nil)
	w2 := httptest.NewRecorder()
	engine2.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("engine2: expected status 200, got %d", w2.Code)
	}
	if w2.Body.String() != "engine2" {
		t.Errorf("engine2: expected body 'engine2', got '%s'", w2.Body.String())
	}

	// 确保 engine1 不会响应 engine2 的路由
	req3 := httptest.NewRequest(http.MethodGet, "/test2", nil)
	w3 := httptest.NewRecorder()
	engine1.ServeHTTP(w3, req3)

	if w3.Code != http.StatusNotFound {
		t.Errorf("engine1 should not handle /test2: expected status 404, got %d", w3.Code)
	}

	// 确保 engine2 不会响应 engine1 的路由
	req4 := httptest.NewRequest(http.MethodGet, "/test1", nil)
	w4 := httptest.NewRecorder()
	engine2.ServeHTTP(w4, req4)

	if w4.Code != http.StatusNotFound {
		t.Errorf("engine2 should not handle /test1: expected status 404, got %d", w4.Code)
	}
}

// TestConcurrentEngineCreation 测试并发创建 Engine 不会产生竞态
func TestConcurrentEngineCreation(t *testing.T) {
	const numEngines = 10

	done := make(chan *Engine, numEngines)
	errs := make(chan error, numEngines)

	// 并发创建多个 Engine
	for i := 0; i < numEngines; i++ {
		go func(id int) {
			cfg := Config{
				Mode: "test",
				Addr: ":0",
			}
			engine, err := New(cfg)
			if err != nil {
				errs <- err
				return
			}
			done <- engine
		}(i)
	}

	// 收集结果
	engines := make([]*Engine, 0, numEngines)
	for i := 0; i < numEngines; i++ {
		select {
		case engine := <-done:
			engines = append(engines, engine)
		case err := <-errs:
			t.Fatalf("Failed to create engine: %v", err)
		}
	}

	// 验证每个 Engine 都是独立的
	if len(engines) != numEngines {
		t.Errorf("Expected %d engines, got %d", numEngines, len(engines))
	}

	for i, engine := range engines {
		if engine == nil {
			t.Errorf("Engine %d is nil", i)
		}
		if engine.gin == nil {
			t.Errorf("Engine %d has nil gin instance", i)
		}
	}
}

// TestGinModeConfiguration verifies that Config.Mode is applied to Gin.
func TestGinModeIsolation(t *testing.T) {
	// 保存原始模式
	originalMode := gin.Mode()
	defer gin.SetMode(originalMode)

	// 创建第一个 Engine (release 模式)
	cfg1 := Config{
		Mode: gin.ReleaseMode,
		Addr: ":0",
	}
	engine1, err := New(cfg1)
	if err != nil {
		t.Fatalf("Failed to create engine1: %v", err)
	}
	if engine1 == nil {
		t.Fatal("engine1 is nil")
	}

	if engine1.config.Mode != gin.ReleaseMode {
		t.Errorf("engine1 config mode = %s, want %s", engine1.config.Mode, gin.ReleaseMode)
	}
	if mode := gin.Mode(); mode != gin.ReleaseMode {
		t.Errorf("Gin global mode after creating engine1 = %s, want %s", mode, gin.ReleaseMode)
	}

	// 创建第二个 Engine (debug 模式)
	cfg2 := Config{
		Mode: gin.DebugMode,
		Addr: ":0",
	}
	engine2, err := New(cfg2)
	if err != nil {
		t.Fatalf("Failed to create engine2: %v", err)
	}
	if engine2 == nil {
		t.Fatal("engine2 is nil")
	}

	if engine2.config.Mode != gin.DebugMode {
		t.Errorf("engine2 config mode = %s, want %s", engine2.config.Mode, gin.DebugMode)
	}
	if mode := gin.Mode(); mode != gin.DebugMode {
		t.Errorf("Gin global mode after creating engine2 = %s, want %s", mode, gin.DebugMode)
	}

	// 验证两个 Engine 都可以正常工作
	engine1.GET("/ping", func(c *Context) {
		c.String(http.StatusOK, "pong1")
	})

	engine2.GET("/ping", func(c *Context) {
		c.String(http.StatusOK, "pong2")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w1 := httptest.NewRecorder()
	engine1.ServeHTTP(w1, req1)
	if w1.Body.String() != "pong1" {
		t.Errorf("engine1: expected 'pong1', got '%s'", w1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w2 := httptest.NewRecorder()
	engine2.ServeHTTP(w2, req2)
	if w2.Body.String() != "pong2" {
		t.Errorf("engine2: expected 'pong2', got '%s'", w2.Body.String())
	}
}

func TestNewHidesGinDebugHooks(t *testing.T) {
	previousMode := gin.Mode()
	previousPrint := gin.DebugPrintFunc
	previousRoutePrint := gin.DebugPrintRouteFunc
	defer func() {
		gin.SetMode(previousMode)
		gin.DebugPrintFunc = previousPrint
		gin.DebugPrintRouteFunc = previousRoutePrint
	}()

	gin.SetMode(gin.ReleaseMode)
	var printCalls, routePrintCalls int
	gin.DebugPrintFunc = func(string, ...any) { printCalls++ }
	gin.DebugPrintRouteFunc = func(string, string, string, int) { routePrintCalls++ }

	if _, err := New(Config{Addr: ":0", Mode: gin.DebugMode}); err != nil {
		t.Fatalf("New failed: %v", err)
	}

	gin.DebugPrintFunc("test")
	gin.DebugPrintRouteFunc("GET", "/", "handler", 1)
	if printCalls != 0 || routePrintCalls != 0 {
		t.Fatalf("Gin debug hooks were not hidden: print calls = %d, route print calls = %d", printCalls, routePrintCalls)
	}
}

// ===== aurora_race_test.go =====

// TestRunEarlyCancellation 测试 Run 在启动早期被取消时不会导致竞态条件
func TestRunEarlyCancellation(t *testing.T) {
	cfg := Config{
		Addr: ":0",
		Mode: "test",
	}

	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	engine.GET("/test", func(c *Context) {
		c.JSON(200, map[string]string{"status": "ok"})
	})

	// 创建一个立即取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Run 应该快速返回，不应该启动服务器
	err = engine.Run(ctx)
	if err != nil && err != context.Canceled {
		t.Logf("Run returned error: %v", err)
	}

	// 验证服务器没有在监听
	if engine.Listening() {
		t.Error("server should not be listening after immediate cancellation")
	}

	// 验证 Shutdown 是安全的（即使 server 可能为 nil）
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := engine.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}
}

// TestServeNilListener 测试 Serve 传入 nil listener 时返回错误
func TestServeNilListener(t *testing.T) {
	cfg := Config{
		Addr: ":0",
		Mode: "test",
	}

	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	err = engine.Serve(nil)
	if err == nil {
		t.Error("Serve(nil) should return an error")
	}
	if err.Error() != "listener cannot be nil" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestConcurrentShutdown 测试并发调用 Shutdown 的安全性
func TestConcurrentShutdown(t *testing.T) {
	cfg := Config{
		Addr: ":0",
		Mode: "test",
	}

	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// 启动服务器
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	go func() {
		engine.Serve(listener)
	}()

	// 等待服务器启动
	time.Sleep(100 * time.Millisecond)

	// 并发调用多次 Shutdown
	done := make(chan bool, 3)
	for i := 0; i < 3; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			engine.Shutdown(ctx)
			done <- true
		}()
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 3; i++ {
		<-done
	}
}

// ===== web_regression_test.go =====

func TestBindingFailureUsesPublicMessage(t *testing.T) {
	type Request struct {
		ID   string `uri:"id" binding:"required"`
		Name string `json:"name" binding:"required"`
	}

	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.RequestID = false
	cfg.Middleware.Recovery = false
	cfg.Middleware.AccessLog = false
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.POST("/users/:id", Bind(func(*Context, *Request) (*struct{}, error) {
		return &struct{}{}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "/users/42", strings.NewReader(`{"name":`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Message != ErrBadRequest.Message {
		t.Fatalf("message = %q, want %q", response.Message, ErrBadRequest.Message)
	}
	if strings.Contains(w.Body.String(), "unexpected end") || strings.Contains(w.Body.String(), "Request.Name") {
		t.Fatalf("binding details leaked into response: %s", w.Body.String())
	}
}

func TestEngineCannotRestartAfterShutdown(t *testing.T) {
	e, err := New(Config{Addr: "127.0.0.1:0", Mode: "test"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !e.Listening() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !e.Listening() {
		t.Fatal("server did not start listening")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}

	if err := e.Start(); err == nil || err.Error() != "server already started" {
		t.Fatalf("restart error = %v, want server already started", err)
	}
}

func TestRunReturnsAfterExternalShutdown(t *testing.T) {
	e, err := New(Config{Addr: "127.0.0.1:0", Mode: "test"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for !e.Listening() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !e.Listening() {
		t.Fatal("server did not start listening")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after external shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after external shutdown")
	}
}

func TestShutdownRejectsNilContext(t *testing.T) {
	e, err := New(Config{Addr: "127.0.0.1:0", Mode: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !e.Listening() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !e.Listening() {
		t.Fatal("server did not start listening")
	}
	if err := e.Shutdown(nil); err == nil || err.Error() != "context cannot be nil" {
		t.Fatalf("Shutdown(nil) error = %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRecoveryPreservesWrittenResponseAfterPanic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.RequestID = false
	cfg.Middleware.AccessLog = false
	cfg.Middleware.Tracing = false
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.GET("/panic", func(c *Context) {
		c.String(http.StatusOK, "partial")
		panic("after response")
	})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after committed response", w.Code)
	}
	if got := w.Body.String(); got != "partial" {
		t.Fatalf("body = %q, want only committed response", got)
	}
}

func TestAccessLogUsesRouteTemplate(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "access.log")
	log, err := logger.New(&logger.Config{Output: logger.OutputFile, File: logPath})
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.RequestID = false
	e, err := New(cfg, WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	e.GET("/users/:id", func(c *Context) { c.OK(nil) })

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/123", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if !strings.Contains(output, `"/users/:id"`) || strings.Contains(output, `"/users/123"`) {
		t.Fatalf("access log path = %s", output)
	}
}
