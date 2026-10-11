package aurora

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/surge-go/aurora/core/logger"
	"os"
	"strings"
	"testing"
)

func TestRouteOutputUsesAuroraFormat(t *testing.T) {
	e, err := New(Config{Addr: ":0", Mode: gin.DebugMode})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	e.routeOutput = &output
	e.GET("/users", func(*Context) {})
	e.POST("/users", func(*Context) {})
	e.printRoutes()

	text := output.String()
	if !bytes.Contains(output.Bytes(), []byte("[Aurora] HTTP 路由注册完成 | 2 条路由 | v"+version+"\n")) {
		t.Fatalf("route summary = %q", text)
	}
	if !bytes.Contains(output.Bytes(), []byte("[Aurora] GET     /users")) || !bytes.Contains(output.Bytes(), []byte("[Aurora] POST    /users")) {
		t.Fatalf("route entries = %q", text)
	}
}

func TestRouteOutputUsesBoundBusinessHandlerName(t *testing.T) {
	e, err := New(Config{Addr: ":0", Mode: gin.ReleaseMode})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	e.routeOutput = &output
	e.GET("/health", BindR(routeHealthHandler))
	e.printRoutes()

	if !bytes.Contains(output.Bytes(), []byte("--> github.com/surge-go/aurora.routeHealthHandler")) {
		t.Fatalf("route output = %q", output.String())
	}
}

func TestRouteOutputSizesPathColumnFromLongestRoute(t *testing.T) {
	e, err := New(Config{Addr: ":0", Mode: gin.ReleaseMode})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	e.routeOutput = &output
	e.GET("/health", func(*Context) {})
	e.GET("/api/v1/audit/operation/detail", func(*Context) {})
	e.printRoutes()

	var arrowPositions []int
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.Contains(line, "GET") && strings.Contains(line, "-->") {
			arrowPositions = append(arrowPositions, strings.Index(line, "-->"))
		}
	}
	if len(arrowPositions) != 2 || arrowPositions[0] != arrowPositions[1] {
		t.Fatalf("route arrows are not aligned: %v\n%s", arrowPositions, output.String())
	}
}

func routeHealthHandler(*Context) (*struct{}, error) {
	return nil, nil
}

// ===== route_output_test.go =====

func TestRouteRegistrationUsesProjectLogger(t *testing.T) {
	// 创建一个捕获 stderr 的 buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
	}()

	// 创建 logger（配置输出到 stderr）
	logCfg := &logger.Config{
		Level:  "info",
		Format: "console", // 使用 console 格式
		Output: "stderr",
	}

	log, err := logger.New(logCfg)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// 创建 engine 并注入 logger
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	engine, err := New(cfg, WithLogger(log))
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// 注册路由
	engine.GET("/users/:id", func(c *Context) {
		c.String(200, "ok")
	})

	// 触发路由输出
	engine.printRoutes()

	// 关闭 writer 并读取 stderr 输出
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	t.Logf("Logger output (via stderr):\n%s", output)

	// 验证路由日志通过 logger 输出到 stderr
	if output == "" {
		t.Error("Expected route logs in logger output (stderr), got empty")
	}

	// 验证包含路由信息
	if !strings.Contains(output, "GET") || !strings.Contains(output, "/users/:id") {
		t.Errorf("Route registration log missing expected content: %s", output)
	}

	t.Log("✓ Route logs now use project logger")
}
