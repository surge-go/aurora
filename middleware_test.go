package aurora

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/surge-go/aurora/core/logger"
	corelogger "github.com/surge-go/aurora/core/logger"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatAccessLog(t *testing.T) {
	got := formatAccessLog(
		time.Date(2026, time.October, 9, 23, 7, 49, 0, time.Local),
		517*time.Microsecond+125*time.Nanosecond,
		401,
		"127.0.0.1",
		"GET",
		"/health/live",
		"3a7b46c26c547a1986b8e197e0057f2a",
	)

	want := `[Aurora] 2026/10/09 - 23:07:49 | 401 | 517.125µs |       127.0.0.1 | GET     "/health/live" trace_id=3a7b46c26c547a1986b8e197e0057f2a
`
	if got != want {
		t.Fatalf("formatted access log = %q, want %q", got, want)
	}
}

func TestFormatAccessLogOmitsEmptyTraceID(t *testing.T) {
	got := formatAccessLog(time.Time{}, time.Millisecond, 200, "127.0.0.1", "GET", "/health", "")
	if strings.Contains(got, "trace_id=") {
		t.Fatalf("empty trace ID was written: %q", got)
	}
}

func TestAccessLogMiddlewareUsesProjectLogger(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "access.log")
	log, err := corelogger.New(&corelogger.Config{
		Output:          corelogger.OutputFile,
		File:            logPath,
		StacktraceLevel: corelogger.StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("GET", "/health/live", nil)
	ginContext.Set("trace_id", "trace-123")
	ginContext.Writer.WriteHeader(401)
	c := &Context{ctx: ginContext}
	AccessLogMiddleware(log)(c)
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(content)
	if !strings.Contains(output, "[Aurora]") || !strings.Contains(output, "trace_id=trace-123") {
		t.Fatalf("access log = %q", output)
	}
	if strings.Contains(output, `"msg"`) {
		t.Fatalf("access log unexpectedly has JSON wrapper: %q", output)
	}
}

// ===== middleware_accesslog_test.go =====

// TestAccessLogRecordsPanicRequests 验证 panic 请求会记录访问日志
func TestAccessLogRecordsPanicRequests(t *testing.T) {
	// 创建临时日志文件
	logPath := filepath.Join(t.TempDir(), "test.log")
	log, err := logger.New(&logger.Config{
		Output:          logger.OutputFile,
		File:            logPath,
		StacktraceLevel: logger.StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.Mode = "test"
	engine, err := New(cfg, WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}

	// 注册一个会 panic 的 handler
	engine.GET("/panic", func(c *Context) {
		panic("test panic")
	})

	// 发送请求
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	engine.ServeHTTP(w, req)

	// 检查响应状态码（应该是 500）
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}

	// 同步日志
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}

	// 读取日志文件
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logOutput := string(content)
	t.Logf("Log output:\n%s", logOutput)

	// 应该包含访问日志
	if !strings.Contains(logOutput, "[Aurora]") {
		t.Error("access log not found in output")
	}

	// 应该包含 panic 错误日志
	if !strings.Contains(logOutput, "panic recovered") {
		t.Error("panic recovery log not found in output")
	}

	// 访问日志应该记录 500 状态码
	if !strings.Contains(logOutput, "500") {
		t.Error("status code 500 not found in access log")
	}
}

// TestAccessLogWithNilLogger 验证 nil logger 时的行为
func TestAccessLogWithNilLogger(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "test"
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	engine.GET("/test", func(c *Context) {
		c.OK("ok")
	})

	// 发送请求（不应该 panic）
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	engine.ServeHTTP(w, req)

	// 请求应该正常完成
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// 这个测试只是确保不会 panic
	// 但无法验证日志是否被静默丢失（因为没有地方可以检查）
	t.Log("Request completed without panic, but access log is silently dropped")
}

// TestAccessLogWithNilLoggerAndPanic 验证 nil logger + panic 的组合
func TestAccessLogWithNilLoggerAndPanic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "test"
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	engine.GET("/panic", func(c *Context) {
		panic("test panic")
	})

	// 发送请求（不应该 panic，应该被 recovery 捕获）
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	engine.ServeHTTP(w, req)

	// 请求应该返回 500
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}

	// 这个测试确保：
	// 1. panic 被正确 recovery
	// 2. 即使 logger 为 nil 也不会导致程序崩溃
	// 但是 panic 日志和访问日志都会被静默丢失
	t.Log("Panic recovered, but both panic log and access log are silently dropped")
}

// ===== middleware_maxbody_test.go =====

func TestMaxBodyBytesMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		maxBytes       int64
		bodySize       int
		expectStatus   int
		expectContains string
	}{
		{
			name:         "body within limit",
			maxBytes:     100,
			bodySize:     50,
			expectStatus: http.StatusOK,
		},
		{
			name:           "body exceeds limit",
			maxBytes:       100,
			bodySize:       200,
			expectStatus:   http.StatusRequestEntityTooLarge, // 413
			expectContains: "请求体过大",
		},
		{
			name:         "body at exact limit",
			maxBytes:     100,
			bodySize:     100,
			expectStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxBodyBytes = tt.maxBytes
			cfg.Middleware.RequestID = false
			cfg.Middleware.Recovery = false
			cfg.Middleware.AccessLog = false

			e, err := New(cfg)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			// 注册一个接收 JSON 的路由
			type Request struct {
				Data string `json:"data"`
			}
			type Resp struct {
				Message string `json:"message"`
			}
			e.POST("/test", Bind(func(c *Context, req *Request) (*Resp, error) {
				return &Resp{Message: "ok"}, nil
			}))

			// 创建测试请求
			var body []byte
			if tt.bodySize > 0 {
				// 创建一个合法的 JSON，但大小接近限制
				data := strings.Repeat("a", tt.bodySize-20) // 留出 JSON 结构的空间
				body = []byte(`{"data":"` + data + `"}`)
			} else {
				body = []byte(`{"data":""}`)
			}
			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			e.ServeHTTP(w, req)

			if w.Code != tt.expectStatus {
				t.Errorf("status = %d, want %d, body = %s", w.Code, tt.expectStatus, w.Body.String())
			}

			if tt.expectContains != "" && !strings.Contains(strings.ToLower(w.Body.String()), tt.expectContains) {
				t.Errorf("response body should contain %q, got %q", tt.expectContains, w.Body.String())
			}
		})
	}
}

func TestMaxBodyBytesDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxBodyBytes = 0 // 禁用限制
	cfg.Middleware.RequestID = false
	cfg.Middleware.Recovery = false
	cfg.Middleware.AccessLog = false

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	type Request struct {
		Data string `json:"data"`
	}
	type Resp struct {
		Message string `json:"message"`
	}
	e.POST("/test", Bind(func(c *Context, req *Request) (*Resp, error) {
		return &Resp{Message: "ok"}, nil
	}))

	// 创建一个大请求（超过默认的 10MB）
	body := bytes.Repeat([]byte("a"), 1000)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	e.ServeHTTP(w, req)

	// 应该成功（因为禁用了限制）
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected bad request due to invalid JSON, got status %d", w.Code)
	}
}

func TestGetRawDataRespectsMaxBodyBytes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxBodyBytes = 100
	cfg.Middleware.RequestID = false
	cfg.Middleware.Recovery = false
	cfg.Middleware.AccessLog = false

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	e.POST("/test", func(c *Context) {
		data, err := c.GetRawData()
		if err != nil {
			c.Fail(ErrBadRequest.WithErr(err))
			return
		}
		c.OK(map[string]int{"size": len(data)})
	})

	// 创建超过限制的请求
	body := bytes.Repeat([]byte("a"), 200)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	w := httptest.NewRecorder()

	e.ServeHTTP(w, req)

	// 应该返回错误
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ===== middleware_requestid_accesslog_test.go =====

func TestAccessLog_IncludesRequestID(t *testing.T) {
	// 创建临时日志文件
	logPath := filepath.Join(t.TempDir(), "test.log")
	log, err := logger.New(&logger.Config{
		Output:          logger.OutputFile,
		File:            logPath,
		StacktraceLevel: logger.StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Mode = "test"
	cfg.Middleware.RequestID = true
	cfg.Middleware.AccessLog = true
	cfg.Middleware.Recovery = false

	engine, err := New(cfg, WithLogger(log))
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	engine.GET("/test", func(c *Context) {
		c.String(200, "ok")
	})

	// 发送请求
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "test-request-id-456")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	// 同步日志
	if err := log.Sync(); err != nil {
		t.Logf("Failed to sync log (may be ok in test): %v", err)
	}

	// 读取日志文件
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	logOutput := string(logBytes)
	t.Logf("Access log output:\n%s", logOutput)

	// 访问日志应该包含 trace_id
	if !strings.Contains(logOutput, "trace_id=test-request-id-456") {
		t.Errorf("Access log should contain 'trace_id=test-request-id-456'")
		t.Errorf("Got: %s", logOutput)
	}

	// 响应头应该包含 X-Request-ID
	responseRequestID := w.Header().Get("X-Request-ID")
	if responseRequestID != "test-request-id-456" {
		t.Errorf("Response X-Request-ID = %s, want test-request-id-456", responseRequestID)
	}
}

// ===== middleware_requestid_test.go =====

func TestRequestIDMiddleware_SetsTraceID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Middleware.RequestID = true // 启用 RequestID 中间件
	cfg.Middleware.AccessLog = false
	cfg.Middleware.Recovery = false

	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	var capturedTraceID string
	var capturedRequestID string
	engine.GET("/test", func(c *Context) {
		capturedTraceID = c.TraceID()
		capturedRequestID = c.RequestID()
		c.String(200, "ok")
	})

	// 测试 1：生成新的 Request ID
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if capturedTraceID == "" {
		t.Error("✗ TraceID() should return the generated request_id, got empty")
		t.Error("Problem: RequestIDMiddleware sets 'request_id', but TraceID() reads 'trace_id'")
	} else {
		t.Logf("✓ TraceID() returned: %s", capturedTraceID)
	}

	responseRequestID := w.Header().Get("X-Request-ID")
	if responseRequestID == "" {
		t.Error("Response should have X-Request-ID header")
	}

	if capturedTraceID != responseRequestID {
		t.Errorf("TraceID() = %s, but X-Request-ID = %s, should match", capturedTraceID, responseRequestID)
	}
	if capturedRequestID != responseRequestID {
		t.Errorf("RequestID() = %s, but X-Request-ID = %s, should match", capturedRequestID, responseRequestID)
	}
}

func TestRequestIDMiddlewareRejectsUnsafeOrOversizedHeader(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.AccessLog = false
	cfg.Middleware.Recovery = false
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.GET("/test", func(c *Context) { c.OK(nil) })

	unsafe := "request id with spaces"
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", unsafe)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if got := w.Header().Get("X-Request-ID"); got == unsafe || got == "" || len(got) > maxRequestIDLength {
		t.Fatalf("unsafe request ID was accepted: %q", got)
	}

	oversized := make([]byte, maxRequestIDLength+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	req = httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", string(oversized))
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if got := w.Header().Get("X-Request-ID"); got == string(oversized) || len(got) > maxRequestIDLength {
		t.Fatalf("oversized request ID was accepted: length=%d", len(got))
	}
}

func TestRequestIDMiddleware_UsesHeaderIfProvided(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Middleware.RequestID = true
	cfg.Middleware.AccessLog = false
	cfg.Middleware.Recovery = false

	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	var capturedTraceID string
	engine.GET("/test", func(c *Context) {
		capturedTraceID = c.TraceID()
		c.String(200, "ok")
	})

	// 测试 2：使用请求头中的 Request ID
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "custom-request-id-123")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if capturedTraceID != "custom-request-id-123" {
		t.Errorf("TraceID() should return custom request_id 'custom-request-id-123', got %s", capturedTraceID)
	}

	responseRequestID := w.Header().Get("X-Request-ID")
	if responseRequestID != "custom-request-id-123" {
		t.Errorf("Response X-Request-ID should be 'custom-request-id-123', got %s", responseRequestID)
	}
}

// ===== middleware_tracing_panic_test.go =====

// testTracingRuntime 是一个简单的 TracingRuntime 实现，用于测试
type testTracingRuntime struct {
	tp         *sdktrace.TracerProvider
	propagator propagation.TextMapPropagator
}

func newTestTracingRuntime(tp *sdktrace.TracerProvider) TracingRuntime {
	return &testTracingRuntime{
		tp:         tp,
		propagator: propagation.TraceContext{},
	}
}

func (t *testTracingRuntime) TracerProvider() oteltrace.TracerProvider {
	return t.tp
}

func (t *testTracingRuntime) Propagator() propagation.TextMapPropagator {
	return t.propagator
}

func (t *testTracingRuntime) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return t.propagator.Extract(ctx, carrier)
}

func (t *testTracingRuntime) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	t.propagator.Inject(ctx, carrier)
}

func (t *testTracingRuntime) Shutdown(ctx context.Context) error {
	return t.tp.Shutdown(ctx)
}

func TestTracingMiddleware_RecordsStatusCodeOnPanic(t *testing.T) {
	// 创建内存 span exporter 用于测试
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)

	// 创建 tracing runtime
	rt := newTestTracingRuntime(tp)

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Mode = "test"
	cfg.Middleware.RequestID = false
	cfg.Middleware.AccessLog = false
	cfg.Middleware.Recovery = true
	cfg.Middleware.Tracing = true

	engine, err := New(cfg, WithTracingRuntime(rt))
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// 注册一个会 panic 的 handler
	engine.GET("/panic", func(c *Context) {
		panic("test panic")
	})

	// 发送请求
	req := httptest.NewRequest("GET", "/panic", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	// 验证响应状态码是 500
	if w.Code != 500 {
		t.Errorf("Expected status 500, got %d", w.Code)
	}

	// 强制刷新 spans
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("Failed to flush spans: %v", err)
	}

	// 获取导出的 spans
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("Expected at least one span, got none")
	}

	// 验证 span 包含 http.status_code 属性
	span := spans[0]
	t.Logf("Span name: %s", span.Name)
	t.Logf("Span attributes: %+v", span.Attributes)
	t.Logf("Span status: %+v", span.Status)

	// 检查是否有 http.status_code 属性
	var hasStatusCode bool
	var statusCode int
	for _, attr := range span.Attributes {
		if attr.Key == "http.status_code" {
			hasStatusCode = true
			statusCode = int(attr.Value.AsInt64())
			break
		}
	}

	if !hasStatusCode {
		t.Error("✗ Span should have http.status_code attribute when panic occurs")
		t.Error("Problem: Tracing middleware is after Recovery, so it exits before recording status_code")
	} else {
		t.Logf("✓ Span has http.status_code: %d", statusCode)
		if statusCode != 500 {
			t.Errorf("Expected http.status_code=500, got %d", statusCode)
		}
	}

	// 可选：验证 span 状态是否标记为 error
	if span.Status.Code != codes.Error {
		t.Logf("Note: Span status is %v, could be marked as Error for 5xx responses", span.Status.Code)
	}
}

func TestTracingMiddleware_RecordsStatusCodeOnSuccess(t *testing.T) {
	// 创建内存 span exporter
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)

	rt := newTestTracingRuntime(tp)

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Mode = "test"
	cfg.Middleware.Tracing = true

	engine, err := New(cfg, WithTracingRuntime(rt))
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	engine.GET("/success", func(c *Context) {
		c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/success", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("Failed to flush spans: %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("Expected at least one span")
	}

	span := spans[0]

	// 验证成功请求也有 status_code
	var hasStatusCode bool
	var statusCode int
	for _, attr := range span.Attributes {
		if attr.Key == "http.status_code" {
			hasStatusCode = true
			statusCode = int(attr.Value.AsInt64())
			break
		}
	}

	if !hasStatusCode {
		t.Error("Span should have http.status_code attribute for successful requests")
	} else if statusCode != 200 {
		t.Errorf("Expected http.status_code=200, got %d", statusCode)
	}
}

// ===== middleware_tracing_test.go =====

// mockTracingRuntime 实现 tracing.Runtime 接口用于测试
type mockTracingRuntime struct {
	tp         *sdktrace.TracerProvider
	propagator propagation.TextMapPropagator
}

func (m *mockTracingRuntime) TracerProvider() trace.TracerProvider {
	return m.tp
}

func (m *mockTracingRuntime) Propagator() propagation.TextMapPropagator {
	return m.propagator
}

func (m *mockTracingRuntime) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	m.propagator.Inject(ctx, carrier)
}

func (m *mockTracingRuntime) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return m.propagator.Extract(ctx, carrier)
}

func (m *mockTracingRuntime) Shutdown(ctx context.Context) error {
	return m.tp.Shutdown(ctx)
}

func TestTracingMiddleware_ExtractUpstreamContext(t *testing.T) {
	// 创建测试用的 span exporter 和 tracer provider
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)),
	)
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")
	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)

	// 包装为 tracing.Runtime（使用匿名结构实现接口）
	rt := &mockTracingRuntime{
		tp:         tp,
		propagator: propagator,
	}

	// 创建上游 span context
	upstreamCtx, upstreamSpan := tracer.Start(context.Background(), "upstream-operation")
	upstreamTraceID := upstreamSpan.SpanContext().TraceID()
	defer upstreamSpan.End()

	// 提取 traceparent header
	carrier := propagation.HeaderCarrier(http.Header{})
	propagator.Inject(upstreamCtx, carrier)
	traceparent := carrier.Get("traceparent")

	if traceparent == "" {
		t.Fatal("Failed to inject traceparent")
	}
	t.Logf("Upstream traceparent: %s", traceparent)
	t.Logf("Upstream trace ID: %s", upstreamTraceID)

	// 创建 Aurora engine（使用当前实现，不提取上游 context）
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	engine, err := New(cfg, WithTracingRuntime(rt))
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// 显式使用 tracing middleware
	engine.Use(TracingMiddleware(rt))

	engine.GET("/test", func(c *Context) {
		// 从 context 中获取当前 span
		span := trace.SpanFromContext(c.Context())
		traceID := span.SpanContext().TraceID()
		t.Logf("Handler span trace ID: %s", traceID)
		c.String(http.StatusOK, "ok")
	})

	// 发送带 traceparent 的请求
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("traceparent", traceparent)
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Flush spans
	tp.ForceFlush(context.Background())

	// 验证：trace context 是否正确提取
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("No spans recorded")
	}

	// 查找 middleware 创建的 span（应该是最后一个）
	middlewareSpan := spans[len(spans)-1]
	middlewareTraceID := middlewareSpan.SpanContext.TraceID()
	t.Logf("Middleware span trace ID: %s", middlewareTraceID)

	// 验证 trace ID 是否与上游一致
	if middlewareTraceID == upstreamTraceID {
		t.Log("✓ Trace context correctly extracted from upstream")
	} else {
		t.Errorf("✗ Trace context NOT extracted: upstream=%s, middleware=%s", upstreamTraceID, middlewareTraceID)
	}
}

func TestTracingMiddleware_SanitizeURL(t *testing.T) {
	// 创建测试用的 span exporter 和 tracer provider
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)),
	)
	defer tp.Shutdown(context.Background())

	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)

	rt := &mockTracingRuntime{
		tp:         tp,
		propagator: propagator,
	}

	// 创建 Aurora engine
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	engine, err := New(cfg, WithTracingRuntime(rt))
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// 使用 tracing middleware
	engine.Use(TracingMiddleware(rt))

	engine.GET("/users/:id", func(c *Context) {
		c.String(http.StatusOK, "ok")
	})

	// 发送带敏感 query 参数的请求
	req := httptest.NewRequest("GET", "/users/123?token=secret123&phone=13800138000", nil)
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Flush spans
	tp.ForceFlush(context.Background())

	// 验证 span 属性中不包含敏感信息
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("No spans recorded")
	}

	lastSpan := spans[len(spans)-1]
	foundURL := false
	for _, attr := range lastSpan.Attributes {
		if attr.Key == "http.url" {
			foundURL = true
			urlValue := attr.Value.AsString()
			t.Logf("http.url attribute: %s", urlValue)

			// 当前实现的问题：包含敏感 query 参数
			if strings.Contains(urlValue, "token") || strings.Contains(urlValue, "secret") || strings.Contains(urlValue, "phone") {
				t.Errorf("✗ http.url contains sensitive query parameters: %s", urlValue)
			} else {
				t.Logf("✓ http.url does not contain sensitive query parameters")
			}
		}
	}

	if !foundURL {
		t.Error("http.url attribute not found in span")
	}
}

func TestMetricsMiddlewareRecordsHTTPMeasurements(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())

	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.Metrics = true
	cfg.Middleware.AccessLog = false
	cfg.Middleware.RequestID = false
	e, err := New(cfg, WithMetricsProvider(provider))
	if err != nil {
		t.Fatal(err)
	}
	e.POST("/users/:id", func(c *Context) { c.String(http.StatusCreated, "created") })

	req := httptest.NewRequest(http.MethodPost, "/users/42?token=secret", strings.NewReader("payload"))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	data := collectHTTPMetrics(t, reader)
	assertActiveRequests(t, data, "POST", "/users/:id")
	assertHTTPMetric(t, data, "http.server.request.duration", "POST", "/users/:id", http.StatusCreated)
	assertHTTPMetric(t, data, "http.server.request.body.size", "POST", "/users/:id", -1)
	assertHTTPMetric(t, data, "http.server.response.body.size", "POST", "/users/:id", http.StatusCreated)
	if strings.Contains(fmt.Sprint(data), "secret") || strings.Contains(fmt.Sprint(data), "/users/42") {
		t.Fatalf("metric data contains query or concrete path: %+v", data)
	}
}

func TestMetricsMiddlewareRecordsRecoveryStatusAndUnmatchedRoute(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())

	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.Metrics = true
	cfg.Middleware.AccessLog = false
	cfg.Middleware.RequestID = false
	e, err := New(cfg, WithMetricsProvider(provider))
	if err != nil {
		t.Fatal(err)
	}
	e.GET("/panic/:id", func(*Context) { panic("test") })
	for _, target := range []string{"/panic/42", "/missing/secret"} {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}

	data := collectHTTPMetrics(t, reader)
	assertHTTPMetric(t, data, "http.server.request.duration", "GET", "/panic/:id", http.StatusInternalServerError)
	assertHTTPMetric(t, data, "http.server.request.duration", "GET", "", http.StatusNotFound)
	if strings.Contains(fmt.Sprint(data), "/panic/42") || strings.Contains(fmt.Sprint(data), "/missing/secret") {
		t.Fatalf("metric data contains concrete or unmatched URL: %+v", data)
	}
}

func TestMetricsMiddlewareRequiresProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Middleware.Metrics = true
	_, err := New(cfg)
	if err == nil || !strings.Contains(err.Error(), "metrics provider is required") {
		t.Fatalf("New() error = %v, want missing metrics provider", err)
	}
}

func collectHTTPMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func assertHTTPMetric(t *testing.T, data metricdata.ResourceMetrics, name, method, route string, status int) {
	t.Helper()
	for _, scope := range data.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != name {
				continue
			}
			switch points := item.Data.(type) {
			case metricdata.Histogram[float64]:
				for _, point := range points.DataPoints {
					if metricPointMatches(point.Attributes, method, route, status) {
						if point.Count != 1 {
							t.Fatalf("%s count = %d, want 1", name, point.Count)
						}
						return
					}
				}
			case metricdata.Histogram[int64]:
				for _, point := range points.DataPoints {
					if metricPointMatches(point.Attributes, method, route, status) {
						if point.Count != 1 {
							t.Fatalf("%s count = %d, want 1", name, point.Count)
						}
						return
					}
				}
			}
			t.Fatalf("metric %q has no point matching method=%q route=%q status=%d", name, method, route, status)
		}
	}
	t.Fatalf("metric %q not found", name)
}

func assertActiveRequests(t *testing.T, data metricdata.ResourceMetrics, method, route string) {
	t.Helper()
	for _, scope := range data.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != "http.server.active_requests" {
				continue
			}
			points, ok := item.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("active requests has unexpected data type %T", item.Data)
			}
			for _, point := range points.DataPoints {
				if metricPointMatches(point.Attributes, method, route, -1) {
					if point.Value != 0 {
						t.Fatalf("active requests = %d, want 0 after request", point.Value)
					}
					return
				}
			}
			t.Fatalf("active requests has no point matching method=%q route=%q", method, route)
		}
	}
	t.Fatal("active requests metric not found")
}

func metricPointMatches(attrs attribute.Set, method, route string, status int) bool {
	gotMethod, hasMethod := attrs.Value("http.request.method")
	gotRoute, hasRoute := attrs.Value("http.route")
	gotStatus, hasStatus := attrs.Value("http.response.status_code")
	if !hasMethod || gotMethod.AsString() != method {
		return false
	}
	if route == "" {
		if hasRoute {
			return false
		}
	} else if !hasRoute || gotRoute.AsString() != route {
		return false
	}
	return status < 0 || (hasStatus && gotStatus.AsInt64() == int64(status))
}
