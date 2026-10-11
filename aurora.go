package aurora

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/surge-go/aurora/core/logger"
	"go.opentelemetry.io/otel/metric"
)

const version = "0.0.2"

var ginGlobalMu sync.Mutex

// Engine 是 Aurora Web 框架的核心引擎。
//
// Engine 把三类职责放在同一个生命周期对象中：Gin 负责路由分发，
// http.Server 负责网络服务，Aurora 负责配置、中间件和启动/停止状态。
// 应用应在 Start、Serve 或 Run 之前完成路由和中间件注册。
type Engine struct {
	config   Config
	gin      *gin.Engine
	server   *http.Server
	listener net.Listener

	routeHandlers map[string]string
	routeOutput   io.Writer

	// 运行时依赖（通过 Option 注入）
	logger          *logger.Logger
	tracingRuntime  TracingRuntime
	metricsProvider metric.MeterProvider
	httpMetrics     *httpMetrics

	// mu 保护 server、listener 以及下面三个生命周期状态字段。
	// 路由注册本身应在启动前完成，不受该锁保护。
	mu        sync.RWMutex
	listening bool
	started   bool
	stopping  bool
}

// New 创建一个新的 Engine 实例。
// cfg 是配置，opts 是可选的运行时依赖注入。
// 注意：此函数只创建 Engine，不监听端口。
func New(cfg Config, opts ...Option) (*Engine, error) {
	// 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Gin 的模式和调试输出钩子是包级全局状态。先在创建路由前应用框架配置，
	// 并关闭 Gin 自己的路由输出，由 Aurora 统一输出项目日志格式。
	ginGlobalMu.Lock()
	gin.SetMode(cfg.Mode)
	gin.DebugPrintFunc = func(string, ...any) {}
	gin.DebugPrintRouteFunc = func(string, string, string, int) {}
	ginGlobalMu.Unlock()

	// 创建底层路由引擎。Aurora 不把它暴露给调用方，所有请求通过
	// Aurora Context 和 HandlerFunc 进入应用代码。
	ginEngine := gin.New()

	// 设置信任的代理（安全默认：不信任任何代理）
	if len(cfg.TrustedProxies) > 0 {
		// 显式配置了信任的代理列表
		if err := ginEngine.SetTrustedProxies(cfg.TrustedProxies); err != nil {
			return nil, err
		}
	} else {
		// 未配置时，显式禁用代理信任，防止 IP 伪造攻击
		// 此时 ClientIP() 将返回 RemoteAddr，忽略 X-Forwarded-For 等头部
		ginEngine.SetTrustedProxies(nil)
	}

	// 创建 Engine
	e := &Engine{
		config:        cfg,
		gin:           ginEngine,
		routeHandlers: make(map[string]string),
		routeOutput:   os.Stdout,
	}

	// 应用选项后再创建依赖运行时的中间件，例如 Metrics 需要 MeterProvider。
	for _, opt := range opts {
		opt(e)
	}
	if cfg.Middleware.Metrics {
		if e.metricsProvider == nil {
			return nil, errors.New("metrics provider is required when metrics middleware is enabled")
		}
		metrics, err := newHTTPMetrics(e.metricsProvider)
		if err != nil {
			return nil, fmt.Errorf("initialize metrics middleware: %w", err)
		}
		e.httpMetrics = metrics
	}

	// 安装默认中间件
	e.installMiddleware()

	return e, nil
}

// ============================================================================
// 路由注册方法
// ============================================================================

// GET 注册 GET 请求路由。
func (e *Engine) GET(path string, handlers ...any) {
	e.handle("GET", path, handlers...)
}

// POST 注册 POST 请求路由。
func (e *Engine) POST(path string, handlers ...any) {
	e.handle("POST", path, handlers...)
}

// PUT 注册 PUT 请求路由。
func (e *Engine) PUT(path string, handlers ...any) {
	e.handle("PUT", path, handlers...)
}

// PATCH 注册 PATCH 请求路由。
func (e *Engine) PATCH(path string, handlers ...any) {
	e.handle("PATCH", path, handlers...)
}

// DELETE 注册 DELETE 请求路由。
func (e *Engine) DELETE(path string, handlers ...any) {
	e.handle("DELETE", path, handlers...)
}

// HEAD 注册 HEAD 请求路由。
func (e *Engine) HEAD(path string, handlers ...any) {
	e.handle("HEAD", path, handlers...)
}

// OPTIONS 注册 OPTIONS 请求路由。
func (e *Engine) OPTIONS(path string, handlers ...any) {
	e.handle("OPTIONS", path, handlers...)
}

// Any 注册所有 HTTP 方法的路由。
func (e *Engine) Any(path string, handlers ...any) {
	// Any 显式注册 Gin 支持的常用方法，避免依赖底层引擎的隐式行为。
	e.handle("GET", path, handlers...)
	e.handle("POST", path, handlers...)
	e.handle("PUT", path, handlers...)
	e.handle("PATCH", path, handlers...)
	e.handle("DELETE", path, handlers...)
	e.handle("HEAD", path, handlers...)
	e.handle("OPTIONS", path, handlers...)
	e.handle("CONNECT", path, handlers...)
	e.handle("TRACE", path, handlers...)
}

// ============================================================================
// 中间件和路由分组
// ============================================================================

// Use 为全局添加中间件。
func (e *Engine) Use(handlers ...any) {
	// 先统一转换为 Aurora HandlerFunc，再包装成 Gin handler；这样全局中间件
	// 与路由处理器共享同一套 Context 边界。
	handlerFuncs := make([]HandlerFunc, 0, len(handlers))
	for _, h := range handlers {
		switch v := h.(type) {
		case *HandlerWithMeta:
			handlerFuncs = append(handlerFuncs, v.Handler)
		case HandlerFunc:
			handlerFuncs = append(handlerFuncs, v)
		case func(*Context):
			handlerFuncs = append(handlerFuncs, HandlerFunc(v))
		default:
			panic("middleware must be HandlerFunc, *HandlerWithMeta, or func(*Context)")
		}
	}

	ginHandlers := make([]gin.HandlerFunc, len(handlerFuncs))
	for i, h := range handlerFuncs {
		ginHandlers[i] = e.wrapHandler(h)
	}
	e.gin.Use(ginHandlers...)
}

// Group 创建一个新的路由分组。
func (e *Engine) Group(prefix string, handlers ...any) *RouterGroup {
	// 分组只保存路径前缀和 Gin 的中间件链，业务依赖仍由调用方注入 Handler。
	handlerFuncs := make([]HandlerFunc, 0, len(handlers))
	for _, h := range handlers {
		switch v := h.(type) {
		case *HandlerWithMeta:
			handlerFuncs = append(handlerFuncs, v.Handler)
		case HandlerFunc:
			handlerFuncs = append(handlerFuncs, v)
		case func(*Context):
			handlerFuncs = append(handlerFuncs, HandlerFunc(v))
		default:
			panic("middleware must be HandlerFunc, *HandlerWithMeta, or func(*Context)")
		}
	}

	ginHandlers := make([]gin.HandlerFunc, len(handlerFuncs))
	for i, h := range handlerFuncs {
		ginHandlers[i] = e.wrapHandler(h)
	}

	return &RouterGroup{
		engine: e,
		group:  e.gin.Group(prefix, ginHandlers...),
	}
}

// ============================================================================
// 生命周期管理
// ============================================================================

// Start 启动 HTTP 服务器，监听配置的地址。
// 该方法会阻塞，直到服务器关闭或发生错误。
func (e *Engine) Start() error {
	return e.start(context.Background(), nil)
}

// start 初始化并运行 HTTP 服务器。ready 在启动决策完成后关闭：此时
// server 要么已经进入 listening 状态，要么启动已被取消或失败。
func (e *Engine) start(ctx context.Context, ready chan<- struct{}) error {
	// ready 用于 Run 的启动屏障：Run 只有在这里发布 listening 状态后，
	// 才会响应取消并调用 Shutdown，避免启动与关闭并发竞态。
	signalReady := func() {
		if ready != nil {
			close(ready)
		}
	}

	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		signalReady()
		return errors.New("server already started")
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		signalReady()
		return nil
	}

	// 先持有状态锁再监听，保证同一个 Engine 不会并发启动两次。
	listener, err := net.Listen("tcp", e.config.Addr)
	if err != nil {
		e.mu.Unlock()
		signalReady()
		return err
	}
	// 取消可能发生在 net.Listen 期间。关闭刚创建的 listener，避免
	// 已取消的 Run 在后台留下可用端口。
	if err := ctx.Err(); err != nil {
		_ = listener.Close()
		e.mu.Unlock()
		signalReady()
		return nil
	}

	// 创建 http.Server 并在同一临界区发布 listener，之后 Shutdown 才能看到
	// 完整的可关闭状态。
	server := e.newHTTPServer()

	e.server = server
	e.listener = listener
	e.started = true
	e.listening = true
	e.stopping = false
	e.mu.Unlock()
	signalReady()

	e.printRoutes()

	// 启动服务器
	return e.serveHTTP(server, listener)
}

// Serve 使用提供的 listener 启动 HTTP 服务器。
// 该方法会阻塞，直到服务器关闭或发生错误。
func (e *Engine) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("listener cannot be nil")
	}
	return e.serve(context.Background(), listener)
}

// serve is the context-aware implementation used by Lifecycle. The public
// Serve method keeps the original non-cancellable API, while Lifecycle can
// cancel a pending listener handoff during concurrent shutdown.
func (e *Engine) serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("listener cannot be nil")
	}
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if err := ctx.Err(); err != nil {
		_ = listener.Close()
		return nil
	}

	// Serve 与 Start 共用 started 状态，但网络监听器由调用方提供；关闭时
	// Engine 仍会负责关闭该 listener。
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("server already started")
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		_ = listener.Close()
		return nil
	}

	server := e.newHTTPServer()
	e.server = server
	e.listener = listener
	e.started = true
	e.listening = true
	e.stopping = false
	e.mu.Unlock()

	e.printRoutes()
	return e.serveHTTP(server, listener)
}

// Shutdown 优雅地关闭 HTTP 服务器。
// ctx 的 deadline 决定关闭超时时间。
func (e *Engine) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	// Shutdown 允许在未启动或已停止状态下幂等返回；真正关闭由 http.Server
	// 负责排空连接，listener.Close 作为启动/关闭竞态下的兜底。
	e.mu.Lock()
	if !e.listening {
		e.mu.Unlock()
		return nil
	}
	server := e.server
	listener := e.listener
	e.stopping = true
	e.mu.Unlock()

	if server == nil {
		if listener != nil {
			return listener.Close()
		}
		return nil
	}

	// Shutdown closes listeners and drains active connections. If it runs in
	// the small window before Serve enters http.Server.Serve, it has no listener
	// to close yet, so close the owned listener after the call as a fallback.
	err := server.Shutdown(ctx)
	if listener != nil {
		_ = listener.Close()
	}
	return err
}

// Run 启动服务器并监听上下文取消信号。
// 当 ctx 被取消时，自动调用 Shutdown 进行优雅关闭。
func (e *Engine) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	// 启动服务器（非阻塞）。启动错误通过 errCh 返回，启动屏障通过
	// startupDone 发布，两个通道分别表达“错误结果”和“状态已可观察”。
	errCh := make(chan error, 1)
	startupDone := make(chan struct{})
	go func() {
		errCh <- e.start(ctx, startupDone)
	}()

	// 在处理取消前，等待启动流程完成状态发布。否则 Shutdown 可能在
	// Start 设置 listening 之前返回，而后台 goroutine 随后仍会启动服务器。
	select {
	case <-ctx.Done():
		<-startupDone
		// 上下文取消，优雅关闭
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.WriteTimeout)
		defer cancel()
		return e.Shutdown(shutdownCtx)
	case <-startupDone:
	}

	// 等待上下文取消或服务器错误
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), e.config.WriteTimeout)
		defer cancel()
		return e.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// ============================================================================
// http.Handler 接口
// ============================================================================

// ServeHTTP 实现 http.Handler 接口。
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.gin.ServeHTTP(w, r)
}

// ============================================================================
// 工具方法
// ============================================================================

// Addr 返回服务器监听的实际地址。
// 只有在 Start 或 Serve 之后才有效。
func (e *Engine) Addr() string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.listener == nil {
		return ""
	}
	return e.listener.Addr().String()
}

// Listening 返回服务器是否正在监听。
func (e *Engine) Listening() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.listening
}

// ============================================================================
// 内部方法
// ============================================================================

// handle 是内部路由注册方法。
func (e *Engine) handle(method, path string, handlers ...any) {
	// 路由注册阶段保留最后一个业务处理器的名称，运行时仍只执行包装后的
	// HandlerFunc；名称仅用于 Aurora 路由日志，不参与请求分发。
	// 解析 handlers，提取 HandlerFunc 和元信息
	handlerFuncs := make([]HandlerFunc, 0, len(handlers))
	var lastHandlerName string

	for _, h := range handlers {
		switch v := h.(type) {
		case *HandlerWithMeta:
			handlerFuncs = append(handlerFuncs, v.Handler)
			lastHandlerName = v.BusinessFuncName
		case HandlerFunc:
			handlerFuncs = append(handlerFuncs, v)
		case func(*Context):
			handlerFuncs = append(handlerFuncs, HandlerFunc(v))
		default:
			panic("handler must be HandlerFunc, *HandlerWithMeta, or func(*Context)")
		}
	}

	ginHandlers := make([]gin.HandlerFunc, len(handlerFuncs))
	for i, h := range handlerFuncs {
		ginHandlers[i] = e.wrapHandler(h)
	}

	// 记录最后一个 handler 的名称
	if lastHandlerName != "" {
		e.routeHandlers[routeKey(method, path)] = lastHandlerName
	} else if len(handlerFuncs) > 0 {
		e.routeHandlers[routeKey(method, path)] = handlerName(handlerFuncs[len(handlerFuncs)-1])
	}

	e.gin.Handle(method, path, ginHandlers...)
}

func (e *Engine) recordRoute(method, path string, handler HandlerFunc) {
	e.routeHandlers[routeKey(method, path)] = handlerName(handler)
}

func routeKey(method, path string) string {
	return method + "\x00" + path
}

func handlerName(handler HandlerFunc) string {
	if handler == nil {
		return ""
	}
	fn := runtime.FuncForPC(reflect.ValueOf(handler).Pointer())
	if fn == nil {
		return ""
	}
	return fn.Name()
}

func (e *Engine) printRoutes() {
	// 路由只在真正开始 Serve 时打印一次。先排序并计算统一列宽，保证不同
	// 路由长度下日志仍可扫描；未提供 Logger 时回退到 routeOutput/stdout。
	routes := e.gin.Routes()
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})

	pathWidth := routePathWidth(routes)

	// 构建路由日志内容
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "[Aurora] HTTP 路由注册完成 | %d 条路由 | v%s\n", len(routes), version)
	for _, route := range routes {
		handler := e.routeHandlers[routeKey(route.Method, route.Path)]
		if handler == "" {
			handler = route.Handler
		}
		fmt.Fprintf(&buf, "[Aurora] %-7s %-*s --> %s\n", route.Method, pathWidth, route.Path, handler)
	}

	// 使用 logger 输出（如果有），否则回退到 stdout
	if e.logger != nil {
		e.logger.InfoText(buf.String())
	} else {
		output := e.routeOutput
		if output == nil {
			output = os.Stdout
		}
		fmt.Fprint(output, buf.String())
	}
}

const routePathPadding = 4

func routePathWidth(routes gin.RoutesInfo) int {
	width := 0
	for _, route := range routes {
		if pathWidth := utf8.RuneCountInString(route.Path); pathWidth > width {
			width = pathWidth
		}
	}
	return width + routePathPadding
}

// wrapHandler 将 Aurora HandlerFunc 包装为 Gin HandlerFunc。
func (e *Engine) wrapHandler(handler HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		auroraCtx := &Context{ctx: c}
		handler(auroraCtx)
	}
}

func (e *Engine) newHTTPServer() *http.Server {
	// http.Server 只承载网络级配置；请求级限制（例如 MaxBodyBytes）由
	// Aurora 中间件处理，因为它需要进入统一的 Context/错误响应流程。
	return &http.Server{
		Handler:           e.gin,
		ReadTimeout:       e.config.ReadTimeout,
		ReadHeaderTimeout: e.config.ReadHeaderTimeout,
		WriteTimeout:      e.config.WriteTimeout,
		IdleTimeout:       e.config.IdleTimeout,
		MaxHeaderBytes:    e.config.MaxHeaderBytes,
	}
}

func (e *Engine) serveHTTP(server *http.Server, listener net.Listener) error {
	// Serve 返回后统一发布 listening=false。Shutdown 设置 stopping 后，
	// http.ErrServerClosed 被转换为正常结束；其他错误继续交给调用方。
	err := server.Serve(listener)

	e.mu.Lock()
	stopping := e.stopping
	e.listening = false
	e.mu.Unlock()

	if stopping {
		return http.ErrServerClosed
	}
	return err
}
