package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testYAML = `
app:
  name: test-app
  port: 8080
  debug: true
  timeout: 5s
  tags:
    - web
    - api
database:
  host: localhost
  port: 5432
  name: testdb
`

func writeTestConfig(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}

func TestNew(t *testing.T) {
	c := New()
	assert.NotNil(t, c)
	assert.NotNil(t, c.viper)
	assert.False(t, c.protected)
	assert.False(t, c.autoWatch)
}

func TestNewWithOptions(t *testing.T) {
	c := New(
		WithProtected(true),
		WithAutoWatch(true),
		WithEnvPrefix("TEST"),
	)
	assert.NotNil(t, c)
	assert.True(t, c.protected)
	assert.True(t, c.autoWatch)
	assert.Equal(t, "TEST", c.envPrefix)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	err := c.Load()
	require.NoError(t, err)

	assert.Equal(t, "test-app", c.GetString("app.name"))
	assert.Equal(t, 8080, c.GetInt("app.port"))
}

func TestLoadWithNameAndPaths(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "myconfig.yaml", testYAML)

	c := New(
		WithConfigName("myconfig"),
		WithConfigType("yaml"),
		WithConfigPaths(dir),
	)
	err := c.Load()
	require.NoError(t, err)

	assert.Equal(t, "test-app", c.GetString("app.name"))
}

func TestGetString(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	assert.Equal(t, "test-app", c.GetString("app.name"))
	assert.Equal(t, "", c.GetString("nonexistent"))
}

func TestGetInt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	assert.Equal(t, 8080, c.GetInt("app.port"))
	assert.Equal(t, int64(5432), c.GetInt64("database.port"))
	assert.Equal(t, 0, c.GetInt("nonexistent"))
}

func TestGetBool(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	assert.True(t, c.GetBool("app.debug"))
	assert.False(t, c.GetBool("nonexistent"))
}

func TestGetDuration(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	assert.Equal(t, 5*time.Second, c.GetDuration("app.timeout"))
}

func TestGetStringSlice(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	tags := c.GetStringSlice("app.tags")
	assert.Equal(t, []string{"web", "api"}, tags)
}

func TestGetStringSliceMissingReturnsNil(t *testing.T) {
	c := New()
	assert.Nil(t, c.GetStringSlice("missing"))
}

func TestGenericGet(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	name := Get[string](c, "app.name")
	assert.Equal(t, "test-app", name)

	debug := Get[bool](c, "app.debug")
	assert.True(t, debug)

	// 不存在的键返回零值
	missing := Get[string](c, "nonexistent")
	assert.Equal(t, "", missing)
}

func TestGenericGetConvertsValues(t *testing.T) {
	c := New()
	c.Set("count", 42)
	c.Set("timeout", "5s")

	assert.Equal(t, int64(42), Get[int64](c, "count"))
	assert.Equal(t, 5*time.Second, Get[time.Duration](c, "timeout"))
}

func TestSet(t *testing.T) {
	c := New()
	c.Set("foo", "bar")
	assert.Equal(t, "bar", c.GetString("foo"))

	c.Set("count", 42)
	assert.Equal(t, 42, c.GetInt("count"))
}

func TestIsSet(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	assert.True(t, c.IsSet("app.name"))
	assert.True(t, c.IsSet("database.host"))
	assert.False(t, c.IsSet("nonexistent.key"))
}

func TestAllSettings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	all := c.AllSettings()
	assert.NotNil(t, all)
	assert.Contains(t, all, "app")
	assert.Contains(t, all, "database")
}

func TestSub(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	dbCfg := c.Sub("database")
	require.NotNil(t, dbCfg)
	assert.Equal(t, "localhost", dbCfg.GetString("host"))
	assert.Equal(t, 5432, dbCfg.GetInt("port"))
	assert.Equal(t, "testdb", dbCfg.GetString("name"))

	// 不存在的子配置返回 nil
	nilSub := c.Sub("nonexistent")
	assert.Nil(t, nilSub)
}

func TestReturnedCollectionsAreIsolated(t *testing.T) {
	c := New()
	c.Set("database", map[string]any{
		"host": "localhost",
		"tags": []any{"primary"},
	})

	m := c.GetStringMap("database")
	m["host"] = "changed"
	m["tags"].([]any)[0] = "changed"
	assert.Equal(t, "localhost", c.GetString("database.host"))
	assert.Equal(t, "primary", c.GetStringSlice("database.tags")[0])

	sub := c.Sub("database")
	require.NotNil(t, sub)
	sub.Set("host", "sub-changed")
	assert.Equal(t, "localhost", c.GetString("database.host"))
}

func TestSubPreservesEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", "database:\n  host: file-host\n")
	t.Setenv("MYAPP_DATABASE_HOST", "env-host")

	c := New(WithConfigFile(cfgPath), WithEnvPrefix("MYAPP"))
	require.NoError(t, c.Load())
	sub := c.Sub("database")
	require.NotNil(t, sub)
	assert.Equal(t, "env-host", sub.GetString("host"))
}

func TestUnmarshal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	var cfg struct {
		App struct {
			Name    string        `mapstructure:"name"`
			Port    int           `mapstructure:"port"`
			Debug   bool          `mapstructure:"debug"`
			Timeout time.Duration `mapstructure:"timeout"`
		} `mapstructure:"app"`
		Database struct {
			Host string `mapstructure:"host"`
			Port int    `mapstructure:"port"`
			Name string `mapstructure:"name"`
		} `mapstructure:"database"`
	}

	err := c.Unmarshal(&cfg)
	require.NoError(t, err)
	assert.Equal(t, "test-app", cfg.App.Name)
	assert.Equal(t, 8080, cfg.App.Port)
	assert.True(t, cfg.App.Debug)
	assert.Equal(t, "localhost", cfg.Database.Host)
}

func TestUnmarshalKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	var db struct {
		Host string `mapstructure:"host"`
		Port int    `mapstructure:"port"`
		Name string `mapstructure:"name"`
	}

	err := c.UnmarshalKey("database", &db)
	require.NoError(t, err)
	assert.Equal(t, "localhost", db.Host)
	assert.Equal(t, 5432, db.Port)
	assert.Equal(t, "testdb", db.Name)
}

func TestDefault(t *testing.T) {
	// 重置全局状态，隔离测试
	defaultMu.Lock()
	old := defaultInstance
	defaultInstance = nil
	defaultMu.Unlock()
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultInstance = old
		defaultMu.Unlock()
	})

	d := Default()
	assert.NotNil(t, d)

	// 再次调用 Default 返回同一个实例
	d2 := Default()
	assert.Same(t, d, d2)
}

func TestSetDefault(t *testing.T) {
	// 重置全局状态
	defaultMu.Lock()
	old := defaultInstance
	defaultInstance = nil
	defaultMu.Unlock()
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultInstance = old
		defaultMu.Unlock()
	})

	c := New()
	SetDefault(c)
	assert.Same(t, c, Default())
}

func TestWithDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", `
app:
  name: myapp
`)

	defaults := map[string]any{
		"app.port":  3000,
		"app.debug": false,
	}

	c := New(
		WithConfigFile(cfgPath),
		WithDefaults(defaults),
	)
	require.NoError(t, c.Load())

	// 显式值覆盖默认值
	assert.Equal(t, "myapp", c.GetString("app.name"))
	// 文件中未配置的键使用默认值
	assert.Equal(t, 3000, c.GetInt("app.port"))
	assert.False(t, c.GetBool("app.debug"))
}

func TestWithEnvPrefix(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", `
app:
  name: myapp
`)

	t.Setenv("MYAPP_APP_NAME", "env-app")

	c := New(
		WithConfigFile(cfgPath),
		WithEnvPrefix("MYAPP"),
		WithEnvKeyReplacer(strings.NewReplacer(".", "_")),
	)
	require.NoError(t, c.Load())

	// 环境变量应覆盖配置文件的值
	assert.Equal(t, "env-app", c.GetString("app.name"))
}

func TestWithEnvPrefixUsesUnderscoreByDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", "app:\n  name: file-app\n")
	t.Setenv("MYAPP_APP_NAME", "env-app")

	c := New(WithConfigFile(cfgPath), WithEnvPrefix("MYAPP"))
	require.NoError(t, c.Load())
	assert.Equal(t, "env-app", c.GetString("app.name"))
}

func TestWithOnError(t *testing.T) {
	var gotErr error
	c := New(
		WithOnError(func(err error) {
			gotErr = err
		}),
	)
	assert.NotNil(t, c)
	assert.NotNil(t, c.onError)
	// 通过保护模式下的恢复失败间接验证 onError
	_ = gotErr
}

func TestProtectedMode(t *testing.T) {
	dir := t.TempDir()
	originalContent := `
app:
  name: original
`
	cfgPath := writeTestConfig(t, dir, "config.yaml", originalContent)
	require.NoError(t, os.Chmod(cfgPath, 0640))

	c := New(
		WithConfigFile(cfgPath),
		WithProtected(true),
		WithAutoWatch(true),
	)
	require.NoError(t, c.Load())
	t.Cleanup(c.Close)
	assert.True(t, c.IsProtected())

	// 在外部修改文件
	modifiedContent := `
app:
  name: modified
`
	err := os.WriteFile(cfgPath, []byte(modifiedContent), 0644)
	require.NoError(t, err)

	// 等待 fsnotify 检测变更并恢复文件
	time.Sleep(500 * time.Millisecond)

	// 文件应恢复为原始内容
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, string(data))
	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm())
}

func TestProtectedModeContinuesAfterRestore(t *testing.T) {
	dir := t.TempDir()
	originalContent := "app:\n  name: original\n"
	cfgPath := writeTestConfig(t, dir, "config.yaml", originalContent)

	c := New(
		WithConfigFile(cfgPath),
		WithProtected(true),
		WithAutoWatch(true),
	)
	require.NoError(t, c.Load())
	t.Cleanup(c.Close)

	for i := 0; i < 2; i++ {
		err := os.WriteFile(cfgPath, []byte("app:\n  name: modified\n"), 0644)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(cfgPath)
			return err == nil && string(data) == originalContent
		}, 2*time.Second, 10*time.Millisecond)
	}
}

func TestNonProtectedMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	changed := make(chan struct{}, 1)
	c := New(
		WithConfigFile(cfgPath),
		WithProtected(false),
		WithAutoWatch(true),
		WithOnChange(func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		}),
	)
	require.NoError(t, c.Load())
	t.Cleanup(c.Close)
	assert.False(t, c.IsProtected())

	// 修改文件
	newContent := `
app:
  name: updated-app
  port: 9090
  debug: false
  timeout: 10s
  tags:
    - web
database:
  host: remotehost
  port: 5432
  name: testdb
`
	err := os.WriteFile(cfgPath, []byte(newContent), 0644)
	require.NoError(t, err)

	// 等待 onChange 回调
	select {
	case <-changed:
		// 回调已触发
	case <-time.After(2 * time.Second):
		t.Fatal("onChange callback was not triggered within timeout")
	}
}

func TestSetProtected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(
		WithConfigFile(cfgPath),
		WithProtected(false),
		WithAutoWatch(true),
	)
	require.NoError(t, c.Load())
	t.Cleanup(c.Close)
	assert.False(t, c.IsProtected())

	// 动态启用保护模式
	c.SetProtected(true)
	assert.True(t, c.IsProtected())

	// 动态关闭保护模式
	c.SetProtected(false)
	assert.False(t, c.IsProtected())
}

func TestSetProtectedStaysDisabledWhenSnapshotFails(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	var gotErr error
	c := New(
		WithConfigFile(cfgPath),
		WithOnError(func(err error) {
			gotErr = err
		}),
	)
	require.NoError(t, c.Load())
	require.NoError(t, os.Remove(cfgPath))

	c.SetProtected(true)
	assert.False(t, c.IsProtected())
	assert.Error(t, gotErr)
}

func TestConfigFileNotFound(t *testing.T) {
	c := New(WithConfigFile("/nonexistent/path/config.yaml"))
	err := c.Load()
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrConfigNotFound))
}

func TestConfigFileNotFoundByName(t *testing.T) {
	dir := t.TempDir()
	c := New(
		WithConfigName("nonexistent"),
		WithConfigType("yaml"),
		WithConfigPaths(dir),
	)
	err := c.Load()
	assert.Error(t, err)
}

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	var wg sync.WaitGroup
	const goroutines = 50

	// 并发读取
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.GetString("app.name")
			_ = c.GetInt("app.port")
			_ = c.GetBool("app.debug")
			_ = c.IsSet("app.name")
			_ = c.AllSettings()
		}()
	}

	// 并发写入
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Set("dynamic.key", i)
		}(i)
	}

	wg.Wait()

	// 验证并发访问后配置仍可读取
	assert.Equal(t, "test-app", c.GetString("app.name"))
}

func TestStartStopWatch(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	// 开始监听
	err := c.StartWatch()
	assert.NoError(t, err)

	// 重复启动不应创建额外监听器
	err = c.StartWatch()
	assert.NoError(t, err)

	oldWatcher := c.watcher
	require.NotNil(t, oldWatcher)

	// 停止监听并释放底层 fsnotify 监听器。
	c.StopWatch()
	assert.Nil(t, oldWatcher.WatchList())
	assert.False(t, c.watching)
	assert.Nil(t, c.watcher)

	// 重新启动时创建新的监听器，旧监听器已经释放。
	require.NoError(t, c.StartWatch())
	require.NotNil(t, c.watcher)
	assert.NotSame(t, oldWatcher, c.watcher)
	c.Close()
}

func TestStartWatchRequiresLoadedConfig(t *testing.T) {
	c := New(WithConfigFile(filepath.Join(t.TempDir(), "config.yaml")))
	assert.Error(t, c.StartWatch())
}

func TestGetStringMap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "config.yaml", testYAML)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	m := c.GetStringMap("database")
	assert.NotNil(t, m)
	assert.Equal(t, "localhost", m["host"])
}

func TestGetStringMapString(t *testing.T) {
	dir := t.TempDir()
	content := `
labels:
  env: production
  team: backend
`
	cfgPath := writeTestConfig(t, dir, "config.yaml", content)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	m := c.GetStringMapString("labels")
	assert.Equal(t, "production", m["env"])
	assert.Equal(t, "backend", m["team"])
}

func TestGetFloat64(t *testing.T) {
	dir := t.TempDir()
	content := `
thresholds:
  rate: 0.95
`
	cfgPath := writeTestConfig(t, dir, "config.yaml", content)

	c := New(WithConfigFile(cfgPath))
	require.NoError(t, c.Load())

	assert.InDelta(t, 0.95, c.GetFloat64("thresholds.rate"), 0.001)
}

func TestViper(t *testing.T) {
	c := New()
	assert.NotNil(t, c.Viper())
}
