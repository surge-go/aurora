package aurora

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

// Context 是 Aurora 对 Gin 请求上下文的封装。
//
// Context 只在当前请求处理期间有效，不应被业务代码保存到全局或异步
// goroutine 中。需要跨层传递时使用 Context() 返回的标准库 context.Context。
// 部分只读方法对 nil Context 做了保护，便于错误路径和测试辅助代码安全调用；
// 请求处理方法仍要求由框架创建的有效 Context。
type Context struct{ ctx *gin.Context }

// ============================================================================
// 流程控制
// ============================================================================

// Next 执行当前 Gin handler 链中尚未执行的下一个中间件/Handler。
func (c *Context) Next() {
	c.ctx.Next()
}

// Abort 将当前请求标记为中止，后续 Handler 不再执行；已经写出的响应不会被回滚。
func (c *Context) Abort() {
	c.ctx.Abort()
}

// IsAborted 检查是否已中止。
func (c *Context) IsAborted() bool {
	return c.ctx.IsAborted()
}

// ============================================================================
// 请求参数获取
// ============================================================================

// Param 获取路径参数。
func (c *Context) Param(key string) string {
	return c.ctx.Param(key)
}

// Query 获取 query 参数。
func (c *Context) Query(key string) string {
	return c.ctx.Query(key)
}

// DefaultQuery 获取 query 参数，如果不存在则返回默认值。
func (c *Context) DefaultQuery(key, defaultValue string) string {
	return c.ctx.DefaultQuery(key, defaultValue)
}

// GetQuery 获取 query 参数并返回是否存在。
func (c *Context) GetQuery(key string) (string, bool) {
	return c.ctx.GetQuery(key)
}

// QueryArray 获取 query 参数数组。
func (c *Context) QueryArray(key string) []string {
	return c.ctx.QueryArray(key)
}

// GetQueryArray 获取 query 参数数组并返回是否存在。
func (c *Context) GetQueryArray(key string) ([]string, bool) {
	return c.ctx.GetQueryArray(key)
}

// QueryMap 获取 query 参数 map。
func (c *Context) QueryMap(key string) map[string]string {
	return c.ctx.QueryMap(key)
}

// GetQueryMap 获取 query 参数 map 并返回是否存在。
func (c *Context) GetQueryMap(key string) (map[string]string, bool) {
	return c.ctx.GetQueryMap(key)
}

// PostForm 获取表单参数。
func (c *Context) PostForm(key string) string {
	return c.ctx.PostForm(key)
}

// DefaultPostForm 获取表单参数，如果不存在则返回默认值。
func (c *Context) DefaultPostForm(key, defaultValue string) string {
	return c.ctx.DefaultPostForm(key, defaultValue)
}

// GetPostForm 获取表单参数并返回是否存在。
func (c *Context) GetPostForm(key string) (string, bool) {
	return c.ctx.GetPostForm(key)
}

// PostFormArray 获取表单参数数组。
func (c *Context) PostFormArray(key string) []string {
	return c.ctx.PostFormArray(key)
}

// GetPostFormArray 获取表单参数数组并返回是否存在。
func (c *Context) GetPostFormArray(key string) ([]string, bool) {
	return c.ctx.GetPostFormArray(key)
}

// PostFormMap 获取表单参数 map。
func (c *Context) PostFormMap(key string) map[string]string {
	return c.ctx.PostFormMap(key)
}

// GetPostFormMap 获取表单参数 map 并返回是否存在。
func (c *Context) GetPostFormMap(key string) (map[string]string, bool) {
	return c.ctx.GetPostFormMap(key)
}

// FormFile 获取上传的文件。
func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	return c.ctx.FormFile(name)
}

// MultipartForm 获取完整的 multipart form。
func (c *Context) MultipartForm() (*multipart.Form, error) {
	return c.ctx.MultipartForm()
}

// Header 获取请求头。
func (c *Context) Header(key string) string {
	return c.ctx.GetHeader(key)
}

// GetHeader 获取请求头（别名）。
func (c *Context) GetHeader(key string) string {
	return c.ctx.GetHeader(key)
}

// ContentType 获取 Content-Type。
func (c *Context) ContentType() string {
	return c.ctx.ContentType()
}

// Cookie 获取 Cookie。
func (c *Context) Cookie(name string) (string, error) {
	return c.ctx.Cookie(name)
}

// ClientIP 获取客户端 IP。
func (c *Context) ClientIP() string {
	return c.ctx.ClientIP()
}

// GetRawData 获取原始请求体。
func (c *Context) GetRawData() ([]byte, error) {
	if c == nil || c.ctx == nil {
		return nil, errors.New("nil context")
	}
	return c.ctx.GetRawData()
}

// FullPath 获取路由完整路径（如 /user/:id）。
func (c *Context) FullPath() string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.FullPath()
}

// ============================================================================
// 参数绑定（Should 系列在失败时直接写入统一错误响应并中止请求）
// ============================================================================

// ShouldBind 自动绑定请求参数（根据 HTTP 方法和 Content-Type）。
// 绑定失败会立即写入 ErrBadRequest/ErrPayloadTooLarge 并 Abort，同时返回原始错误。
func (c *Context) ShouldBind(obj any) error {
	if c == nil || c.ctx == nil {
		return errors.New("nil context")
	}
	if err := c.ctx.ShouldBind(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindJSON 绑定 JSON 请求体。
func (c *Context) ShouldBindJSON(obj any) error {
	if err := c.ctx.ShouldBindJSON(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindXML 绑定 XML 请求体。
func (c *Context) ShouldBindXML(obj any) error {
	if err := c.ctx.ShouldBindXML(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindYAML 绑定 YAML 请求体。
func (c *Context) ShouldBindYAML(obj any) error {
	if err := c.ctx.ShouldBindYAML(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindTOML 绑定 TOML 请求体。
func (c *Context) ShouldBindTOML(obj any) error {
	if err := c.ctx.ShouldBindTOML(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindHeader 绑定请求头。
func (c *Context) ShouldBindHeader(obj any) error {
	if err := c.ctx.ShouldBindHeader(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindQuery 绑定 query 参数。
func (c *Context) ShouldBindQuery(obj any) error {
	if err := c.ctx.ShouldBindQuery(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ShouldBindUri 绑定 URI 路径参数。
func (c *Context) ShouldBindUri(obj any) error {
	if err := c.ctx.ShouldBindUri(obj); err != nil {
		failBinding(c, err)
		return err
	}
	return nil
}

// ============================================================================
// 响应渲染
// ============================================================================

// JSON 返回 JSON 响应。该方法直接使用底层 Gin 编码，不会自动套用 Aurora Response。
func (c *Context) JSON(code int, obj any) {
	c.ctx.JSON(code, obj)
}

// XML 返回 XML 响应。
func (c *Context) XML(code int, obj any) {
	c.ctx.XML(code, obj)
}

// YAML 返回 YAML 响应。
func (c *Context) YAML(code int, obj any) {
	c.ctx.YAML(code, obj)
}

// String 返回纯文本响应。
func (c *Context) String(code int, format string, values ...any) {
	c.ctx.String(code, format, values...)
}

// HTML 返回 HTML 响应。
func (c *Context) HTML(code int, name string, obj any) {
	c.ctx.HTML(code, name, obj)
}

// Data 返回原始数据响应。
func (c *Context) Data(code int, contentType string, data []byte) {
	c.ctx.Data(code, contentType, data)
}

// File 返回文件。
func (c *Context) File(filepath string) {
	c.ctx.File(filepath)
}

// FileAttachment 返回文件作为附件。
func (c *Context) FileAttachment(filepath, filename string) {
	c.ctx.FileAttachment(filepath, filename)
}

// Redirect 重定向。
func (c *Context) Redirect(code int, location string) {
	c.ctx.Redirect(code, location)
}

// Stream 流式响应。
func (c *Context) Stream(step func(w io.Writer) bool) bool {
	return c.ctx.Stream(step)
}

// Status 设置响应状态码。状态码一旦随响应头写出，后续调用无法改变客户端已收到的状态。
func (c *Context) Status(code int) {
	c.ctx.Status(code)
}

// SetHeader 设置响应头。
func (c *Context) SetHeader(key, value string) {
	c.ctx.Header(key, value)
}

// ============================================================================
// Context 状态管理
// ============================================================================

// Set 设置值到 Context。
func (c *Context) Set(key string, value any) {
	if c == nil || c.ctx == nil {
		return
	}
	c.ctx.Set(key, value)
}

// Get 获取值。
func (c *Context) Get(key string) (any, bool) {
	if c == nil || c.ctx == nil {
		return nil, false
	}
	return c.ctx.Get(key)
}

// MustGet 获取值（不存在会 panic）。
func (c *Context) MustGet(key string) any {
	return c.ctx.MustGet(key)
}

// GetString 类型安全获取 string。
func (c *Context) GetString(key string) string {
	return c.ctx.GetString(key)
}

// GetBool 类型安全获取 bool。
func (c *Context) GetBool(key string) bool {
	return c.ctx.GetBool(key)
}

// GetInt 类型安全获取 int。
func (c *Context) GetInt(key string) int {
	return c.ctx.GetInt(key)
}

// GetInt64 类型安全获取 int64。
func (c *Context) GetInt64(key string) int64 {
	return c.ctx.GetInt64(key)
}

// GetFloat64 类型安全获取 float64。
func (c *Context) GetFloat64(key string) float64 {
	return c.ctx.GetFloat64(key)
}

// GetTime 类型安全获取 time.Time。
func (c *Context) GetTime(key string) time.Time {
	return c.ctx.GetTime(key)
}

// GetDuration 类型安全获取 time.Duration。
func (c *Context) GetDuration(key string) time.Duration {
	return c.ctx.GetDuration(key)
}

// GetStringSlice 类型安全获取 []string。
func (c *Context) GetStringSlice(key string) []string {
	return c.ctx.GetStringSlice(key)
}

// GetStringMap 类型安全获取 map[string]any。
func (c *Context) GetStringMap(key string) map[string]any {
	return c.ctx.GetStringMap(key)
}

// GetStringMapString 类型安全获取 map[string]string。
func (c *Context) GetStringMapString(key string) map[string]string {
	return c.ctx.GetStringMapString(key)
}

// TraceID 获取链路追踪ID
func (c *Context) TraceID() string {
	if c == nil || c.ctx == nil {
		return ""
	}

	// 首先尝试从 OpenTelemetry span context 中提取
	spanCtx := trace.SpanFromContext(c.Context()).SpanContext()
	if spanCtx.IsValid() {
		return spanCtx.TraceID().String()
	}

	// 降级：从上下文存储中读取（用于手动设置的情况）
	traceID := c.GetString("trace_id")
	return traceID
}

// RequestID 获取当前请求的请求标识。
func (c *Context) RequestID() string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.GetString("request_id")
}

// SetTraceID 设置链路追踪ID
func (c *Context) SetTraceID(traceID string) {
	c.Set("trace_id", traceID)
}

// ============================================================================
// Cookie 操作
// ============================================================================

// SetCookie 设置 Cookie。
func (c *Context) SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) {
	c.ctx.SetCookie(name, value, maxAge, path, domain, secure, httpOnly)
}

// ============================================================================
// 上下文与底层访问
// ============================================================================

// Request 返回底层的 *http.Request。
func (c *Context) Request() *http.Request {
	if c == nil || c.ctx == nil {
		return nil
	}
	return c.ctx.Request
}

// Context 返回标准库的 context.Context（用于传递给业务层和基础设施）。
// 它始终来自当前 HTTP 请求，因此会继承请求取消、超时和 TracingMiddleware 注入的 Span。
func (c *Context) Context() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx.Request.Context()
}

// Writer 返回 ResponseWriter。
func (c *Context) Writer() gin.ResponseWriter {
	if c == nil || c.ctx == nil {
		return nil
	}
	return c.ctx.Writer
}

// SetWriter 设置 ResponseWriter（用于中间件如 Gzip 替换 Writer）。
func (c *Context) SetWriter(w gin.ResponseWriter) {
	if c == nil || c.ctx == nil {
		return
	}
	c.ctx.Writer = w
}

// ============================================================================
// 错误处理
// ============================================================================

// Error 添加错误到 Gin 的错误列表。它只记录错误，不会自动生成 HTTP 响应；
// 需要结束请求时请使用 Fail。
func (c *Context) Error(err error) *gin.Error {
	return c.ctx.Error(err)
}

// ============================================================================
// 其他工具方法
// ============================================================================

// HandlerName 获取当前 Handler 名称，主要用于调试和诊断日志。
func (c *Context) HandlerName() string { return c.ctx.HandlerName() }

// IsWebsocket 检查是否为 WebSocket 请求。
func (c *Context) IsWebsocket() bool { return c.ctx.IsWebsocket() }

// SaveUploadedFile 将 multipart 上传文件保存到目标路径。
func (c *Context) SaveUploadedFile(file *multipart.FileHeader, dst string) error {
	return c.ctx.SaveUploadedFile(file, dst)
}

// ============================================================================
// Aurora 统一响应方法
// ============================================================================

// OK 返回 Aurora 统一成功响应，HTTP 状态码固定为 200。
// data 为业务数据，message 为可选的提示信息（默认为 "success"）。
func (c *Context) OK(data any, message ...string) {
	msg := "success"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	c.respond(http.StatusOK, 200, msg, data)
}

// Fail 返回 Aurora 统一失败响应并中止请求。
// Aurora Error 使用自身的状态码和业务码；未知 error 映射为 ErrInternal，
// 原始错误不会出现在客户端响应中。
func (c *Context) Fail(err error) {
	if err == nil {
		c.respond(ErrInternal.status, ErrInternal.Code, "unknown", nil)
		c.Abort()
		return
	}

	// 检查是否是 Aurora Error 类型
	var auroraErr *Error
	if errors.As(err, &auroraErr) {
		// 使用 Aurora Error 的状态码和业务错误码
		c.respond(auroraErr.Status(), auroraErr.Code, auroraErr.Message, nil)
		c.Abort()
		return
	}

	// 未知错误，映射为内部错误
	c.respond(ErrInternal.status, ErrInternal.Code, ErrInternal.Message, nil)
	c.Abort()
}

// respond 是统一响应的唯一写入口，负责从当前 Context 自动填充 trace_id。
func (c *Context) respond(httpStatus int, code int, msg string, data any) {
	traceID := c.TraceID()
	resp := NewResponse(code, data, msg, traceID)
	c.ctx.JSON(httpStatus, resp)
}
