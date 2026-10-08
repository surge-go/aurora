package logger

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlogSharesOutputAndPreservesAttributeGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slog.log")
	log, err := New(&Config{Output: OutputFile, File: path, StacktraceLevel: StacktraceLevelNone})
	if err != nil {
		t.Fatal(err)
	}
	adapter := log.Slog().With("component", "db").WithGroup("query")
	adapter.Debug("hidden")
	adapter.Log(context.Background(), slog.Level(100), "failed", slog.Group("details", slog.Int("count", 3)))
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	for _, want := range []string{`"component":"db"`, `"query.details.count":3`, `"level":"error"`, `"msg":"failed"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %s in %s", want, content)
		}
	}
	if strings.Contains(content, "hidden") {
		t.Fatal("debug log bypassed configured level")
	}
}

func TestSlogCallerPointsToBusinessCallsite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slog-caller.log")
	log, err := New(&Config{
		Output:          OutputFile,
		File:            path,
		AddCaller:       true,
		StacktraceLevel: StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	log.Slog().Info("caller check")
	if err := log.Sync(); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, path)
	if !strings.Contains(content, "slog_test.go:") || strings.Contains(content, "slog/logger.go:") {
		t.Fatalf("log content = %s, want business caller", content)
	}
}
