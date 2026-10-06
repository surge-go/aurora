package logger

import (
	"context"
	"log/slog"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Slog 返回共享当前日志输出的 slog 适配器，供数据库等标准日志 API 使用。
func (l *Logger) Slog() *slog.Logger {
	if l == nil {
		l = Nop()
	}
	return slog.New(&slogHandler{
		log:  l,
		base: l.base.WithOptions(zap.AddCallerSkip(2)),
	})
}

type slogHandler struct {
	log    *Logger
	base   *zap.Logger
	fields []Field
	groups []string
}

func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.log.base.Core().Enabled(slogZapLevel(level))
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := append([]Field(nil), h.fields...)
	record.Attrs(func(attr slog.Attr) bool {
		fields = append(fields, slogFields(h.groups, attr)...)
		return true
	})
	// 直接写入对应级别，避免 slog 的高自定义级别被映射为 Fatal。
	h.base.Log(slogZapLevel(record.Level), record.Message, contextZapFields(ctx, fields)...)
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	result := *h
	result.fields = append([]Field(nil), h.fields...)
	for _, attr := range attrs {
		result.fields = append(result.fields, slogFields(h.groups, attr)...)
	}
	return &result
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	result := *h
	result.groups = append(append([]string(nil), h.groups...), name)
	return &result
}

func slogFields(groups []string, attr slog.Attr) []Field {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return nil
	}
	if attr.Value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			groups = append(append([]string(nil), groups...), attr.Key)
		}
		var result []Field
		for _, child := range attr.Value.Group() {
			result = append(result, slogFields(groups, child)...)
		}
		return result
	}
	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}
	return []Field{Any(key, attr.Value.Any())}
}

func slogZapLevel(level slog.Level) zapcore.Level {
	switch {
	case level >= slog.LevelError:
		return zapcore.ErrorLevel
	case level >= slog.LevelWarn:
		return zapcore.WarnLevel
	case level >= slog.LevelInfo:
		return zapcore.InfoLevel
	default:
		return zapcore.DebugLevel
	}
}
