package aurora

import (
	"errors"
	"fmt"
	"net/http"
)

const defaultStatus = http.StatusInternalServerError

var (
	// ErrInternal 表示服务器内部错误。
	ErrInternal = NewWithStatus(1000, http.StatusInternalServerError, "服务发生异常")
	// ErrBadRequest 表示请求参数或请求格式无效。
	ErrBadRequest = NewWithStatus(1001, http.StatusBadRequest, "请求无效")
	// ErrUnauthorized 表示请求缺少有效的身份认证信息。
	ErrUnauthorized = NewWithStatus(1003, http.StatusUnauthorized, "请求未认证")
	// ErrForbidden 表示当前身份无权访问资源。
	ErrForbidden = NewWithStatus(1004, http.StatusForbidden, "无权访问")
	// ErrNotFound 表示请求的资源不存在。
	ErrNotFound = NewWithStatus(1005, http.StatusNotFound, "资源不存在")
	// ErrMethodNotAllowed 表示请求方法不被目标资源支持。
	ErrMethodNotAllowed = NewWithStatus(1006, http.StatusMethodNotAllowed, "请求方法不允许")
	// ErrRequestTimeout 表示服务器等待请求超时。
	ErrRequestTimeout = NewWithStatus(1007, http.StatusRequestTimeout, "请求超时")
	// ErrConflict 表示请求与当前资源状态冲突。
	ErrConflict = NewWithStatus(1008, http.StatusConflict, "请求冲突")
	// ErrPayloadTooLarge 表示请求体超过服务器允许的大小。
	ErrPayloadTooLarge = NewWithStatus(1009, http.StatusRequestEntityTooLarge, "请求体过大")
	// ErrUnprocessableEntity 表示请求格式正确但语义校验失败。
	ErrUnprocessableEntity = NewWithStatus(1010, http.StatusUnprocessableEntity, "请求无法处理")
	// ErrTooManyRequests 表示请求频率超过限制。
	ErrTooManyRequests = NewWithStatus(1011, http.StatusTooManyRequests, "请求过于频繁")
	// ErrBadGateway 表示上游服务返回了无效响应。
	ErrBadGateway = NewWithStatus(1012, http.StatusBadGateway, "网关错误")
	// ErrServiceUnavailable 表示服务暂时不可用。
	ErrServiceUnavailable = NewWithStatus(1013, http.StatusServiceUnavailable, "服务暂不可用")
	// ErrGatewayTimeout 表示网关等待上游服务超时。
	ErrGatewayTimeout = NewWithStatus(1014, http.StatusGatewayTimeout, "网关超时")
	// ErrAlreadyExists 表示要创建的资源或唯一标识已存在。
	ErrAlreadyExists = NewWithStatus(1015, http.StatusConflict, "资源已存在")
)

// Error 表示可安全返回给 HTTP 客户端的业务错误。
// Message 只用于对外响应，err 仅用于日志和 errors.Is/errors.As 链路；
// 这样底层数据库、网络或校验细节不会意外泄露到响应体。
type Error struct {
	Code    int    `json:"code"`    // 业务错误码
	Message string `json:"message"` // 用户可见消息
	err     error  // 原始错误
	status  int    // HTTP 状态码
}

// NewError 创建使用默认 500 状态码的框架错误。
func NewError(code int, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		status:  defaultStatus,
	}
}

// NewWithStatus 创建带 HTTP 状态码的错误。
func NewWithStatus(code int, status int, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		status:  status,
	}
}

// clone 复制一份，避免链式调用污染预定义错误。预定义错误是共享变量，
// 因此 WithXxx 始终返回新实例而不是修改接收者。
func (e *Error) clone() *Error {
	if e == nil {
		return &Error{status: defaultStatus}
	}
	return &Error{
		Code:    e.Code,
		Message: e.Message,
		err:     e.err,
		status:  e.status,
	}
}

// WithErr 附加原始错误（返回新实例）
func (e *Error) WithErr(err error) *Error {
	n := e.clone()
	n.err = err
	return n
}

// WithStatus 设置 HTTP 状态码（返回新实例）
func (e *Error) WithStatus(status int) *Error {
	n := e.clone()
	n.status = status
	return n
}

// WithMessage 覆盖消息（返回新实例）
func (e *Error) WithMessage(message string) *Error {
	n := e.clone()
	n.Message = message
	return n
}

// WithMessagef 格式化覆盖消息（返回新实例）
func (e *Error) WithMessagef(format string, args ...any) *Error {
	n := e.clone()
	n.Message = fmt.Sprintf(format, args...)
	return n
}

// Is 判断 target 是否为业务码相同的框架错误。
func (e *Error) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}
	var t *Error
	ok := errors.As(target, &t)
	if !ok || t == nil {
		return false
	}
	return e.Code == t.Code
}

// ===== 访问器 =====

// Error 实现 error 接口
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.err)
	}
	return e.Message
}

// Status 获取 HTTP 状态码
func (e *Error) Status() int {
	if e == nil || e.status == 0 {
		return defaultStatus
	}
	return e.status
}

// Unwrap 实现标准库 errors.Unwrap 接口
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// ===== 辅助函数 =====

// Wrap 包装已有错误为框架 Error。
func Wrap(err error, code int, message string) *Error {
	if err == nil {
		return NewError(code, message)
	}
	return &Error{
		Code:    code,
		Message: message,
		err:     err,
		status:  defaultStatus,
	}
}

// WrapWithStatus 包装已有错误（带 HTTP 状态码）
func WrapWithStatus(err error, code int, status int, message string) *Error {
	if err == nil {
		return NewWithStatus(code, status, message)
	}
	return &Error{
		Code:    code,
		Message: message,
		err:     err,
		status:  status,
	}
}

// GetCode 从 error 中提取业务错误码，非框架 Error 返回 -1。
func GetCode(err error) int {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.Code
	}
	return -1
}

// GetStatus 从 error 中提取 HTTP 状态码，非框架 Error 返回 500。
func GetStatus(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status()
	}
	return http.StatusInternalServerError
}

// IsCode 判断 error 是否匹配指定业务错误码
func IsCode(err error, code int) bool {
	return GetCode(err) == code
}

// As 按照标准库 errors.As 的签名和语义查找错误链中的目标类型。
//
// target 必须是非 nil 指针，或指向实现 error 接口的类型的非 nil 指针。
// 非法 target 的处理方式与标准库 errors.As 一致，会 panic。
func As(err error, target any) bool {
	return errors.As(err, target)
}

// Is 标准库 errors.Is 的透传
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// Join 合并多个错误，语义与标准库 errors.Join 一致。
// nil 错误会被忽略；所有参数均为 nil 时返回 nil。
func Join(errs ...error) error {
	return errors.Join(errs...)
}
