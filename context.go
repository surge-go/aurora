package aurora

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

// Context is Aurora's HTTP context. Gin remains available through Gin for
// framework-specific operations.
type Context struct {
	ctx *gin.Context
}

var _ context.Context = (*Context)(nil)

// newContext wraps a Gin context for use by Aurora's handler adapter.
func newContext(ctx *gin.Context) *Context {
	if ctx == nil {
		return nil
	}
	return &Context{ctx: ctx}
}

// Gin returns the underlying Gin context.
func (c *Context) Gin() *gin.Context {
	if c == nil {
		return nil
	}
	return c.ctx
}

// Request returns the underlying HTTP request.
func (c *Context) Request() *http.Request {
	if c == nil || c.ctx == nil {
		return nil
	}
	return c.ctx.Request
}

// Context returns the request's standard context. It never returns nil.
func (c *Context) Context() context.Context {
	if request := c.Request(); request != nil {
		return request.Context()
	}
	return context.Background()
}

// Copy returns a copy suitable for passing to a goroutine. The copy must not
// be used to write the original response after the request finishes.
func (c *Context) Copy() *Context {
	if c == nil || c.ctx == nil {
		return nil
	}
	return newContext(c.ctx.Copy())
}

// Deadline, Done, Err and Value expose request cancellation and values while
// keeping the explicit Context() method as the preferred business API.
func (c *Context) Deadline() (time.Time, bool) { return c.Context().Deadline() }
func (c *Context) Done() <-chan struct{}       { return c.Context().Done() }
func (c *Context) Err() error                  { return c.Context().Err() }
func (c *Context) Value(key any) any           { return c.Context().Value(key) }

// Next executes the remaining handlers in the current chain.
func (c *Context) Next() {
	if c != nil && c.ctx != nil {
		c.ctx.Next()
	}
}

// Abort prevents remaining handlers in the current chain from running.
func (c *Context) Abort() {
	if c != nil && c.ctx != nil {
		c.ctx.Abort()
	}
}

// IsAborted reports whether the current handler chain has been aborted.
func (c *Context) IsAborted() bool {
	return c != nil && c.ctx != nil && c.ctx.IsAborted()
}

// FullPath returns the registered route template, such as /users/:id.
func (c *Context) FullPath() string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.FullPath()
}

// Param returns a path parameter.
func (c *Context) Param(name string) string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.Param(name)
}

// Query returns a query parameter or an empty string when absent.
func (c *Context) Query(name string) string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.Query(name)
}

// DefaultQuery returns a query parameter or defaultValue when absent.
func (c *Context) DefaultQuery(name, defaultValue string) string {
	if c == nil || c.ctx == nil {
		return defaultValue
	}
	return c.ctx.DefaultQuery(name, defaultValue)
}

// GetQuery returns a query parameter and whether it was present.
func (c *Context) GetQuery(name string) (string, bool) {
	if c == nil || c.ctx == nil {
		return "", false
	}
	return c.ctx.GetQuery(name)
}

// QueryArray returns all values for a query parameter.
func (c *Context) QueryArray(name string) []string {
	if c == nil || c.ctx == nil {
		return nil
	}
	return c.ctx.QueryArray(name)
}

// QueryMap returns a query parameter map.
func (c *Context) QueryMap(name string) map[string]string {
	if c == nil || c.ctx == nil {
		return nil
	}
	return c.ctx.QueryMap(name)
}

// PostForm returns a form value or an empty string when absent.
func (c *Context) PostForm(name string) string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.PostForm(name)
}

// GetPostForm returns a form value and whether it was present.
func (c *Context) GetPostForm(name string) (string, bool) {
	if c == nil || c.ctx == nil {
		return "", false
	}
	return c.ctx.GetPostForm(name)
}

// FormFile returns an uploaded file header.
func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	if c == nil || c.ctx == nil {
		return nil, errors.New("aurora: context is nil")
	}
	return c.ctx.FormFile(name)
}

// SaveUploadedFile stores an uploaded file at dst.
func (c *Context) SaveUploadedFile(file *multipart.FileHeader, dst string, perm ...os.FileMode) error {
	if c == nil || c.ctx == nil {
		return errors.New("aurora: context is nil")
	}
	return c.ctx.SaveUploadedFile(file, dst, perm...)
}

// Bind binds the request according to its Content-Type.
func (c *Context) Bind(value any) error {
	if c == nil || c.ctx == nil {
		return errors.New("aurora: context is nil")
	}
	return c.ctx.ShouldBind(value)
}

// BindJSON binds a JSON request body.
func (c *Context) BindJSON(value any) error {
	if c == nil || c.ctx == nil {
		return errors.New("aurora: context is nil")
	}
	return c.ctx.ShouldBindJSON(value)
}

// BindQuery binds query parameters.
func (c *Context) BindQuery(value any) error {
	if c == nil || c.ctx == nil {
		return errors.New("aurora: context is nil")
	}
	return c.ctx.ShouldBindQuery(value)
}

// BindURI binds path parameters.
func (c *Context) BindURI(value any) error {
	if c == nil || c.ctx == nil {
		return errors.New("aurora: context is nil")
	}
	return c.ctx.ShouldBindUri(value)
}

// ContentType returns the request Content-Type without parameters.
func (c *Context) ContentType() string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.ContentType()
}

// GetRawData reads the request body.
func (c *Context) GetRawData() ([]byte, error) {
	if c == nil || c.ctx == nil {
		return nil, errors.New("aurora: context is nil")
	}
	return c.ctx.GetRawData()
}

// ReadBody reads the remaining request body for custom parsers.
func (c *Context) ReadBody() ([]byte, error) {
	request := c.Request()
	if request == nil || request.Body == nil {
		return nil, errors.New("aurora: request body is nil")
	}
	return io.ReadAll(request.Body)
}

// Header returns a request header.
func (c *Context) Header(name string) string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.GetHeader(name)
}

// SetHeader sets a response header.
func (c *Context) SetHeader(name, value string) {
	if c != nil && c.ctx != nil {
		c.ctx.Header(name, value)
	}
}

// ClientIP returns the client address according to Gin's trusted proxy rules.
func (c *Context) ClientIP() string {
	if c == nil || c.ctx == nil {
		return ""
	}
	return c.ctx.ClientIP()
}

// Set stores a request-scoped value in the Gin context.
func (c *Context) Set(key string, value any) {
	if c != nil && c.ctx != nil {
		c.ctx.Set(key, value)
	}
}

// Get reads a request-scoped value.
func (c *Context) Get(key string) (any, bool) {
	if c == nil || c.ctx == nil {
		return nil, false
	}
	return c.ctx.Get(key)
}

// MustGet reads a request-scoped value and panics if it is missing, matching
// Gin's behavior.
func (c *Context) MustGet(key string) any {
	if c == nil || c.ctx == nil {
		panic("aurora: context is nil")
	}
	return c.ctx.MustGet(key)
}

// Delete removes a request-scoped value.
func (c *Context) Delete(key string) {
	if c != nil && c.ctx != nil {
		c.ctx.Delete(key)
	}
}

// JSON writes an unwrapped JSON response.
func (c *Context) JSON(status int, value any) {
	if c != nil && c.ctx != nil {
		c.ctx.JSON(status, value)
	}
}

// OK writes a successful unified response.
func (c *Context) OK(data any, message ...string) {
	msg := "success"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	c.respond(http.StatusOK, 0, msg, data)
}

// Fail writes a safe unified error response. The cause of an Aurora Error is
// deliberately excluded from the response body.
func (c *Context) Fail(err error) {
	if err == nil {
		c.OK(nil)
		return
	}

	var frameworkErr *Error
	if errors.As(err, &frameworkErr) && frameworkErr != nil {
		c.respond(frameworkErr.Status(), frameworkErr.Code, frameworkErr.Message, nil)
		return
	}
	c.respond(ErrInternal.Status(), ErrInternal.Code, ErrInternal.Message, nil)
}

// FailWithCode writes a unified error response without constructing an Error.
func (c *Context) FailWithCode(code, status int, message string) {
	c.respond(status, code, message, nil)
}

// Page writes a successful paginated response.
func (c *Context) Page(total int64, list any, message ...string) {
	c.OK(map[string]any{"total": total, "list": list}, message...)
}

// AbortWithStatus aborts the current chain and writes a status-only response.
func (c *Context) AbortWithStatus(status int) {
	if c != nil && c.ctx != nil {
		c.ctx.AbortWithStatus(status)
	}
}

// AbortWithStatusJSON aborts the current chain and writes an unwrapped JSON response.
func (c *Context) AbortWithStatusJSON(status int, value any) {
	if c != nil && c.ctx != nil {
		c.ctx.AbortWithStatusJSON(status, value)
	}
}

// TraceID returns the current trace ID, if a valid span or middleware value exists.
func (c *Context) TraceID() string {
	if value, ok := c.Get("trace_id"); ok {
		if traceID, ok := value.(string); ok && traceID != "" {
			return traceID
		}
	}
	spanContext := trace.SpanContextFromContext(c.Context())
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func (c *Context) respond(status, code int, message string, data any) {
	if c == nil || c.ctx == nil {
		return
	}
	c.ctx.JSON(status, &Response[any]{
		Code:    code,
		Data:    data,
		Message: message,
		TraceID: c.TraceID(),
	})
}
