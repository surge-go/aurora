package logger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// New 根据 Config 创建 Logger。
//
// 函数会先执行 Config.Validate，然后按配置构造内部 zapcore.Core。
func New(cfg *Config) (*Logger, error) {
	if cfg == nil {
		return nil, errors.New("logger config is nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	level, err := parseZapLevel(cfg.levelOrDefault())
	if err != nil {
		return nil, err
	}

	// 结构化日志和纯文本日志共用 writer，保证并发写入与文件轮转一致。
	writer, err := buildWriteSyncer(cfg)
	if err != nil {
		return nil, err
	}
	core, err := buildCore(cfg, level, writer)
	if err != nil {
		return nil, err
	}

	options, err := buildOptions(cfg)
	if err != nil {
		return nil, err
	}

	textCore := zapcore.NewCore(zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey: "msg", LineEnding: "\n",
	}), writer, level)
	if cfg.Sampling != nil && cfg.Sampling.Enabled {
		textCore = zapcore.NewSamplerWithOptions(textCore, time.Second, cfg.Sampling.Initial, cfg.Sampling.Thereafter)
	}
	return &Logger{
		base: zap.New(core, options...),
		text: zap.New(textCore, zap.ErrorOutput(buildErrorOutput(cfg.ErrorOutput))),
	}, nil
}

// buildCore 构造 zapcore.Core。
//
// Core 是 zap 的核心执行单元，负责把日志级别判断、encoder 编码和 writer 写入串起来。
// 如果启用了 Sampling，会在 Core 外层包一层采样器，用于削减短时间内重复日志的输出量。
func buildCore(cfg *Config, level zapcore.Level, writer zapcore.WriteSyncer) (zapcore.Core, error) {
	encoder, err := buildEncoder(cfg.formatOrDefault(), cfg.outputsOrDefault(), cfg.Encoder)
	if err != nil {
		return nil, err
	}

	core := zapcore.NewCore(encoder, writer, level)
	if cfg.Sampling != nil && cfg.Sampling.Enabled {
		core = zapcore.NewSamplerWithOptions(core, time.Second, cfg.Sampling.Initial, cfg.Sampling.Thereafter)
	}
	return core, nil
}

// buildEncoder 根据日志格式创建 zap encoder。
//
// JSON encoder 适合生产环境和日志平台采集；Console encoder 更适合本地开发阅读。
// 字段名、时间格式、级别格式等细节由 buildEncoderConfig 统一处理。
func buildEncoder(format Format, outputs []Output, cfg *EncoderConfig) (zapcore.Encoder, error) {
	encoderConfig, err := buildEncoderConfig(format, outputs, cfg)
	if err != nil {
		return nil, err
	}

	switch format {
	case FormatJSON:
		return zapcore.NewJSONEncoder(encoderConfig), nil
	case FormatConsole:
		return zapcore.NewConsoleEncoder(encoderConfig), nil
	default:
		return nil, fmt.Errorf("unsupported logger format %q", format)
	}
}

// buildEncoderConfig 构造 zapcore.EncoderConfig。
//
// 默认字段名保持简洁稳定：level、ts、msg、logger、caller、stacktrace。调用方
// 只有在接入既有日志规范或日志平台字段要求时，才需要通过 EncoderConfig 覆盖。
func buildEncoderConfig(format Format, outputs []Output, cfg *EncoderConfig) (zapcore.EncoderConfig, error) {
	encoderConfig := zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		TimeKey:        "ts",
		NameKey:        "logger",
		CallerKey:      "caller",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	if format == FormatConsole {
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		if !containsOutput(outputs, OutputFile) {
			encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		}
	}

	if cfg == nil {
		return encoderConfig, nil
	}

	if cfg.MessageKey != "" {
		encoderConfig.MessageKey = cfg.MessageKey
	}
	if cfg.LevelKey != "" {
		encoderConfig.LevelKey = cfg.LevelKey
	}
	if cfg.TimeKey != "" {
		encoderConfig.TimeKey = cfg.TimeKey
	}
	if cfg.NameKey != "" {
		encoderConfig.NameKey = cfg.NameKey
	}
	if cfg.CallerKey != "" {
		encoderConfig.CallerKey = cfg.CallerKey
	}
	encoderConfig.FunctionKey = cfg.FunctionKey
	if cfg.StacktraceKey != "" {
		encoderConfig.StacktraceKey = cfg.StacktraceKey
	}
	if cfg.LineEnding != "" {
		encoderConfig.LineEnding = cfg.LineEnding
	}

	if cfg.TimeEncoding != "" {
		encoder, err := parseTimeEncoder(cfg.TimeEncoding)
		if err != nil {
			return zapcore.EncoderConfig{}, err
		}
		encoderConfig.EncodeTime = encoder
	}
	if cfg.DurationEncoding != "" {
		encoder, err := parseDurationEncoder(cfg.DurationEncoding)
		if err != nil {
			return zapcore.EncoderConfig{}, err
		}
		encoderConfig.EncodeDuration = encoder
	}
	if cfg.LevelEncoding != "" {
		encoder, err := parseLevelEncoder(cfg.LevelEncoding)
		if err != nil {
			return zapcore.EncoderConfig{}, err
		}
		encoderConfig.EncodeLevel = encoder
	}

	return encoderConfig, nil
}

// buildWriteSyncer 根据输出目标构造 zapcore.WriteSyncer。
//
// stdout/stderr 使用 zapcore.Lock 包装，保证并发写入时不会交错；file 在配置 Rotation
// 时使用 lumberjack writer，否则按写入打开普通文件，避免长期持有文件描述符。
func buildWriteSyncer(cfg *Config) (zapcore.WriteSyncer, error) {
	outputs := cfg.outputsOrDefault()
	syncers := make([]zapcore.WriteSyncer, 0, len(outputs))
	for _, output := range outputs {
		switch output {
		case OutputStdout:
			syncers = append(syncers, zapcore.Lock(consoleWriteSyncer{File: os.Stdout}))
		case OutputStderr:
			syncers = append(syncers, zapcore.Lock(consoleWriteSyncer{File: os.Stderr}))
		case OutputFile:
			filename := strings.TrimSpace(cfg.File)
			if cfg.Rotation == nil {
				syncers = append(syncers, zapcore.Lock(fileWriteSyncer{filename: filename}))
			} else {
				syncers = append(syncers, zapcore.Lock(rotatingWriteSyncer{writer: newRotateWriter(filename, cfg.Rotation)}))
			}
		default:
			return nil, fmt.Errorf("unsupported logger output %q", output)
		}
	}
	if len(syncers) == 1 {
		return syncers[0], nil
	}
	return zapcore.Lock(zapcore.NewMultiWriteSyncer(syncers...)), nil
}

// consoleWriteSyncer 只忽略控制台设备不支持 fsync 的错误，保留文件输出的真实错误。
type consoleWriteSyncer struct {
	*os.File
}

func (w consoleWriteSyncer) Sync() error {
	err := w.File.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EBADF) {
		return nil
	}
	return err
}

func containsOutput(outputs []Output, want Output) bool {
	for _, output := range outputs {
		if output == want {
			return true
		}
	}
	return false
}

// fileWriteSyncer opens the target only for the duration of each write. This
// gives the non-rotating file mode a bounded file-descriptor lifetime because
// zap.Logger exposes Sync, but no Close hook for its underlying Core.
type fileWriteSyncer struct {
	filename string
}

func (w fileWriteSyncer) Write(p []byte) (int, error) {
	if err := os.MkdirAll(filepath.Dir(w.filename), 0o755); err != nil {
		return 0, fmt.Errorf("create logger directory: %w", err)
	}
	file, err := os.OpenFile(w.filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open logger file: %w", err)
	}
	defer file.Close()

	n, err := file.Write(p)
	if err != nil {
		return n, err
	}
	return n, file.Sync()
}

func (fileWriteSyncer) Sync() error { return nil }

type rotatingWriteSyncer struct {
	writer *lumberjack.Logger
}

func (w rotatingWriteSyncer) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func (w rotatingWriteSyncer) Sync() error {
	return w.writer.Close()
}

// newRotateWriter 根据文件路径和轮转配置创建 lumberjack.Logger。
//
// 该函数只创建 writer，不会主动写入日志文件。
func newRotateWriter(filename string, cfg *RotationConfig) *lumberjack.Logger {
	writer := &lumberjack.Logger{Filename: filename}
	if cfg == nil {
		return writer
	}
	writer.MaxSize = cfg.MaxSize
	writer.MaxAge = cfg.MaxAge
	writer.MaxBackups = cfg.MaxBackups
	writer.LocalTime = cfg.LocalTime
	writer.Compress = cfg.Compress
	return writer
}

// buildOptions 将 Config 映射为 zap.Option。
//
// 这里处理 caller、caller skip、stacktrace、开发模式、内部错误输出和初始字段等
// logger 行为选项。日志编码和写入目标不在这里处理，而是由 Core 负责。
func buildOptions(cfg *Config) ([]zap.Option, error) {
	options := make([]zap.Option, 0, 6)
	options = append(options, zap.ErrorOutput(buildErrorOutput(cfg.ErrorOutput)))

	if cfg.Development {
		options = append(options, zap.Development())
	}
	if cfg.AddCaller {
		options = append(options, zap.AddCaller())
	}
	// Logger exposes a wrapper around zap.Logger, so skip that wrapper in
	// addition to any caller levels requested by the application.
	options = append(options, zap.AddCallerSkip(cfg.CallerSkip+1))
	if stacktraceLevel := cfg.stacktraceLevelOrDefault(); stacktraceLevel != StacktraceLevelNone {
		level, err := parseStacktraceLevel(stacktraceLevel)
		if err != nil {
			return nil, err
		}
		options = append(options, zap.AddStacktrace(level))
	}
	if len(cfg.InitialFields) > 0 {
		options = append(options, zap.Fields(buildInitialFields(cfg.InitialFields)...))
	}

	return options, nil
}

// buildErrorOutput 构造 zap 内部错误输出 writer。
//
// 这个 writer 只用于 zap 自身的错误，例如写日志失败或 Sync 失败，不是业务日志输出。
// 为空时默认写 stderr；如果传入普通文件路径，会使用 lumberjack writer 写入该文件。
func buildErrorOutput(output string) zapcore.WriteSyncer {
	output = strings.TrimSpace(output)
	switch output {
	case "", string(OutputStderr):
		return zapcore.Lock(consoleWriteSyncer{File: os.Stderr})
	case string(OutputStdout):
		return zapcore.Lock(consoleWriteSyncer{File: os.Stdout})
	default:
		return zapcore.AddSync(&lumberjack.Logger{Filename: output})
	}
}

// buildInitialFields 将配置文件中的固定字段转换为 zap.Field。
//
// 这些字段会出现在每一条日志中，适合放 service、env、version、region 等稳定标签。
func buildInitialFields(fields map[string]string) []zap.Field {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	zapFields := make([]zap.Field, 0, len(keys))
	for _, key := range keys {
		value := fields[key]
		zapFields = append(zapFields, zap.String(key, value))
	}
	return zapFields
}

// parseZapLevel 将配置级别转换为 zapcore.Level。
func parseZapLevel(level Level) (zapcore.Level, error) {
	switch level {
	case LevelDebug:
		return zapcore.DebugLevel, nil
	case LevelInfo:
		return zapcore.InfoLevel, nil
	case LevelWarn:
		return zapcore.WarnLevel, nil
	case LevelError:
		return zapcore.ErrorLevel, nil
	case LevelDPanic:
		return zapcore.DPanicLevel, nil
	case LevelPanic:
		return zapcore.PanicLevel, nil
	case LevelFatal:
		return zapcore.FatalLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("unsupported logger level %q", level)
	}
}

// parseStacktraceLevel 将配置中的 stacktrace 起始级别转换为 zapcore.Level。
//
// StacktraceLevelNone 不会进入该函数；调用方会在 buildOptions 中直接跳过
// zap.AddStacktrace。
func parseStacktraceLevel(level StacktraceLevel) (zapcore.Level, error) {
	switch level {
	case StacktraceLevelError:
		return zapcore.ErrorLevel, nil
	case StacktraceLevelPanic:
		return zapcore.PanicLevel, nil
	case StacktraceLevelFatal:
		return zapcore.FatalLevel, nil
	default:
		return zapcore.ErrorLevel, fmt.Errorf("unsupported logger stacktrace_level %q", level)
	}
}

// parseTimeEncoder 将配置中的时间编码方式转换为 zap 时间编码器。
func parseTimeEncoder(encoding string) (zapcore.TimeEncoder, error) {
	switch encoding {
	case "iso8601":
		return zapcore.ISO8601TimeEncoder, nil
	case "millis":
		return zapcore.EpochMillisTimeEncoder, nil
	case "nanos":
		return zapcore.EpochNanosTimeEncoder, nil
	case "epoch":
		return zapcore.EpochTimeEncoder, nil
	default:
		return nil, fmt.Errorf("unsupported logger time_encoding %q", encoding)
	}
}

// parseDurationEncoder 将配置中的 duration 编码方式转换为 zap duration 编码器。
func parseDurationEncoder(encoding string) (zapcore.DurationEncoder, error) {
	switch encoding {
	case "seconds":
		return zapcore.SecondsDurationEncoder, nil
	case "millis":
		return zapcore.MillisDurationEncoder, nil
	case "nanos":
		return zapcore.NanosDurationEncoder, nil
	case "string":
		return zapcore.StringDurationEncoder, nil
	default:
		return nil, fmt.Errorf("unsupported logger duration_encoding %q", encoding)
	}
}

// parseLevelEncoder 将配置中的级别编码方式转换为 zap level 编码器。
func parseLevelEncoder(encoding string) (zapcore.LevelEncoder, error) {
	switch encoding {
	case "lowercase":
		return zapcore.LowercaseLevelEncoder, nil
	case "capital":
		return zapcore.CapitalLevelEncoder, nil
	case "color":
		return zapcore.CapitalColorLevelEncoder, nil
	default:
		return nil, fmt.Errorf("unsupported logger level_encoding %q", encoding)
	}
}
