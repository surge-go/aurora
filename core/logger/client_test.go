package logger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestNewWritesJSONFile(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "app.log")

	log, err := New(&Config{
		Level:           LevelInfo,
		Format:          FormatJSON,
		Output:          OutputFile,
		File:            logFile,
		AddCaller:       true,
		StacktraceLevel: StacktraceLevelNone,
		InitialFields: map[string]string{
			"service": "qi",
		},
		Rotation: &RotationConfig{
			MaxSize:    10,
			MaxBackups: 2,
			MaxAge:     7,
			Compress:   true,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("hello", String("request_id", "req-1"))
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	log.Info("after-sync")
	if err := log.Sync(); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	wantContains := []string{
		`"level":"info"`,
		`"msg":"hello"`,
		`"service":"qi"`,
		`"request_id":"req-1"`,
		`"msg":"after-sync"`,
	}
	for _, want := range wantContains {
		if !strings.Contains(content, want) {
			t.Fatalf("log content = %s, want to contain %q", content, want)
		}
	}
}

func TestNewAppliesEncoderConfig(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "app.log")

	log, err := New(&Config{
		Level:           LevelDebug,
		Format:          FormatJSON,
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
		Encoder: &EncoderConfig{
			MessageKey:       "message",
			LevelKey:         "severity",
			TimeKey:          "time",
			StacktraceKey:    "stack",
			TimeEncoding:     "epoch",
			DurationEncoding: "millis",
			LevelEncoding:    "capital",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Debug("configured")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	wantContains := []string{
		`"severity":"DEBUG"`,
		`"message":"configured"`,
		`"time":`,
	}
	for _, want := range wantContains {
		if !strings.Contains(content, want) {
			t.Fatalf("log content = %s, want to contain %q", content, want)
		}
	}
}

func TestNewFiltersByLevel(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "app.log")

	log, err := New(&Config{
		Level:           LevelWarn,
		Format:          FormatJSON,
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("hidden")
	log.Warn("visible")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	if strings.Contains(content, "hidden") {
		t.Fatalf("log content = %s, want info log filtered", content)
	}
	if !strings.Contains(content, "visible") {
		t.Fatalf("log content = %s, want warn log visible", content)
	}
}

func TestLoggerSugar(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "sugar.log")

	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Sugar().Infow("hello", "component", "test")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	if !strings.Contains(content, `"component":"test"`) {
		t.Fatalf("log content = %s, want sugared field", content)
	}
}

func TestLoggerSugarAcceptsPackageFields(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "sugar-fields.log")

	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Sugar().Infow("hello", String("component", "test"), Error(errors.New("boom")))
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	for _, want := range []string{`"component":"test"`, `"error":"boom"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("log content = %s, want to contain %q", content, want)
		}
	}
}

func TestLoggerFieldZeroValueIsSafe(t *testing.T) {
	var field Field
	Nop().Info("zero field", field)
}

func TestLoggerCallerPointsToBusinessCallsite(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "caller.log")
	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		AddCaller:       true,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("caller check")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	if strings.Contains(content, `"caller":"logger.go:`) || !strings.Contains(content, "client_test.go:") {
		t.Fatalf("log content = %s, want caller in client_test.go", content)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	log, err := New(&Config{
		Output: OutputFile,
	})
	if err == nil {
		_ = log.Sync()
		t.Fatal("New() error = nil, want error")
	}
}

func TestNewRejectsNilConfig(t *testing.T) {
	log, err := New(nil)
	if err == nil {
		_ = log.Sync()
		t.Fatal("New() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "logger config is nil") {
		t.Fatalf("New() error = %v, want nil config error", err)
	}
}

func TestNewTrimsFilePath(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "trimmed.log")

	log, err := New(&Config{
		Output:          OutputFile,
		File:            " " + logFile + " ",
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("trimmed")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	if !strings.Contains(content, "trimmed") {
		t.Fatalf("log content = %s, want trimmed file path to be used", content)
	}
}

func TestNewFileWithoutRotationDoesNotCreateBackups(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")

	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	log.Info("first")
	log.Info("second")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "app.log" {
		t.Fatalf("files = %v, want only app.log", entries)
	}
}

func TestConsoleFileOutputDoesNotUseColorCodes(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "app.log")

	log, err := New(&Config{
		Format:          FormatConsole,
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	log.Info("plain")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	if strings.Contains(content, "\x1b[") {
		t.Fatalf("log content contains ANSI escape sequence: %q", content)
	}
}

func TestLoggerInfoContextAddsTraceFields(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "context.log")
	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("TraceIDFromHex() error = %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatalf("SpanIDFromHex() error = %v", err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	log.InfoContext(ctx, "context log",
		String("component", "test"),
		String("trace_id", "forged"),
		Int64("count", 2),
		Error(errors.New("boom")),
	)
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	for _, want := range []string{
		`"trace_id":"0123456789abcdef0123456789abcdef"`,
		`"span_id":"0123456789abcdef"`,
		`"trace_sampled":true`,
		`"component":"test"`,
		`"count":2`,
		`"error":"boom"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("log content = %s, want to contain %q", content, want)
		}
	}
	if strings.Count(content, `"trace_id"`) != 1 || strings.Contains(content, "forged") {
		t.Fatalf("log content = %s, want one authoritative trace_id", content)
	}
}

func TestLoggerInfoContextOmitsInvalidTrace(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "no-context.log")
	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.InfoContext(context.Background(), "without trace")
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content := readFile(t, logFile)
	if strings.Contains(content, "trace_id") || strings.Contains(content, "span_id") {
		t.Fatalf("log content = %s, want no trace fields", content)
	}
}

func TestLoggerWithRejectsReservedTraceFields(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "fixed-trace.log")
	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	log.With(String("trace_id", "forged")).InfoContext(ctx, "fixed field")
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, logFile)
	if strings.Contains(content, "forged") || strings.Count(content, `"trace_id"`) != 1 {
		t.Fatalf("log content = %s, want one context trace_id", content)
	}
}

func TestSugaredContextRejectsReservedTraceFields(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "sugar-trace.log")
	log, err := New(&Config{
		Output:          OutputFile,
		File:            logFile,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	log.Sugar().InfowContext(ctx, "sugar field", "trace_id", "forged", "component", "test")
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, logFile)
	if strings.Contains(content, "forged") || strings.Count(content, `"trace_id"`) != 1 || !strings.Contains(content, `"component":"test"`) {
		t.Fatalf("log content = %s, want one context trace_id and component", content)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(content)
}

func TestInfoTextKeepsStructuredLogsAndSharedOutput(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "mixed.log")
	log, err := New(&Config{
		Level: LevelInfo, Format: FormatJSON, Output: OutputFile, File: logFile,
		AddCaller: true, StacktraceLevel: StacktraceLevelNone,
		InitialFields: map[string]string{"service": "qi"},
		Rotation:      &RotationConfig{MaxSize: 1, MaxBackups: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	derived := log.With(String("component", "http")).Named("server")
	derived.InfoText("[QI] request\n")
	derived.Info("应用启动中")
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}
	// Sync 后仍可通过相同轮转 writer 写入，纯文本和结构化输出都保留。
	derived.InfoText("[QI] after-sync")
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(readFile(t, logFile), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "[QI] request" || lines[2] != "[QI] after-sync" {
		t.Fatalf("unexpected text or extra metadata: %q", lines)
	}
	var structured map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &structured); err != nil {
		t.Fatalf("structured log no longer JSON: %v", err)
	}
	for key, want := range map[string]string{
		"msg": "应用启动中", "level": "info", "service": "qi", "component": "http", "logger": "server",
	} {
		if structured[key] != want {
			t.Fatalf("field %s = %v, want %q", key, structured[key], want)
		}
	}
}

func TestInfoTextRespectsLevelAndNop(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "warn.log")
	log, err := New(&Config{
		Level: LevelWarn, Output: OutputFile, File: logFile, StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	log.With(String("component", "http")).Named("server").InfoText("hidden")
	log.Warn("visible")
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, logFile)
	if strings.Contains(content, "hidden") || !strings.Contains(content, "visible") {
		t.Fatalf("unexpected level filtering: %s", content)
	}
	Nop().With(String("component", "http")).Named("server").InfoText("hidden")
}

func TestLoggerWritesTextAndJSONToConsoleAndFile(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotation=%t", rotate), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stdout
			os.Stdout = writer
			t.Cleanup(func() {
				os.Stdout = previous
				_ = writer.Close()
				_ = reader.Close()
			})
			path := filepath.Join(t.TempDir(), "runtime", "logs", "qi.log")
			cfg := &Config{
				Outputs: []Output{OutputStdout, OutputFile}, File: path,
				StacktraceLevel: StacktraceLevelNone,
			}
			if rotate {
				cfg.Rotation = &RotationConfig{MaxSize: 1, MaxBackups: 1}
			}
			log, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			log.Info("应用启动中", String("service", "qi"))
			log.InfoText("[QI] GET /health/live")
			if err := log.Sync(); err != nil {
				t.Fatalf("multi-output Sync: %v", err)
			}
			log.InfoText("[QI] after-sync")
			if err := log.Sync(); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			console, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			file := readFile(t, path)
			if string(console) != file {
				t.Fatalf("console and file differ: console=%q file=%q", console, file)
			}
			if strings.Count(file, "\n") != 3 || !strings.Contains(file, `"msg":"应用启动中"`) || !strings.Contains(file, "[QI] GET /health/live\n") {
				t.Fatalf("missing or duplicated output: %q", file)
			}
		})
	}
}
