package aurora

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/surge-go/aurora/core/logger"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
)

const maxRequestIDLength = 128

// installMiddleware 根据配置安装默认中间件。
//
// 注册顺序决定包裹关系：先注册的中间件在请求进入时先执行、返回时后执行。
// 当前顺序为 MaxBody -> RequestID -> Metrics -> Recovery -> Tracing -> AccessLog，
// 从而让指标和恢复层可以观察到下游最终状态，让访问日志和 Span 在返回阶段收尾。
func (e *Engine) installMiddleware() {
	cfg := e.config.Middleware

	// 0. MaxBodyBytes 限制（最先执行，防止大请求体攻击）
	if e.config.MaxBodyBytes > 0 {
		e.Use(MaxBodyBytesMiddleware(e.config.MaxBodyBytes))
	}

	// 1. RequestID 中间件（最先执行，为请求生成唯一 ID）
	if cfg.RequestID {
		e.Use(RequestIDMiddleware())
	}

	// Metrics 包裹 Recovery，才能观察 Recovery 在下游 panic 后写出的最终状态。
	if cfg.Metrics && e.httpMetrics != nil {
		e.Use(e.httpMetrics.middleware())
	}

	// Recovery 包裹其余框架和业务中间件，将任意下游 panic 转成统一错误响应。
	if cfg.Recovery {
		e.Use(RecoveryMiddleware(e.logger))
	}

	if cfg.Tracing {
		e.Use(TracingMiddleware(e.tracingRuntime))
	}

	if cfg.AccessLog {
		e.Use(AccessLogMiddleware(e.logger))
	}
}

// ============================================================================
// MaxBodyBytes 中间件
// ============================================================================

// MaxBodyBytesMiddleware 限制请求体的最大大小。
// 超过限制的请求会被拒绝并返回 413 Payload Too Large。
func MaxBodyBytesMiddleware(maxBytes int64) Middleware {
	// MaxBytesReader 只在读取请求体时报告超限；它不会主动读取 body，
	// 因此真正的 413 响应仍由绑定器读取失败后的统一错误映射完成。
	return func(c *Context) {
		// 使用 http.MaxBytesReader 包装 Request.Body
		// 当读取超过 maxBytes 时会返回错误
		c.Request().Body = http.MaxBytesReader(c.Writer(), c.Request().Body, maxBytes)
		c.Next()
	}
}

// ============================================================================
// RequestID 中间件
// ============================================================================

// RequestIDMiddleware 为每个请求生成唯一的 Request ID。
// Request ID 会被设置到 Context 中，可通过 c.RequestID() 获取。
func RequestIDMiddleware() Middleware {
	// 请求头中的 ID 只接受有限 ASCII 字符集，避免日志注入和异常响应头；
	// 不合规或过长的值会被新的 UUID 替换。
	return func(c *Context) {
		// 尝试从请求头获取 Request ID
		requestID := c.GetHeader("X-Request-ID")
		if !validRequestID(requestID) {
			// 生成新的 UUID
			requestID = uuid.New().String()
		}

		// 请求标识与 OpenTelemetry trace ID 分开保存；Tracing 未启用时，trace_id
		// 仍保留请求标识作为 TraceID 调用方的兼容降级值。
		c.Set("request_id", requestID)
		c.Set("trace_id", requestID)

		// 设置到响应头
		c.SetHeader("X-Request-ID", requestID)

		c.Next()
	}
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return false
	}
	return true
}

// ============================================================================
// Recovery 中间件
// ============================================================================

// RecoveryMiddleware 捕获下游 panic 并返回统一的错误响应。
// 已经写出响应时只 Abort 并保留原内容，避免再次写 JSON 破坏响应。
func RecoveryMiddleware(log *logger.Logger) Middleware {
	return func(c *Context) {
		defer func() {
			if err := recover(); err != nil {
				// 记录 panic 日志（包含详细信息）
				if log != nil {
					log.Error("panic recovered",
						logger.Error(fmt.Errorf("%v", err)),
						logger.String("request_id", c.RequestID()),
						logger.String("method", c.Request().Method),
						logger.String("path", c.Request().URL.Path),
						logger.String("client_ip", c.ClientIP()),
						logger.String("stack", string(debug.Stack())),
					)
				}

				// If headers/body were already written, a second JSON response
				// would corrupt the response. Abort the chain and preserve it.
				if c.ctx.Writer.Written() {
					c.Abort()
					return
				}
				c.Fail(ErrInternal)
			}
		}()

		c.Next()
	}
}

// ============================================================================
// AccessLog 中间件
// ============================================================================

// AccessLogMiddleware 记录每个请求的访问日志。
//
// 日志统一使用 [Aurora] 纯文本格式，便于和路由注册日志保持一致：
// [Aurora] 2026/10/09 - 23:07:49 | 401 | 517.125µs |       127.0.0.1 | GET     "/health/live" trace_id=...
// 访问日志通过 logger.InfoText 写入 logger 配置的输出目标，同时保持纯文本格式。
//
// 使用 defer 确保即使请求 panic 也能记录访问日志。
func AccessLogMiddleware(log *logger.Logger) Middleware {
	return func(c *Context) {
		// 记录开始时间
		start := time.Now()

		// 使用 defer 确保即使 panic 也会记录访问日志。该中间件位于 Recovery
		// 内侧，因此 panic 分支记录后必须继续向外抛出，交由 Recovery 收口。
		defer func() {
			if recovered := recover(); recovered != nil {
				// AccessLog 位于 Recovery 内侧。重新抛出前先设置 500，确保外层
				// tracing 和 metrics 观察到最终响应状态。
				c.Status(http.StatusInternalServerError)
				if log != nil {
					log.InfoText(formatAccessLog(
						time.Now(),
						time.Since(start),
						c.ctx.Writer.Status(),
						c.ClientIP(),
						c.Request().Method,
						accessLogPath(c),
						c.TraceID(),
					))
				}
				panic(recovered)
			}

			// 使用同一个时间点生成时间戳和耗时，输出稳定的纯文本格式。
			now := time.Now()
			if log != nil {
				log.InfoText(formatAccessLog(
					now,
					now.Sub(start),
					c.ctx.Writer.Status(),
					c.ClientIP(),
					c.Request().Method,
					accessLogPath(c),
					c.TraceID(),
				))
			}
		}()

		// 执行请求
		c.Next()
	}
}

func accessLogPath(c *Context) string {
	// 已匹配路由使用模板降低日志基数；未匹配请求才回退到实际 URL 路径。
	if path := c.FullPath(); path != "" {
		return path
	}
	return c.Request().URL.Path
}

func formatAccessLog(timestamp time.Time, latency time.Duration, status int, clientIP, method, path, traceID string) string {
	// 固定列宽只影响可读性，不截断任何字段；path 用 %q 包裹以区分空格和特殊字符。
	uri := fmt.Sprintf("%q", path)
	if traceID != "" {
		uri += " trace_id=" + traceID
	}
	return fmt.Sprintf("[Aurora] %s | %3d | %8v | %15s | %-7s %s\n",
		timestamp.Format("2006/01/02 - 15:04:05"),
		status,
		latency,
		clientIP,
		method,
		uri,
	)
}

// ============================================================================
// Tracing 中间件
// ============================================================================

// TracingMiddleware 为每个请求创建 OpenTelemetry Span 并提取上游 trace context。
// Span 写回 Request.Context 后，业务代码通过 c.Context() 即可沿用同一链路。
func TracingMiddleware(rt TracingRuntime) Middleware {
	return func(c *Context) {
		// 如果没有注入 tracing runtime，直接跳过
		if rt == nil {
			c.Next()
			return
		}

		// 从请求头提取上游 trace context
		ctx := rt.Extract(c.Context(), propagation.HeaderCarrier(c.Request().Header))

		// 获取 tracer 并创建新的 span
		tracer := rt.TracerProvider().Tracer("aurora")
		ctx, span := tracer.Start(ctx, c.Request().Method+" "+c.FullPath())

		// 设置 span 属性（只记录 path，不包含 query 参数）
		span.SetAttributes(
			attribute.String("http.method", c.Request().Method),
			attribute.String("http.url", c.Request().URL.Path),
			attribute.String("http.route", c.FullPath()),
			attribute.String("http.client_ip", c.ClientIP()),
		)

		// 将新的 context 设置回 gin.Context
		c.ctx.Request = c.ctx.Request.WithContext(ctx)

		defer func() {
			if recovered := recover(); recovered != nil {
				// Recovery runs outside this middleware. Mark the response before
				// rethrowing so the span records the panic as an HTTP 500.
				c.Status(http.StatusInternalServerError)
				status := c.ctx.Writer.Status()
				span.SetAttributes(
					attribute.Int("http.status_code", status),
					attribute.Int("http.response_size", c.ctx.Writer.Size()),
				)
				span.SetStatus(codes.Error, "panic")
				span.End()
				panic(recovered)
			}

			status := c.ctx.Writer.Status()
			span.SetAttributes(
				attribute.Int("http.status_code", status),
				attribute.Int("http.response_size", c.ctx.Writer.Size()),
			)
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
			span.End()
		}()

		// 执行后续处理。即使下游 panic 并由外层 Recovery 处理，defer 仍会
		// 记录最终状态。
		c.Next()
	}
}

const metricsMeterName = "github.com/surge-go/aurora"

// httpMetrics 只持有从应用 MeterProvider 创建的 instruments，不持有 Provider
// 的关闭权。Provider 的创建、导出和关闭由应用负责。
type httpMetrics struct {
	activeRequests   metric.Int64UpDownCounter
	requestDuration  metric.Float64Histogram
	requestBodySize  metric.Int64Histogram
	responseBodySize metric.Int64Histogram
}

func newHTTPMetrics(provider metric.MeterProvider) (*httpMetrics, error) {
	// 在 Engine 创建阶段一次性创建 instruments，请求处理路径只做 Add/Record，
	// 避免每个请求重复查找 instrument。
	if provider == nil {
		return nil, errors.New("meter provider is nil")
	}

	meter := provider.Meter(metricsMeterName)
	activeRequests, err := meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of HTTP requests currently being handled"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create active requests instrument: %w", err)
	}
	requestDuration, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of HTTP server requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("create request duration instrument: %w", err)
	}
	requestBodySize, err := meter.Int64Histogram(
		"http.server.request.body.size",
		metric.WithDescription("Size of HTTP request bodies"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("create request body size instrument: %w", err)
	}
	responseBodySize, err := meter.Int64Histogram(
		"http.server.response.body.size",
		metric.WithDescription("Size of HTTP response bodies"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("create response body size instrument: %w", err)
	}

	return &httpMetrics{
		activeRequests:   activeRequests,
		requestDuration:  requestDuration,
		requestBodySize:  requestBodySize,
		responseBodySize: responseBodySize,
	}, nil
}

func (m *httpMetrics) middleware() Middleware {
	// active_requests 使用进入/退出各一次的 UpDownCounter；其余 Histogram
	// 在 defer 中记录，确保正常返回、Abort 和 panic 都能完成收尾。
	return func(c *Context) {
		start := time.Now()
		baseAttrs := httpMetricAttributes(c, 0, false)
		m.activeRequests.Add(c.Context(), 1, metric.WithAttributes(baseAttrs...))
		if size := c.Request().ContentLength; size >= 0 {
			m.requestBodySize.Record(c.Context(), size, metric.WithAttributes(baseAttrs...))
		}

		defer func() {
			recovered := recover()
			status := c.ctx.Writer.Status()
			if recovered != nil {
				status = http.StatusInternalServerError
			}
			attrs := httpMetricAttributes(c, status, true)
			ctx := c.Context()
			m.activeRequests.Add(ctx, -1, metric.WithAttributes(baseAttrs...))
			m.requestDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
			if size := c.ctx.Writer.Size(); size >= 0 {
				m.responseBodySize.Record(ctx, int64(size), metric.WithAttributes(attrs...))
			}
			if recovered != nil {
				panic(recovered)
			}
		}()

		c.Next()
	}
}

func httpMetricAttributes(c *Context, status int, includeStatus bool) []attribute.KeyValue {
	// 只记录方法、路由模板和状态码等低基数属性；未匹配路由不把原始 URL
	// 放入指标，避免动态路径造成无界时间序列。
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", c.Request().Method),
	}
	if route := c.FullPath(); route != "" {
		attrs = append(attrs, attribute.String("http.route", route))
	}
	if includeStatus {
		attrs = append(attrs, attribute.Int("http.response.status_code", status))
	}
	return attrs
}
