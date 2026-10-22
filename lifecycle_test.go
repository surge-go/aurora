package aurora

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLifecycleGeneratesDefaultConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	lifecycle, err := NewLifecycle()
	if err != nil {
		t.Fatalf("NewLifecycle() error = %v", err)
	}
	if lifecycle.Engine == nil || lifecycle.Config == nil {
		t.Fatal("NewLifecycle() returned an incomplete lifecycle")
	}
	if lifecycle.Logger == nil {
		t.Fatal("NewLifecycle() did not initialize logger")
	}
	if lifecycle.Tracing != nil || lifecycle.Metrics != nil || lifecycle.Database != nil || lifecycle.Redis != nil || len(lifecycle.authManagers) != 0 || lifecycle.MQPublisher != nil || lifecycle.MQConsumer != nil {
		t.Fatal("commented optional configuration unexpectedly initialized a runtime")
	}
	if got := lifecycle.config.Addr; got != ":8080" {
		t.Fatalf("default address = %q, want :8080", got)
	}
	if _, err := os.Stat(filepath.Join("config", "aurora.yaml")); err != nil {
		t.Fatalf("default config was not created: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("config", "aurora.yaml"))
	if err != nil {
		t.Fatalf("read default config: %v", err)
	}
	for _, section := range []string{"logger:"} {
		if !strings.Contains(string(data), "\n"+section+"\n") {
			t.Fatalf("default config is missing %q section", section)
		}
	}
	for _, section := range []string{"#tracing:\n", "#metrics:\n", "#database:\n", "#redis:\n", "#auth:\n", "#mq:\n", "#  rabbitmq:\n", "#  kafka:\n"} {
		if !strings.Contains(string(data), "\n"+section) {
			t.Fatalf("default config is missing %q section", strings.TrimSpace(section))
		}
	}
	if strings.Contains(string(data), "\nenabled: false") {
		t.Fatal("default config contains an active enabled: false setting")
	}

	if err := lifecycle.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := lifecycle.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestNewLifecycleInitializesConfiguredAuthWithSharedRedis(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	config := `aurora:
  addr: ":0"
  mode: test
redis:
  mode: standalone
  addrs: ["127.0.0.1:6379"]
auth:
  admin:
    key_prefix: "service:auth:{sessions}"
    signing_secret: "kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"
    issuer: aurora-test
    audience: [aurora-client]
    access_ttl: 15m
    refresh_ttl: 1h
    max_session_ttl: 2h
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	lifecycle, err := NewLifecycle(path)
	if err != nil {
		t.Fatalf("NewLifecycle() error = %v", err)
	}
	if lifecycle.Redis == nil || len(lifecycle.authManagers) != 1 {
		t.Fatal("configured Redis and auth were not initialized")
	}
	if err := lifecycle.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNewLifecycleAuthRequiresSigningSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	config := `aurora:
  addr: ":0"
  mode: test
redis:
  mode: standalone
  addrs: ["127.0.0.1:6379"]
auth:
  admin:
    issuer: aurora-test
    audience: [aurora-client]
    max_session_ttl: 2h
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewLifecycle(path)
	if err == nil || !strings.Contains(err.Error(), "signing key") {
		t.Fatalf("NewLifecycle() error = %v, want signing secret error", err)
	}
}

func TestNewLifecycleLoadsExplicitConfigWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	original := "aurora:\n  addr: ':0'\n  mode: test\nlogger:\n  level: warn\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	lifecycle, err := NewLifecycle(path)
	if err != nil {
		t.Fatalf("NewLifecycle(%q) error = %v", path, err)
	}
	defer lifecycle.Close(context.Background())

	if got := lifecycle.config.Addr; got != ":0" {
		t.Fatalf("configured address = %q, want :0", got)
	}
	if lifecycle.Logger == nil {
		t.Fatal("configured logger was not initialized")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("explicit config was modified: %q", string(data))
	}
}

func TestNewLifecycleInitializesConfiguredCoreResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	config := `aurora:
  addr: ":0"
  mode: test
database:
  driver: sqlite
  dsn: "RESOURCE_DSN"
redis:
  mode: standalone
  addrs: ["127.0.0.1:6379"]
mq:
  driver: redis
  redis:
    group: aurora
    consumer: test
`
	dsn := filepath.Join(t.TempDir(), "aurora.db")
	config = strings.Replace(config, "RESOURCE_DSN", dsn, 1)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	lifecycle, err := NewLifecycle(path)
	if err != nil {
		t.Fatalf("NewLifecycle() error = %v", err)
	}
	if lifecycle.Database == nil || lifecycle.Redis == nil || lifecycle.MQPublisher == nil || lifecycle.MQConsumer == nil {
		t.Fatal("configured core resources were not initialized")
	}
	if err := lifecycle.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNewLifecycleRejectsInvalidConfigPathArguments(t *testing.T) {
	if _, err := NewLifecycle("one.yaml", "two.yaml"); err == nil {
		t.Fatal("NewLifecycle() accepted more than one path")
	}
	if _, err := NewLifecycle(""); err == nil || !strings.Contains(err.Error(), "config path") {
		t.Fatalf("NewLifecycle(empty path) error = %v", err)
	}
}

func TestLifecycleRejectsDuplicateRunWithoutClosingActiveServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	if err := os.WriteFile(path, []byte("aurora:\n  addr: ':0'\n  mode: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- lifecycle.Run(ctx) }()
	for i := 0; i < 100 && !lifecycle.Listening(); i++ {
		// Keep the test independent of scheduler timing while the listener starts.
		select {
		case err := <-runDone:
			t.Fatalf("first Run returned before listening: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	if !lifecycle.Listening() {
		cancel()
		<-runDone
		t.Fatal("first Run did not start listening")
	}

	if err := lifecycle.Run(context.Background()); err == nil || err.Error() != "lifecycle already running" {
		t.Fatalf("duplicate Run error = %v, want lifecycle already running", err)
	}
	if !lifecycle.Listening() {
		t.Fatal("duplicate Run stopped the active server")
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("first Run returned error = %v", err)
	}
	if err := lifecycle.Run(context.Background()); err == nil || err.Error() != "lifecycle is closed" {
		t.Fatalf("Run after completion error = %v, want lifecycle is closed", err)
	}
}

func TestLifecycleCannotRunAfterShutdownBeforeStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	if err := os.WriteFile(path, []byte("aurora:\n  addr: ':0'\n  mode: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := lifecycle.Run(context.Background()); err == nil || err.Error() != "lifecycle is closed" {
		t.Fatalf("Run after Shutdown error = %v, want lifecycle is closed", err)
	}
}
