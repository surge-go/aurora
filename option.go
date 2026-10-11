package aurora

import (
	"github.com/surge-go/aurora/core/logger"
	"go.opentelemetry.io/otel/metric"
)

// Option 是用于配置 Engine 的函数选项。
// Option 只注入运行时依赖；静态服务器配置仍由 Config 承担。
type Option func(*Engine)

// ============================================================================
// Logger 注入
// ============================================================================

// WithLogger 注入 Aurora 项目 logger 包的日志器。
// 用于 Recovery、AccessLog 等中间件记录日志。
func WithLogger(log *logger.Logger) Option {
	return func(e *Engine) {
		e.logger = log
	}
}

// ============================================================================
// Tracing 注入
// ============================================================================

// WithTracingRuntime 注入 TracingRuntime。
// 用于 Tracing 中间件创建 Span 并提取上游 trace context。
func WithTracingRuntime(rt TracingRuntime) Option {
	return func(e *Engine) {
		e.tracingRuntime = rt
	}
}

// WithMetricsProvider 注入 OpenTelemetry MeterProvider。
// Aurora 只借用 Provider，不负责创建或关闭它。
func WithMetricsProvider(provider metric.MeterProvider) Option {
	return func(e *Engine) {
		e.metricsProvider = provider
	}
}
