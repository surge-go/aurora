package logger

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger 是项目对外暴露的结构化 logger。
//
// zap logger 只作为内部实现保存，业务代码通过本类型和 Field 构造函数写日志。
type Logger struct {
	base *zap.Logger
	text *zap.Logger
}

// SugaredLogger 是项目对外暴露的低类型约束 logger。
type SugaredLogger struct {
	base *zap.SugaredLogger
	core zapcore.Core
}

// Nop 返回不产生输出的 logger，适合可选日志和测试场景。
func Nop() *Logger {
	return &Logger{base: zap.NewNop(), text: zap.NewNop()}
}

// Debug 记录 debug 级别日志。
func (l *Logger) Debug(msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.DebugLevel) {
		return
	}
	l.base.Debug(msg, toZapFields(fields)...)
}

// Info 记录 info 级别日志。
func (l *Logger) Info(msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.InfoLevel) {
		return
	}
	l.base.Info(msg, toZapFields(fields)...)
}

// InfoText 记录已格式化的纯文本 info 日志，不添加时间、字段或 JSON 外层。
// 遵循同一日志级别、输出目标和采样配置，且与结构化日志共用 writer。
// 末尾换行统一为一个，适合访问日志等已有完整格式的消息。
func (l *Logger) InfoText(msg string) {
	if l == nil || l.text == nil || !l.text.Core().Enabled(zapcore.InfoLevel) {
		return
	}
	l.text.Info(strings.TrimRight(msg, "\r\n"))
}

// Warn 记录 warn 级别日志。
func (l *Logger) Warn(msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.WarnLevel) {
		return
	}
	l.base.Warn(msg, toZapFields(fields)...)
}

// Error 记录 error 级别日志。
func (l *Logger) Error(msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.ErrorLevel) {
		return
	}
	l.base.Error(msg, toZapFields(fields)...)
}

// DPanic 记录 dpanic 级别日志。
func (l *Logger) DPanic(msg string, fields ...Field) {
	l.base.DPanic(msg, toZapFields(fields)...)
}

// Panic 记录 panic 级别日志。
func (l *Logger) Panic(msg string, fields ...Field) {
	l.base.Panic(msg, toZapFields(fields)...)
}

// Fatal 记录 fatal 级别日志并退出进程。
func (l *Logger) Fatal(msg string, fields ...Field) {
	l.base.Fatal(msg, toZapFields(fields)...)
}

// DebugContext 记录带当前 trace context 的 debug 日志。
func (l *Logger) DebugContext(ctx context.Context, msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.DebugLevel) {
		return
	}
	l.base.Debug(msg, contextZapFields(ctx, fields)...)
}

// InfoContext 记录带当前 trace context 的 info 日志。
func (l *Logger) InfoContext(ctx context.Context, msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.InfoLevel) {
		return
	}
	l.base.Info(msg, contextZapFields(ctx, fields)...)
}

// WarnContext 记录带当前 trace context 的 warn 日志。
func (l *Logger) WarnContext(ctx context.Context, msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.WarnLevel) {
		return
	}
	l.base.Warn(msg, contextZapFields(ctx, fields)...)
}

// ErrorContext 记录带当前 trace context 的 error 日志。
func (l *Logger) ErrorContext(ctx context.Context, msg string, fields ...Field) {
	if !l.base.Core().Enabled(zapcore.ErrorLevel) {
		return
	}
	l.base.Error(msg, contextZapFields(ctx, fields)...)
}

// DPanicContext 记录带当前 trace context 的 dpanic 日志。
func (l *Logger) DPanicContext(ctx context.Context, msg string, fields ...Field) {
	l.base.DPanic(msg, contextZapFields(ctx, fields)...)
}

// PanicContext 记录带当前 trace context 的 panic 日志。
func (l *Logger) PanicContext(ctx context.Context, msg string, fields ...Field) {
	l.base.Panic(msg, contextZapFields(ctx, fields)...)
}

// FatalContext 记录带当前 trace context 的 fatal 日志并退出进程。
func (l *Logger) FatalContext(ctx context.Context, msg string, fields ...Field) {
	l.base.Fatal(msg, contextZapFields(ctx, fields)...)
}

// With 返回携带固定字段的 logger。
func (l *Logger) With(fields ...Field) *Logger {
	return &Logger{base: l.base.With(toZapFields(filterTraceFields(fields))...), text: l.text}
}

// Named 返回带 logger 名称的 logger。
func (l *Logger) Named(name string) *Logger {
	return &Logger{base: l.base.Named(name), text: l.text}
}

// Sync 刷新 logger 输出。
func (l *Logger) Sync() error {
	return l.base.Sync()
}

// Sugar 返回共享同一底层 Core 的 SugaredLogger。
func (l *Logger) Sugar() *SugaredLogger {
	return &SugaredLogger{base: l.base.Sugar(), core: l.base.Core()}
}

// Debug 记录 debug 级别日志。
func (l *SugaredLogger) Debug(args ...interface{}) {
	if !l.core.Enabled(zapcore.DebugLevel) {
		return
	}
	l.base.Debug(toZapSugarArgs(args)...)
}

// Info 记录 info 级别日志。
func (l *SugaredLogger) Info(args ...interface{}) {
	if !l.core.Enabled(zapcore.InfoLevel) {
		return
	}
	l.base.Info(toZapSugarArgs(args)...)
}

// Warn 记录 warn 级别日志。
func (l *SugaredLogger) Warn(args ...interface{}) {
	if !l.core.Enabled(zapcore.WarnLevel) {
		return
	}
	l.base.Warn(toZapSugarArgs(args)...)
}

// Error 记录 error 级别日志。
func (l *SugaredLogger) Error(args ...interface{}) {
	if !l.core.Enabled(zapcore.ErrorLevel) {
		return
	}
	l.base.Error(toZapSugarArgs(args)...)
}

// Debugf 记录格式化 debug 日志。
func (l *SugaredLogger) Debugf(template string, args ...interface{}) {
	l.base.Debugf(template, args...)
}

// Infof 记录格式化 info 日志。
func (l *SugaredLogger) Infof(template string, args ...interface{}) {
	l.base.Infof(template, args...)
}

// Warnf 记录格式化 warn 日志。
func (l *SugaredLogger) Warnf(template string, args ...interface{}) {
	l.base.Warnf(template, args...)
}

// Errorf 记录格式化 error 日志。
func (l *SugaredLogger) Errorf(template string, args ...interface{}) {
	l.base.Errorf(template, args...)
}

// Debugw 记录带键值字段的 debug 日志。
func (l *SugaredLogger) Debugw(msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.DebugLevel) {
		return
	}
	l.base.Debugw(msg, toZapSugarArgs(keysAndValues)...)
}

// Infow 记录带键值字段的 info 日志。
func (l *SugaredLogger) Infow(msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.InfoLevel) {
		return
	}
	l.base.Infow(msg, toZapSugarArgs(keysAndValues)...)
}

// Warnw 记录带键值字段的 warn 日志。
func (l *SugaredLogger) Warnw(msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.WarnLevel) {
		return
	}
	l.base.Warnw(msg, toZapSugarArgs(keysAndValues)...)
}

// Errorw 记录带键值字段的 error 日志。
func (l *SugaredLogger) Errorw(msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.ErrorLevel) {
		return
	}
	l.base.Errorw(msg, toZapSugarArgs(keysAndValues)...)
}

// DPanic 记录 dpanic 级别日志。
func (l *SugaredLogger) DPanic(args ...interface{}) {
	l.base.DPanic(toZapSugarArgs(args)...)
}

// Panic 记录 panic 级别日志。
func (l *SugaredLogger) Panic(args ...interface{}) {
	l.base.Panic(toZapSugarArgs(args)...)
}

// Fatal 记录 fatal 级别日志并退出进程。
func (l *SugaredLogger) Fatal(args ...interface{}) {
	l.base.Fatal(toZapSugarArgs(args)...)
}

// DebugContext 记录带当前 trace context 的 debug 日志。
func (l *SugaredLogger) DebugContext(ctx context.Context, args ...interface{}) {
	if !l.core.Enabled(zapcore.DebugLevel) {
		return
	}
	l.withContext(ctx).Debug(toZapSugarContextArgs(ctx, args)...)
}

// InfoContext 记录带当前 trace context 的 info 日志。
func (l *SugaredLogger) InfoContext(ctx context.Context, args ...interface{}) {
	if !l.core.Enabled(zapcore.InfoLevel) {
		return
	}
	l.withContext(ctx).Info(toZapSugarContextArgs(ctx, args)...)
}

// WarnContext 记录带当前 trace context 的 warn 日志。
func (l *SugaredLogger) WarnContext(ctx context.Context, args ...interface{}) {
	if !l.core.Enabled(zapcore.WarnLevel) {
		return
	}
	l.withContext(ctx).Warn(toZapSugarContextArgs(ctx, args)...)
}

// ErrorContext 记录带当前 trace context 的 error 日志。
func (l *SugaredLogger) ErrorContext(ctx context.Context, args ...interface{}) {
	if !l.core.Enabled(zapcore.ErrorLevel) {
		return
	}
	l.withContext(ctx).Error(toZapSugarContextArgs(ctx, args)...)
}

// DPanicContext 记录带当前 trace context 的 dpanic 日志。
func (l *SugaredLogger) DPanicContext(ctx context.Context, args ...interface{}) {
	l.withContext(ctx).DPanic(toZapSugarContextArgs(ctx, args)...)
}

// PanicContext 记录带当前 trace context 的 panic 日志。
func (l *SugaredLogger) PanicContext(ctx context.Context, args ...interface{}) {
	l.withContext(ctx).Panic(toZapSugarContextArgs(ctx, args)...)
}

// FatalContext 记录带当前 trace context 的 fatal 日志并退出进程。
func (l *SugaredLogger) FatalContext(ctx context.Context, args ...interface{}) {
	l.withContext(ctx).Fatal(toZapSugarContextArgs(ctx, args)...)
}

// DebugfContext 记录带当前 trace context 的格式化 debug 日志。
func (l *SugaredLogger) DebugfContext(ctx context.Context, template string, args ...interface{}) {
	if !l.core.Enabled(zapcore.DebugLevel) {
		return
	}
	l.withContext(ctx).Debugf(template, args...)
}

// InfofContext 记录带当前 trace context 的格式化 info 日志。
func (l *SugaredLogger) InfofContext(ctx context.Context, template string, args ...interface{}) {
	if !l.core.Enabled(zapcore.InfoLevel) {
		return
	}
	l.withContext(ctx).Infof(template, args...)
}

// WarnfContext 记录带当前 trace context 的格式化 warn 日志。
func (l *SugaredLogger) WarnfContext(ctx context.Context, template string, args ...interface{}) {
	if !l.core.Enabled(zapcore.WarnLevel) {
		return
	}
	l.withContext(ctx).Warnf(template, args...)
}

// ErrorfContext 记录带当前 trace context 的格式化 error 日志。
func (l *SugaredLogger) ErrorfContext(ctx context.Context, template string, args ...interface{}) {
	if !l.core.Enabled(zapcore.ErrorLevel) {
		return
	}
	l.withContext(ctx).Errorf(template, args...)
}

// DebugwContext 记录带当前 trace context 的结构化 debug 日志。
func (l *SugaredLogger) DebugwContext(ctx context.Context, msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.DebugLevel) {
		return
	}
	l.withContext(ctx).Debugw(msg, toZapSugarContextKVArgs(ctx, keysAndValues)...)
}

// InfowContext 记录带当前 trace context 的结构化 info 日志。
func (l *SugaredLogger) InfowContext(ctx context.Context, msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.InfoLevel) {
		return
	}
	l.withContext(ctx).Infow(msg, toZapSugarContextKVArgs(ctx, keysAndValues)...)
}

// WarnwContext 记录带当前 trace context 的结构化 warn 日志。
func (l *SugaredLogger) WarnwContext(ctx context.Context, msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.WarnLevel) {
		return
	}
	l.withContext(ctx).Warnw(msg, toZapSugarContextKVArgs(ctx, keysAndValues)...)
}

// ErrorwContext 记录带当前 trace context 的结构化 error 日志。
func (l *SugaredLogger) ErrorwContext(ctx context.Context, msg string, keysAndValues ...interface{}) {
	if !l.core.Enabled(zapcore.ErrorLevel) {
		return
	}
	l.withContext(ctx).Errorw(msg, toZapSugarContextKVArgs(ctx, keysAndValues)...)
}

// With 返回携带固定键值字段的 SugaredLogger。
func (l *SugaredLogger) With(keysAndValues ...interface{}) *SugaredLogger {
	return &SugaredLogger{base: l.base.With(filterTraceSugarArgs(toZapSugarArgs(keysAndValues))...), core: l.core}
}

// Named 返回带 logger 名称的 SugaredLogger。
func (l *SugaredLogger) Named(name string) *SugaredLogger {
	return &SugaredLogger{base: l.base.Named(name), core: l.core}
}

// Sync 刷新 SugaredLogger 输出。
func (l *SugaredLogger) Sync() error {
	return l.base.Sync()
}

func (l *SugaredLogger) withContext(ctx context.Context) *zap.SugaredLogger {
	fields := contextZapFields(ctx, nil)
	if len(fields) == 0 {
		return l.base
	}
	return l.base.With(toSugarFieldArgs(fields)...)
}

func contextZapFields(ctx context.Context, fields []Field) []zap.Field {
	traceFields := traceFields(ctx)
	if len(traceFields) == 0 && len(fields) == 0 {
		return nil
	}
	allFields := make([]Field, 0, len(traceFields)+len(fields))
	allFields = append(allFields, traceFields...)
	for _, field := range fields {
		if len(traceFields) > 0 && isTraceField(field) {
			continue
		}
		allFields = append(allFields, field)
	}
	return toZapFields(allFields)
}

func isTraceField(field Field) bool {
	return isTraceKey(field.key)
}

func isTraceKey(key string) bool {
	switch key {
	case "trace_id", "span_id", "trace_sampled":
		return true
	default:
		return false
	}
}

func toZapSugarArgs(args []interface{}) []interface{} {
	var converted []interface{}
	for i, arg := range args {
		field, ok := arg.(Field)
		if !ok {
			if converted != nil {
				converted = append(converted, arg)
			}
			continue
		}
		if converted == nil {
			converted = make([]interface{}, 0, len(args))
			converted = append(converted, args[:i]...)
		}
		if !field.isEmpty() {
			converted = append(converted, field.toZap())
		}
	}
	if converted == nil {
		return args
	}
	return converted
}

func toZapSugarContextArgs(ctx context.Context, args []interface{}) []interface{} {
	converted := toZapSugarArgs(args)
	if len(traceFields(ctx)) == 0 {
		return converted
	}
	return filterTraceZapFields(converted)
}

func toZapSugarContextKVArgs(ctx context.Context, args []interface{}) []interface{} {
	converted := toZapSugarArgs(args)
	if len(traceFields(ctx)) == 0 {
		return converted
	}
	return filterTraceSugarArgs(converted)
}

func filterTraceFields(fields []Field) []Field {
	var filtered []Field
	for i, field := range fields {
		if !isTraceField(field) {
			if filtered != nil {
				filtered = append(filtered, field)
			}
			continue
		}
		if filtered == nil {
			filtered = append([]Field{}, fields[:i]...)
		}
	}
	if filtered == nil {
		return fields
	}
	return filtered
}

func filterTraceZapFields(args []interface{}) []interface{} {
	var filtered []interface{}
	for i, arg := range args {
		field, ok := arg.(zap.Field)
		if !ok || !isTraceKey(field.Key) {
			if filtered != nil {
				filtered = append(filtered, arg)
			}
			continue
		}
		if filtered == nil {
			filtered = append([]interface{}{}, args[:i]...)
		}
	}
	if filtered == nil {
		return args
	}
	return filtered
}

func filterTraceSugarArgs(args []interface{}) []interface{} {
	var filtered []interface{}
	for i := 0; i < len(args); {
		if field, ok := args[i].(zap.Field); ok {
			if isTraceKey(field.Key) {
				if filtered == nil {
					filtered = append([]interface{}{}, args[:i]...)
				}
				i++
				continue
			}
			if filtered != nil {
				filtered = append(filtered, args[i])
			}
			i++
			continue
		}

		key, isString := args[i].(string)
		if isString && isTraceKey(key) {
			if filtered == nil {
				filtered = append([]interface{}{}, args[:i]...)
			}
			i++
			if i < len(args) {
				i++
			}
			continue
		}
		if filtered != nil {
			filtered = append(filtered, args[i])
		}
		i++
		if i < len(args) {
			if filtered != nil {
				filtered = append(filtered, args[i])
			}
			i++
		}
	}
	if filtered == nil {
		return args
	}
	return filtered
}

func toSugarFieldArgs(fields []zap.Field) []interface{} {
	args := make([]interface{}, len(fields))
	for i, field := range fields {
		args[i] = field
	}
	return args
}

func traceFields(ctx context.Context) []Field {
	if ctx == nil {
		return nil
	}

	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}

	return []Field{
		String("trace_id", sc.TraceID().String()),
		String("span_id", sc.SpanID().String()),
		Bool("trace_sampled", sc.IsSampled()),
	}
}
