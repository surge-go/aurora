package aurora

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// RouterGroup 是 Aurora 对 Gin RouterGroup 的封装。
// 路由分组只负责路径前缀和中间件继承，不携带认证、权限或业务依赖。
// Group 返回的新分组共享同一个 Engine，因此路由日志和 Handler 包装规则一致。
type RouterGroup struct {
	engine *Engine
	group  *gin.RouterGroup
}

// ============================================================================
// 中间件管理
// ============================================================================

// Use 为当前路由分组添加中间件。
// 中间件会应用到该分组及其所有子分组的路由上。
func (g *RouterGroup) Use(handlers ...any) *RouterGroup {
	// 分组中间件在该组路由注册时复制到 Gin 链中；调用 Use 不会影响已经
	// 注册到其他分组的路由。
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
		ginHandlers[i] = g.wrapHandler(h)
	}
	g.group.Use(ginHandlers...)
	return g
}

// ============================================================================
// 路由分组
// ============================================================================

// Group 创建一个新的路由分组。
// prefix 是路径前缀，handlers 是可选的中间件列表。
func (g *RouterGroup) Group(prefix string, handlers ...any) *RouterGroup {
	// Gin 负责规范化嵌套前缀，Aurora 只保留 engine 引用以记录完整路由名称。
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
		ginHandlers[i] = g.wrapHandler(h)
	}

	return &RouterGroup{
		engine: g.engine,
		group:  g.group.Group(prefix, ginHandlers...),
	}
}

// ============================================================================
// HTTP 方法路由
// ============================================================================

// GET 注册 GET 请求路由。
func (g *RouterGroup) GET(path string, handlers ...any) {
	g.handle("GET", path, handlers...)
}

// POST 注册 POST 请求路由。
func (g *RouterGroup) POST(path string, handlers ...any) {
	g.handle("POST", path, handlers...)
}

// PUT 注册 PUT 请求路由。
func (g *RouterGroup) PUT(path string, handlers ...any) {
	g.handle("PUT", path, handlers...)
}

// PATCH 注册 PATCH 请求路由。
func (g *RouterGroup) PATCH(path string, handlers ...any) {
	g.handle("PATCH", path, handlers...)
}

// DELETE 注册 DELETE 请求路由。
func (g *RouterGroup) DELETE(path string, handlers ...any) {
	g.handle("DELETE", path, handlers...)
}

// HEAD 注册 HEAD 请求路由。
func (g *RouterGroup) HEAD(path string, handlers ...any) {
	g.handle("HEAD", path, handlers...)
}

// OPTIONS 注册 OPTIONS 请求路由。
func (g *RouterGroup) OPTIONS(path string, handlers ...any) {
	g.handle("OPTIONS", path, handlers...)
}

// Any 注册所有 HTTP 方法的路由。
func (g *RouterGroup) Any(path string, handlers ...any) {
	g.handle("GET", path, handlers...)
	g.handle("POST", path, handlers...)
	g.handle("PUT", path, handlers...)
	g.handle("PATCH", path, handlers...)
	g.handle("DELETE", path, handlers...)
	g.handle("HEAD", path, handlers...)
	g.handle("OPTIONS", path, handlers...)
	g.handle("CONNECT", path, handlers...)
	g.handle("TRACE", path, handlers...)
}

// ============================================================================
// 内部辅助方法
// ============================================================================

// handle 是内部路由注册方法。
func (g *RouterGroup) handle(method, path string, handlers ...any) {
	// 解析 handlers，提取可执行的 HandlerFunc 和仅用于日志的元信息。
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
		ginHandlers[i] = g.wrapHandler(h)
	}

	// 记录最后一个业务 handler 的名称；中间件名称不作为路由目标显示。
	fullPath := joinRoutePath(g.group.BasePath(), path)
	if lastHandlerName != "" {
		// 直接设置业务函数名称
		g.engine.routeHandlers[method+"\x00"+fullPath] = lastHandlerName
	} else if len(handlerFuncs) > 0 {
		g.engine.recordRoute(method, fullPath, handlerFuncs[len(handlerFuncs)-1])
	}

	g.group.Handle(method, path, ginHandlers...)
}

func joinRoutePath(prefix, route string) string {
	// 该函数只用于生成路由日志的 key，不参与 Gin 的实际匹配；统一斜杠
	// 后可以让根分组、嵌套分组和 "/" 路由得到同一个规范路径。
	if prefix == "/" {
		prefix = ""
	}
	if route == "/" {
		route = ""
	}
	joined := strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(route, "/")
	if joined == "" {
		return "/"
	}
	return joined
}

// wrapHandler 将 Aurora HandlerFunc 包装为 Gin HandlerFunc。
func (g *RouterGroup) wrapHandler(handler HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 每次请求创建短生命周期的 Aurora Context，不把 Gin 对象泄露到业务 API。
		auroraCtx := &Context{ctx: c}
		handler(auroraCtx)
	}
}
