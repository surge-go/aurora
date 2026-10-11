package aurora

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBindRequestGETCombinesQueryAndURIValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("GET", "/users/42?page=2", nil)
	ginContext.Params = gin.Params{{Key: "id", Value: "42"}}
	c := &Context{ctx: ginContext}

	var req struct {
		ID   string `uri:"id" binding:"required"`
		Page int    `form:"page" binding:"required"`
	}
	if err := bindRequest(c, &req); err != nil {
		t.Fatalf("bindRequest() error = %v", err)
	}
	if req.ID != "42" || req.Page != 2 {
		t.Fatalf("bound request = %+v, want ID=42 and Page=2", req)
	}
}

func TestBindRequestGETRejectsMissingQueryOrURI(t *testing.T) {
	tests := []struct {
		name   string
		target string
		params gin.Params
	}{
		{name: "missing query", target: "/users/42", params: gin.Params{{Key: "id", Value: "42"}}},
		{name: "missing uri", target: "/users?page=2", params: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest("GET", tt.target, nil)
			ginContext.Params = tt.params
			c := &Context{ctx: ginContext}

			var req struct {
				ID   string `uri:"id" binding:"required"`
				Page int    `form:"page" binding:"required"`
			}
			if err := bindRequest(c, &req); err == nil {
				t.Fatal("bindRequest() error = nil, want validation error")
			}
			if recorder.Code != 400 {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
		})
	}
}

// ===== handler_binding_uri_body_test.go =====

// 测试 POST/PUT/PATCH 请求中 URI 参数和 body 参数的绑定顺序问题
func TestBindRequest_POST_URIAndBodyWithRequired(t *testing.T) {
	type UpdateUserRequest struct {
		ID   string `uri:"id" binding:"required"`    // URI 参数
		Name string `json:"name" binding:"required"` // Body 参数
	}

	type UpdateUserResponse struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	engine.PUT("/users/:id", Bind(func(c *Context, req *UpdateUserRequest) (*UpdateUserResponse, error) {
		return &UpdateUserResponse{
			ID:   req.ID,
			Name: req.Name,
		}, nil
	}))

	// 发送合法请求：URI 有 id，body 有 name
	body := map[string]string{"name": "Alice"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/users/123", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	t.Logf("Response status: %d", w.Code)
	t.Logf("Response body: %s", w.Body.String())

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
		t.Errorf("Problem: ShouldBind validated the struct BEFORE URI was bound")
		t.Errorf("So the required URI field 'id' caused validation to fail")
		return
	}

	var respWrapper struct {
		Code    int                `json:"code"`
		Data    UpdateUserResponse `json:"data"`
		Message string             `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &respWrapper); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	resp := respWrapper.Data
	if resp.ID != "123" {
		t.Errorf("Expected ID=123, got %s", resp.ID)
	}

	if resp.Name != "Alice" {
		t.Errorf("Expected Name=Alice, got %s", resp.Name)
	}

	t.Log("✓ POST/PUT/PATCH should bind URI and body, then validate once")
}

func TestBindRequest_YAMLURIAndBodyWithRequired(t *testing.T) {
	type UpdateRequest struct {
		ID   string `uri:"id" binding:"required"`
		Name string `yaml:"name" binding:"required"`
	}

	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.AccessLog = false
	cfg.Middleware.RequestID = false
	cfg.Middleware.Recovery = false
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine.PUT("/users/:id", Bind(func(_ *Context, req *UpdateRequest) (*UpdateRequest, error) {
		return req, nil
	}))

	req := httptest.NewRequest(http.MethodPut, "/users/789", strings.NewReader("name: Alice\n"))
	req.Header.Set("Content-Type", "application/yaml")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Data UpdateRequest `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ID != "789" || response.Data.Name != "Alice" {
		t.Fatalf("bound request = %+v", response.Data)
	}
}

func TestBindRequest_JSONDecoderOptions(t *testing.T) {
	type UpdateRequest struct {
		ID    string `uri:"id" binding:"required"`
		Value any    `json:"value" binding:"required"`
	}
	type DecodeResult struct {
		UsesNumber bool `json:"uses_number"`
	}

	oldUseNumber := binding.EnableDecoderUseNumber
	oldDisallowUnknownFields := binding.EnableDecoderDisallowUnknownFields
	binding.EnableDecoderUseNumber = true
	binding.EnableDecoderDisallowUnknownFields = true
	defer func() {
		binding.EnableDecoderUseNumber = oldUseNumber
		binding.EnableDecoderDisallowUnknownFields = oldDisallowUnknownFields
	}()

	cfg := DefaultConfig()
	cfg.Mode = "test"
	cfg.Middleware.AccessLog = false
	cfg.Middleware.RequestID = false
	cfg.Middleware.Recovery = false
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine.PUT("/values/:id", Bind(func(_ *Context, req *UpdateRequest) (*DecodeResult, error) {
		_, usesNumber := req.Value.(json.Number)
		return &DecodeResult{UsesNumber: usesNumber}, nil
	}))

	req := httptest.NewRequest(http.MethodPut, "/values/1", strings.NewReader(`{"value":123}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Data DecodeResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Data.UsesNumber {
		t.Fatal("JSON decoder UseNumber option was not preserved")
	}

	req = httptest.NewRequest(http.MethodPut, "/values/1", strings.NewReader(`{"value":123,"unknown":true}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestBindRequest_PATCH_URIAndBodyWithRequired(t *testing.T) {
	type PatchUserRequest struct {
		UserID string `uri:"user_id" binding:"required"` // URI 参数
		Email  string `json:"email" binding:"required"`  // Body 参数
	}

	type PatchUserResponse struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	engine.PATCH("/users/:user_id/email", Bind(func(c *Context, req *PatchUserRequest) (*PatchUserResponse, error) {
		return &PatchUserResponse{
			UserID: req.UserID,
			Email:  req.Email,
		}, nil
	}))

	// 发送合法请求
	body := map[string]string{"email": "alice@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/users/456/email", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d. Response: %s", w.Code, w.Body.String())
		t.Error("URI binding should happen before validation")
		return
	}

	var respWrapper struct {
		Code    int               `json:"code"`
		Data    PatchUserResponse `json:"data"`
		Message string            `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &respWrapper); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	resp := respWrapper.Data
	if resp.UserID != "456" {
		t.Errorf("Expected UserID=456, got %s", resp.UserID)
	}

	if resp.Email != "alice@example.com" {
		t.Errorf("Expected Email=alice@example.com, got %s", resp.Email)
	}
}
