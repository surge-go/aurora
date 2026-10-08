package aurora

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

func testContext(t *testing.T, method, target string) (*Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(method, target, nil)
	return newContext(ginContext), recorder
}

func TestContextRequestAndValues(t *testing.T) {
	c, _ := testContext(t, "GET", "/users/42?page=2&page=3")
	c.Gin().Params = gin.Params{{Key: "id", Value: "42"}}
	c.Gin().Set("role", "admin")

	if got := c.Param("id"); got != "42" {
		t.Fatalf("Param(id) = %q, want 42", got)
	}
	if got := c.Query("page"); got != "2" {
		t.Fatalf("Query(page) = %q, want 2", got)
	}
	if got := c.QueryArray("page"); len(got) != 2 || got[1] != "3" {
		t.Fatalf("QueryArray(page) = %v, want [2 3]", got)
	}
	if got := c.Header("X-Missing"); got != "" {
		t.Fatalf("Header(X-Missing) = %q, want empty", got)
	}
	if got, ok := c.Get("role"); !ok || got != "admin" {
		t.Fatalf("Get(role) = (%v, %t), want (admin, true)", got, ok)
	}

	requestContext := context.WithValue(c.Context(), contextKey("key"), "value")
	c.Gin().Request = c.Request().WithContext(requestContext)
	if got := c.Context().Value(contextKey("key")); got != "value" {
		t.Fatalf("Context().Value(key) = %v, want value", got)
	}
}

func TestContextBindingAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("POST", "/users", strings.NewReader(`{"name":"alice"}`))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	c := newContext(ginContext)

	var request struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.BindJSON(&request); err != nil {
		t.Fatalf("BindJSON() error = %v", err)
	}
	if request.Name != "alice" {
		t.Fatalf("bound name = %q, want alice", request.Name)
	}

	c.SetHeader("X-Test", "yes")
	if got := recorder.Header().Get("X-Test"); got != "yes" {
		t.Fatalf("response X-Test = %q, want yes", got)
	}
}

func TestContextOKIncludesTraceID(t *testing.T) {
	c, recorder := testContext(t, "GET", "/health")
	wantTraceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    wantTraceID,
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	c.Gin().Request = c.Request().WithContext(trace.ContextWithRemoteSpanContext(c.Context(), parent))

	c.OK(map[string]string{"status": "ok"})

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response Response[map[string]string]
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.TraceID != wantTraceID.String() {
		t.Fatalf("trace_id = %q, want %q", response.TraceID, wantTraceID.String())
	}
	if response.Data["status"] != "ok" {
		t.Fatalf("response data = %v, want status=ok", response.Data)
	}
}

func TestContextFailHidesCause(t *testing.T) {
	c, recorder := testContext(t, "GET", "/users/1")
	c.Fail(WrapWithStatus(errors.New("secret sql details"), 2001, 422, "user is invalid"))

	if recorder.Code != 422 {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "secret sql details") {
		t.Fatalf("response leaked error cause: %s", body)
	}
	if !strings.Contains(body, "user is invalid") {
		t.Fatalf("response omitted safe message: %s", body)
	}
}

func TestContextFailUnknownErrorUsesInternal(t *testing.T) {
	c, recorder := testContext(t, "GET", "/failure")
	c.Fail(errors.New("internal secret"))

	if recorder.Code != 500 {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "internal secret") || !strings.Contains(body, ErrInternal.Message) {
		t.Fatalf("unexpected internal error response: %s", body)
	}
}

func TestNilContextIsSafe(t *testing.T) {
	var c *Context
	if c.Gin() != nil || c.Request() != nil || c.FullPath() != "" || c.TraceID() != "" {
		t.Fatal("nil context accessors returned unexpected values")
	}
	if c.Context() == nil {
		t.Fatal("nil context did not return safe zero values")
	}
	if value, ok := c.Get("missing"); value != nil || ok {
		t.Fatalf("nil context Get(missing) = (%v, %t), want (nil, false)", value, ok)
	}
	if err := c.Bind(nil); err == nil {
		t.Fatal("Bind(nil context) error = nil")
	}
}

type contextKey string
