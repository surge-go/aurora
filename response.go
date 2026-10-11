package aurora

// Response 是统一 HTTP 响应结构。
type Response[T any] struct {
	// Code 是业务状态码。
	Code int `json:"code"`
	// Data 是业务响应数据。
	Data T `json:"data"`
	// Message 是用户可见消息。
	Message string `json:"message"`
	// TraceID 是当前请求的链路标识。
	TraceID string `json:"trace_id,omitempty"`
}

// NewResponse 创建统一响应。traceID 为空时 JSON 序列化会省略 trace_id 字段。
func NewResponse[T any](code int, data T, message string, traceId string) *Response[T] {
	return &Response[T]{
		Code:    code,
		Data:    data,
		Message: message,
		TraceID: traceId,
	}
}

// PageResp 是分页查询的统一响应结构。
// 泛型参数 T 适配不同业务元素类型，调用方可传值或指针类型（如 PageResp[User] 或 PageResp[*User]）。
type PageResp[T any] struct {
	List  []T   `json:"list"`
	Total int64 `json:"total"`
}

// NewPageResp 构造分页响应，返回指针避免大切片值拷贝。
// nil 列表会规范化为空数组，避免客户端收到 null。
func NewPageResp[T any](list []T, total int64) *PageResp[T] {
	if list == nil {
		list = make([]T, 0)
	}
	return &PageResp[T]{
		List:  list,
		Total: total,
	}
}
