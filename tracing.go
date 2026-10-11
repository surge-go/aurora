package aurora

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TracingRuntime 定义 tracing 运行时接口，用于依赖注入和测试。
//
// Aurora 只使用该接口创建 Span、传播上下文和关闭 Provider，不拥有具体
// Provider 的创建与生命周期；应用负责在 Engine 外部管理它。
type TracingRuntime interface {
	TracerProvider() trace.TracerProvider
	Propagator() propagation.TextMapPropagator
	Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context
	Inject(ctx context.Context, carrier propagation.TextMapCarrier)
	Shutdown(ctx context.Context) error
}
