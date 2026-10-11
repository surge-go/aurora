package aurora

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"reflect"
	"runtime"

	"github.com/gin-gonic/gin/binding"
	ginjson "github.com/gin-gonic/gin/codec/json"
	"github.com/goccy/go-yaml"
	"github.com/pelletier/go-toml/v2"
	"github.com/ugorji/go/codec"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
)

// HandlerFunc 是 Aurora 的 Handler 函数类型。
type HandlerFunc func(*Context)

// Middleware 是中间件类型，本质上和 HandlerFunc 相同。
type Middleware = HandlerFunc

// HandlerWithMeta 包装 HandlerFunc 并携带业务函数元信息。
// 元信息只用于路由注册日志，Handler 本身仍由 Gin 链路执行。
type HandlerWithMeta struct {
	Handler          HandlerFunc
	BusinessFuncName string
}

// wrapHandlerWithMeta 包装 handler 并附加业务函数元信息。
// 泛型函数的运行时名称通过反射读取，读取失败时保留可执行的 handler，
// 只放弃日志中的业务函数名。
func wrapHandlerWithMeta(handler HandlerFunc, businessFunc any) *HandlerWithMeta {
	if handler == nil || businessFunc == nil {
		return &HandlerWithMeta{Handler: handler}
	}

	function := runtime.FuncForPC(reflect.ValueOf(businessFunc).Pointer())
	if function == nil {
		return &HandlerWithMeta{Handler: handler}
	}

	return &HandlerWithMeta{
		Handler:          handler,
		BusinessFuncName: function.Name(),
	}
}

// ============================================================================
// 泛型绑定适配器
// ============================================================================

// Bind 将业务函数适配为 HandlerFunc。
// 业务函数接收 Aurora Context 和请求参数，返回响应数据和错误。
//
// 适用场景：需要请求参数，有返回值
// 示例：func(c *aurora.Context, req *CreateUserReq) (*User, error)
func Bind[Req any, Resp any](
	fn func(c *Context, req *Req) (*Resp, error),
) *HandlerWithMeta {
	handler := HandlerFunc(func(c *Context) {
		var req Req
		if err := bindRequest(c, &req); err != nil {
			return // bindRequest 内部已调用 c.Fail
		}

		resp, err := fn(c, &req)
		if err != nil {
			c.Fail(err)
			return
		}

		c.OK(resp)
	})
	return wrapHandlerWithMeta(handler, fn)
}

// BindR 将无请求参数的业务函数适配为 HandlerFunc。
// 业务函数只接收 Aurora Context，返回响应数据和错误。
//
// 适用场景：无请求参数，有返回值
// 示例：func(c *aurora.Context) (*UserList, error)
func BindR[Resp any](
	fn func(c *Context) (*Resp, error),
) *HandlerWithMeta {
	handler := HandlerFunc(func(c *Context) {
		resp, err := fn(c)
		if err != nil {
			c.Fail(err)
			return
		}

		c.OK(resp)
	})
	return wrapHandlerWithMeta(handler, fn)
}

// BindE 将有请求参数但无返回值的业务函数适配为 HandlerFunc。
// 业务函数接收 Aurora Context 和请求参数，只返回错误。
//
// 适用场景：需要请求参数，无返回值（如删除操作）
// 示例：func(c *aurora.Context, req *DeleteUserReq) error
func BindE[Req any](
	fn func(c *Context, req *Req) error,
) *HandlerWithMeta {
	handler := HandlerFunc(func(c *Context) {
		var req Req
		if err := bindRequest(c, &req); err != nil {
			return // bindRequest 内部已调用 c.Fail
		}

		if err := fn(c, &req); err != nil {
			c.Fail(err)
			return
		}

		c.OK(nil)
	})
	return wrapHandlerWithMeta(handler, fn)
}

// BindRE 将无请求参数也无返回值的业务函数适配为 HandlerFunc。
// 业务函数只接收 Aurora Context，只返回错误。
//
// 适用场景：无请求参数，无返回值（如健康检查）
// 示例：func(c *aurora.Context) error
func BindRE(
	fn func(c *Context) error,
) *HandlerWithMeta {
	handler := HandlerFunc(func(c *Context) {
		if err := fn(c); err != nil {
			c.Fail(err)
			return
		}

		c.OK(nil)
	})
	return wrapHandlerWithMeta(handler, fn)
}

// ============================================================================
// 内部辅助函数
// ============================================================================

// bindRequest 根据 HTTP 方法绑定请求参数。
//
// 绑定规则：
// - POST、PUT、PATCH：优先绑定 body，并额外绑定 URI 参数
// - GET、DELETE、HEAD、OPTIONS：绑定 query 和 URI 参数
// - uri tag 触发路径参数绑定
// - form tag 触发 query/form 绑定
// - JSON、XML、form 等 body 格式由 Gin binding 根据 Content-Type 选择
func bindRequest(c *Context, req any) error {
	// 绑定失败会在本函数内写入统一错误响应并 Abort；返回原始错误是为了
	// 让调用方保留诊断信息，但适配器收到错误后必须立即 return。
	method := c.Request().Method

	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		// Body 和 URI 绑定器都会在绑定后校验整个结构体。分开调用时，
		// 仅带另一种标签的 required 字段尚未填充，会导致错误的校验失败。
		// 先完成两类参数的映射，再统一校验，确保两类字段可以同时存在。
		if err := bindBodyAndURI(c, req); err != nil {
			failBinding(c, err)
			return err
		}

	case http.MethodGet, http.MethodDelete, http.MethodHead, http.MethodOptions:
		// Query 和 URI 绑定器都会在绑定后校验整个结构体。分开调用时，
		// 仅带另一种标签的 required 字段尚未填充，会导致错误的校验失败。
		// 先完成两类参数的映射，再统一校验，确保两类字段可以同时存在。
		if err := bindQueryAndURI(c, req); err != nil {
			failBinding(c, err)
			return err
		}

	default:
		// 其他方法（如 TRACE、CONNECT）使用默认绑定
		if err := c.ShouldBind(req); err != nil {
			return err
		}
	}

	return nil
}

func failBinding(c *Context, err error) {
	// 对外错误保持稳定且不暴露字段、解码器和验证器细节；请求体超限单独
	// 映射为 413，便于客户端区分大小限制与普通格式错误。
	if isBodyTooLargeError(err) {
		c.Fail(ErrPayloadTooLarge)
		return
	}
	// Decoder and validator details can contain field names and internal type
	// information. Keep those details in the returned error for logs/tests,
	// while exposing only the stable public error to the client.
	c.Fail(ErrBadRequest)
}

func isBodyTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return true
	}
	return err.Error() == "request body too large" || err.Error() == "http: request body too large"
}

// bindQueryAndURI 将 query 和 URI 参数映射到同一个请求结构体，并在映射
// 完成后统一执行 Gin 的结构体验证。
func bindQueryAndURI(c *Context, req any) error {
	// MapFormWithTag 只做字段映射，不触发验证，因此必须在 query 与 URI
	// 都完成后调用一次 Validator，避免 uri-only 字段被 query 绑定提前判定为缺失。
	if err := binding.MapFormWithTag(req, c.Request().URL.Query(), "form"); err != nil {
		return err
	}

	uriValues := make(map[string][]string, len(c.ctx.Params))
	for _, param := range c.ctx.Params {
		uriValues[param.Key] = []string{param.Value}
	}
	if err := binding.MapFormWithTag(req, uriValues, "uri"); err != nil {
		return err
	}

	if binding.Validator != nil {
		return binding.Validator.ValidateStruct(req)
	}
	return nil
}

// bindBodyAndURI 将 body 和 URI 参数映射到同一个请求结构体，并在映射
// 完成后统一执行 Gin 的结构体验证。
func bindBodyAndURI(c *Context, req any) error {
	// 1. 根据 Content-Type 手动解析 body（不触发校验）。Gin 的公开
	// binding 接口都会在 Bind 返回前校验结构体，因此这里直接复用其
	// 解析器行为，待 URI 映射完成后再统一校验。
	if err := bindBodyWithoutValidation(c, req); err != nil {
		return err
	}

	// 2. 映射 URI 参数（不触发校验）
	uriValues := make(map[string][]string, len(c.ctx.Params))
	for _, param := range c.ctx.Params {
		uriValues[param.Key] = []string{param.Value}
	}
	if err := binding.MapFormWithTag(req, uriValues, "uri"); err != nil {
		return err
	}

	// 3. 统一校验
	if binding.Validator != nil {
		return binding.Validator.ValidateStruct(req)
	}
	return nil
}

func bindBodyWithoutValidation(c *Context, req any) error {
	// 这里刻意绕过 binding.Binding.Bind 的统一入口：Gin 的 Bind 会立即
	// 校验结构体，而 POST/PUT/PATCH 还需要先合并 URI 字段。函数只负责解析
	// body 和映射字段，最终验证由 bindBodyAndURI 统一完成。
	contentType := c.ContentType()
	if contentType == "application/x-www-form-urlencoded" || contentType == "multipart/form-data" || contentType == "" {
		if err := c.Request().ParseForm(); err != nil {
			return err
		}
		if contentType == "multipart/form-data" {
			if err := c.Request().ParseMultipartForm(32 << 20); err != nil {
				return err
			}
		}
		return binding.MapFormWithTag(req, c.Request().Form, "form")
	}
	if !isBodyDecoderContentType(contentType) {
		// 与 Gin binding.Default 一致：未知 Content-Type 按 form 处理。
		if err := c.Request().ParseForm(); err != nil {
			return err
		}
		return binding.MapFormWithTag(req, c.Request().Form, "form")
	}

	if c.Request().Body == nil {
		return errors.New("invalid request")
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || err.Error() == "http: request body too large" {
			return errors.New("request body too large")
		}
		return err
	}

	reader := bytes.NewReader(body)
	switch contentType {
	case "application/json":
		decoder := ginjson.API.NewDecoder(reader)
		if binding.EnableDecoderUseNumber {
			decoder.UseNumber()
		}
		if binding.EnableDecoderDisallowUnknownFields {
			decoder.DisallowUnknownFields()
		}
		return decoder.Decode(req)
	case "application/xml", "text/xml":
		return xml.NewDecoder(reader).Decode(req)
	case "application/x-yaml", "application/yaml":
		return yaml.NewDecoder(reader).Decode(req)
	case "application/toml":
		return toml.NewDecoder(reader).Decode(req)
	case "application/msgpack", "application/x-msgpack":
		return codec.NewDecoder(reader, new(codec.MsgpackHandle)).Decode(&req)
	case "application/bson":
		return bson.Unmarshal(body, req)
	case "application/x-protobuf":
		message, ok := req.(proto.Message)
		if !ok {
			return errors.New("obj is not ProtoMessage")
		}
		return proto.Unmarshal(body, message)
	default:
		return errors.New("unsupported body binding")
	}
}

func isBodyDecoderContentType(contentType string) bool {
	switch contentType {
	case "application/json", "application/xml", "text/xml",
		"application/x-yaml", "application/yaml", "application/toml",
		"application/msgpack", "application/x-msgpack", "application/bson",
		"application/x-protobuf":
		return true
	default:
		return false
	}
}
