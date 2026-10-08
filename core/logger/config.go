package logger

import (
	"errors"
	"fmt"
	"strings"
)

// Level 表示日志级别。
type Level string

const (
	// LevelDebug 表示调试日志，通常只在本地开发、测试或临时排障时开启。
	LevelDebug Level = "debug"

	// LevelInfo 表示普通业务日志，是生产环境常用默认级别。
	LevelInfo Level = "info"

	// LevelWarn 表示需要关注但不一定导致请求失败的异常情况。
	LevelWarn Level = "warn"

	// LevelError 表示明确的错误日志，通常需要排查或告警。
	LevelError Level = "error"

	// LevelDPanic 表示开发环境下触发 panic、生产环境下按 error 输出的日志级别。
	LevelDPanic Level = "dpanic"

	// LevelPanic 表示记录日志后 panic。
	LevelPanic Level = "panic"

	// LevelFatal 表示记录日志后退出进程。
	LevelFatal Level = "fatal"
)

// Format 表示日志编码格式。
type Format string

const (
	// FormatJSON 表示 JSON 结构化日志，生产环境推荐使用。
	FormatJSON Format = "json"

	// FormatConsole 表示更适合人眼阅读的控制台日志，通常用于本地开发。
	FormatConsole Format = "console"
)

// Output 表示日志输出目标。
type Output string

const (
	// OutputStdout 表示输出到标准输出，容器和云原生日志采集场景常用。
	OutputStdout Output = "stdout"

	// OutputStderr 表示输出到标准错误，通常用于错误流或本地调试。
	OutputStderr Output = "stderr"

	// OutputFile 表示输出到文件。启用后通常应同时配置 Rotation。
	OutputFile Output = "file"
)

// StacktraceLevel 表示从哪个日志级别开始记录 stacktrace。
type StacktraceLevel string

const (
	// StacktraceLevelNone 表示不自动记录 stacktrace。
	StacktraceLevelNone StacktraceLevel = "none"

	// StacktraceLevelError 表示 error 及以上级别记录 stacktrace，生产环境常用。
	StacktraceLevelError StacktraceLevel = "error"

	// StacktraceLevelPanic 表示 panic 及以上级别记录 stacktrace。
	StacktraceLevelPanic StacktraceLevel = "panic"

	// StacktraceLevelFatal 表示 fatal 级别记录 stacktrace。
	StacktraceLevelFatal StacktraceLevel = "fatal"
)

// Config 表示基于 zap 的日志配置。
//
// 该结构体面向“应用配置文件”设计，只保留字符串、数字、布尔值和 map 等
// 可序列化字段，不包含 zapcore.Core、zap.Option、io.Writer 等运行时代码对象。
//
// 常规项目通常只需要配置 Level、Format、Outputs、File、AddCaller 和 Rotation。
// Encoder、Sampling 等字段属于按需配置：需要调整字段名或降低重复日志量时再使用。
//
// 配置组字段使用指针是为了区分“整组未配置”和“整组参与覆盖”。例如 Rotation 为 nil
// 表示文件输出不启用轮转；Rotation 非 nil 时，组内字段会按日志轮转实现的语义传递，
// 字段零值通常表示使用实现层默认值。
type Config struct {
	// Level 指定日志级别。常用字段。
	// 为空时建议由 New 使用 LevelInfo。
	Level Level `json:"level" yaml:"level" mapstructure:"level"`

	// Format 指定日志编码格式。常用字段。
	// 为空时建议由 New 使用 FormatJSON；本地开发可配置为 FormatConsole。
	Format Format `json:"format" yaml:"format" mapstructure:"format"`

	// Output 保留用于兼容单输出配置，不能与 Outputs 同时设置。
	// Output 和 Outputs 均为空时默认使用 stdout。
	Output Output `json:"output" yaml:"output" mapstructure:"output"`

	// Outputs 可同时指定 stdout、stderr 和 file；为空时回退到 Output。
	Outputs []Output `json:"outputs" yaml:"outputs" mapstructure:"outputs"`

	// File 是日志文件路径，在 Outputs 包含 file 或 Output 为 file 时使用。
	// Rotation 为空时写入单个文件，不自动轮转；生产环境应配置 Rotation，避免单个日志文件无限增长。
	File string `json:"file" yaml:"file" mapstructure:"file"`

	// ErrorOutput 指定 zap 内部错误输出目标，例如 encoder 写入失败、Sync 失败等。
	// 为空时建议由 New 使用 stderr；支持 stdout、stderr 或文件路径。
	ErrorOutput string `json:"error_output" yaml:"error_output" mapstructure:"error_output"`

	// Development 表示是否启用开发模式。
	// 开发模式通常会使用更易读的输出和更激进的 DPanic 行为，生产环境建议关闭。
	Development bool `json:"development" yaml:"development" mapstructure:"development"`

	// AddCaller 表示是否记录调用方文件和行号。
	// 生产环境建议开启，便于定位日志来源；极致性能场景可关闭。
	AddCaller bool `json:"add_caller" yaml:"add_caller" mapstructure:"add_caller"`

	// CallerSkip 为调用方层级增加额外跳过层数。
	// 如果项目在 zap 外再封装一层 logger 方法，通常需要设置为 1 或由 New 内部固定处理。
	CallerSkip int `json:"caller_skip" yaml:"caller_skip" mapstructure:"caller_skip"`

	// StacktraceLevel 指定从哪个级别开始记录 stacktrace。
	// 为空时建议生产环境使用 error，本地开发可按需调整。
	StacktraceLevel StacktraceLevel `json:"stacktrace_level" yaml:"stacktrace_level" mapstructure:"stacktrace_level"`

	// InitialFields 是每条日志都会携带的固定字段，例如 service、env、version、region。
	// 字段值保持为字符串，便于配置文件表达和跨日志后端检索。
	InitialFields map[string]string `json:"initial_fields" yaml:"initial_fields" mapstructure:"initial_fields"`

	// Encoder 是 zap encoder 字段名和时间格式配置。按需配置组。
	// nil 表示使用本包推荐的默认字段名。
	Encoder *EncoderConfig `json:"encoder" yaml:"encoder" mapstructure:"encoder"`

	// Rotation 是文件日志轮转配置。按需配置组。
	// 仅 Outputs 包含 file 或 Output 为 file 时生效；nil 表示不启用文件轮转。
	Rotation *RotationConfig `json:"rotation" yaml:"rotation" mapstructure:"rotation"`

	// Sampling 是 zap 日志采样配置。按需配置组。
	// nil 表示不启用采样；高频重复日志较多时建议开启。
	Sampling *SamplingConfig `json:"sampling" yaml:"sampling" mapstructure:"sampling"`
}

// Validate 校验日志配置是否满足创建 zap logger 的基本要求。
//
// 该方法只做确定性的静态配置校验，不会创建文件、打开 writer，也不会初始化
// tracer、meter 或 Prometheus exporter。
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("logger config is nil")
	}

	var errs []error
	level := c.levelOrDefault()
	format := c.formatOrDefault()
	outputs := c.outputsOrDefault()
	stacktraceLevel := c.stacktraceLevelOrDefault()

	if !isValidLevel(level) {
		errs = append(errs, fmt.Errorf("logger level must be one of %q, %q, %q, %q, %q, %q, %q", LevelDebug, LevelInfo, LevelWarn, LevelError, LevelDPanic, LevelPanic, LevelFatal))
	}
	if !isValidFormat(format) {
		errs = append(errs, fmt.Errorf("logger format must be one of %q, %q", FormatJSON, FormatConsole))
	}
	if c.Output != "" && len(c.Outputs) > 0 {
		errs = append(errs, errors.New("logger output and outputs cannot be set together"))
	}
	hasFile := false
	seen := make(map[Output]bool, len(outputs))
	for _, output := range outputs {
		if seen[output] {
			errs = append(errs, fmt.Errorf("logger outputs contains duplicate %q", output))
		}
		seen[output] = true
		if !isValidOutput(output) {
			errs = append(errs, fmt.Errorf("logger output must be one of %q, %q, %q", OutputStdout, OutputStderr, OutputFile))
		}
		if output == OutputFile {
			hasFile = true
		}
	}
	if hasFile && strings.TrimSpace(c.File) == "" {
		errs = append(errs, errors.New("logger file must not be empty when output is file"))
	}
	if !hasFile && strings.TrimSpace(c.File) != "" {
		errs = append(errs, errors.New("logger file requires output to be file"))
	}
	if c.ErrorOutput != "" && strings.TrimSpace(c.ErrorOutput) == "" {
		errs = append(errs, errors.New("logger error_output must not be blank"))
	}
	if c.CallerSkip < 0 {
		errs = append(errs, errors.New("logger caller_skip must be greater than or equal to 0"))
	}
	if !isValidStacktraceLevel(stacktraceLevel) {
		errs = append(errs, fmt.Errorf("logger stacktrace_level must be one of %q, %q, %q, %q", StacktraceLevelNone, StacktraceLevelError, StacktraceLevelPanic, StacktraceLevelFatal))
	}
	for key := range c.InitialFields {
		if strings.TrimSpace(key) == "" {
			errs = append(errs, errors.New("logger initial_fields key must not be empty"))
			break
		}
		if isTraceKey(key) {
			errs = append(errs, fmt.Errorf("logger initial_fields key %q is reserved for context trace fields", key))
		}
	}
	if c.Encoder != nil {
		errs = append(errs, c.Encoder.validate()...)
	}
	if c.Rotation != nil {
		errs = append(errs, c.Rotation.validate(hasFile)...)
	}
	if c.Sampling != nil {
		errs = append(errs, c.Sampling.validate()...)
	}
	return errors.Join(errs...)
}

func (c *Config) levelOrDefault() Level {
	if c.Level == "" {
		return LevelInfo
	}
	return c.Level
}

func (c *Config) formatOrDefault() Format {
	if c.Format == "" {
		return FormatJSON
	}
	return c.Format
}

func (c *Config) outputsOrDefault() []Output {
	if len(c.Outputs) > 0 {
		return append([]Output(nil), c.Outputs...)
	}
	if c.Output != "" {
		return []Output{c.Output}
	}
	return []Output{OutputStdout}
}

func (c *Config) stacktraceLevelOrDefault() StacktraceLevel {
	if c.StacktraceLevel == "" {
		return StacktraceLevelError
	}
	return c.StacktraceLevel
}

func isValidLevel(level Level) bool {
	switch level {
	case LevelDebug, LevelInfo, LevelWarn, LevelError, LevelDPanic, LevelPanic, LevelFatal:
		return true
	default:
		return false
	}
}

func isValidFormat(format Format) bool {
	switch format {
	case FormatJSON, FormatConsole:
		return true
	default:
		return false
	}
}

func isValidOutput(output Output) bool {
	switch output {
	case OutputStdout, OutputStderr, OutputFile:
		return true
	default:
		return false
	}
}

func isValidStacktraceLevel(level StacktraceLevel) bool {
	switch level {
	case StacktraceLevelNone, StacktraceLevelError, StacktraceLevelPanic, StacktraceLevelFatal:
		return true
	default:
		return false
	}
}

// EncoderConfig 表示 zap encoder 中适合配置文件表达的字段。
//
// 大多数项目不需要调整这组配置。只有在接入现有日志规范、日志平台字段约定、
// 或需要兼容旧系统字段名时再显式配置。
type EncoderConfig struct {
	// MessageKey 是日志消息字段名。默认建议使用 msg。
	MessageKey string `json:"message_key" yaml:"message_key" mapstructure:"message_key"`

	// LevelKey 是日志级别字段名。默认建议使用 level。
	LevelKey string `json:"level_key" yaml:"level_key" mapstructure:"level_key"`

	// TimeKey 是日志时间字段名。默认建议使用 ts。
	TimeKey string `json:"time_key" yaml:"time_key" mapstructure:"time_key"`

	// NameKey 是 logger 名称字段名。默认建议使用 logger。
	NameKey string `json:"name_key" yaml:"name_key" mapstructure:"name_key"`

	// CallerKey 是调用方字段名。默认建议使用 caller。
	CallerKey string `json:"caller_key" yaml:"caller_key" mapstructure:"caller_key"`

	// FunctionKey 是函数名字段名。
	// 为空表示不记录函数名；记录函数名会有额外开销。
	FunctionKey string `json:"function_key" yaml:"function_key" mapstructure:"function_key"`

	// StacktraceKey 是 stacktrace 字段名。默认建议使用 stacktrace。
	StacktraceKey string `json:"stacktrace_key" yaml:"stacktrace_key" mapstructure:"stacktrace_key"`

	// LineEnding 是每条日志结尾。为空时建议使用 zap 默认换行。
	LineEnding string `json:"line_ending" yaml:"line_ending" mapstructure:"line_ending"`

	// TimeEncoding 指定时间编码方式，支持 iso8601、millis、nanos、epoch。
	// 为空时建议使用 iso8601，便于人眼阅读和跨系统检索。
	TimeEncoding string `json:"time_encoding" yaml:"time_encoding" mapstructure:"time_encoding"`

	// DurationEncoding 指定 duration 编码方式，支持 seconds、millis、nanos、string。
	// 为空时建议使用 seconds，与 zap 生产配置接近。
	DurationEncoding string `json:"duration_encoding" yaml:"duration_encoding" mapstructure:"duration_encoding"`

	// LevelEncoding 指定级别编码方式，支持 lowercase、capital、color。
	// JSON 生产日志建议 lowercase；console 开发日志可使用 color。
	LevelEncoding string `json:"level_encoding" yaml:"level_encoding" mapstructure:"level_encoding"`
}

func (c *EncoderConfig) validate() []error {
	var errs []error
	if c.TimeEncoding != "" && !isValidTimeEncoding(c.TimeEncoding) {
		errs = append(errs, errors.New("logger encoder.time_encoding must be one of iso8601, millis, nanos, epoch"))
	}
	if c.DurationEncoding != "" && !isValidDurationEncoding(c.DurationEncoding) {
		errs = append(errs, errors.New("logger encoder.duration_encoding must be one of seconds, millis, nanos, string"))
	}
	if c.LevelEncoding != "" && !isValidLevelEncoding(c.LevelEncoding) {
		errs = append(errs, errors.New("logger encoder.level_encoding must be one of lowercase, capital, color"))
	}
	return errs
}

func isValidTimeEncoding(encoding string) bool {
	switch encoding {
	case "iso8601", "millis", "nanos", "epoch":
		return true
	default:
		return false
	}
}

func isValidDurationEncoding(encoding string) bool {
	switch encoding {
	case "seconds", "millis", "nanos", "string":
		return true
	default:
		return false
	}
}

func isValidLevelEncoding(encoding string) bool {
	switch encoding {
	case "lowercase", "capital", "color":
		return true
	default:
		return false
	}
}

// RotationConfig 表示文件日志轮转配置。
//
// 当前实现使用 lumberjack 承接这组字段。该配置只在 Output 为 file 时生效；
// 容器部署如果统一采集 stdout，通常不需要启用文件轮转。
type RotationConfig struct {
	// MaxSize 是单个日志文件最大大小，单位 MB。
	// 0 表示使用轮转库默认值；生产环境建议显式设置，例如 100。
	MaxSize int `json:"max_size" yaml:"max_size" mapstructure:"max_size"`

	// MaxAge 是旧日志文件保留天数。
	// 0 表示不按天数清理。
	MaxAge int `json:"max_age" yaml:"max_age" mapstructure:"max_age"`

	// MaxBackups 是最多保留的旧日志文件数量。
	// 0 表示不按数量清理。
	MaxBackups int `json:"max_backups" yaml:"max_backups" mapstructure:"max_backups"`

	// LocalTime 表示轮转文件名是否使用本地时间。
	// false 通常表示使用 UTC 时间。
	LocalTime bool `json:"local_time" yaml:"local_time" mapstructure:"local_time"`

	// Compress 表示是否压缩旧日志文件。
	// 生产环境磁盘空间敏感时建议开启。
	Compress bool `json:"compress" yaml:"compress" mapstructure:"compress"`
}

func (c *RotationConfig) validate(hasFile bool) []error {
	var errs []error
	if !hasFile {
		errs = append(errs, errors.New("logger rotation requires output to be file"))
	}
	if c.MaxSize < 0 {
		errs = append(errs, errors.New("logger rotation.max_size must be greater than or equal to 0"))
	}
	if c.MaxAge < 0 {
		errs = append(errs, errors.New("logger rotation.max_age must be greater than or equal to 0"))
	}
	if c.MaxBackups < 0 {
		errs = append(errs, errors.New("logger rotation.max_backups must be greater than or equal to 0"))
	}
	return errs
}

// SamplingConfig 表示 zap 日志采样配置。
//
// 采样用于降低相同日志在短时间内大量重复输出造成的 IO 和存储压力。它不适合
// 安全审计日志、交易流水日志等必须完整保留的场景。
type SamplingConfig struct {
	// Enabled 表示是否启用采样。
	// false 时保留结构但不启用采样，便于不同环境覆盖配置。
	Enabled bool `json:"enabled" yaml:"enabled" mapstructure:"enabled"`

	// Initial 表示每秒内同一日志允许完整输出的初始条数。
	// Enabled 为 true 时必须大于 0。
	Initial int `json:"initial" yaml:"initial" mapstructure:"initial"`

	// Thereafter 表示超过 Initial 后，每隔多少条再输出一条。
	// Enabled 为 true 时必须大于 0。
	Thereafter int `json:"thereafter" yaml:"thereafter" mapstructure:"thereafter"`
}

func (c *SamplingConfig) validate() []error {
	var errs []error
	if c.Initial < 0 {
		errs = append(errs, errors.New("logger sampling.initial must be greater than or equal to 0"))
	}
	if c.Thereafter < 0 {
		errs = append(errs, errors.New("logger sampling.thereafter must be greater than or equal to 0"))
	}
	if c.Enabled && c.Initial == 0 {
		errs = append(errs, errors.New("logger sampling.initial must be greater than 0 when sampling is enabled"))
	}
	if c.Enabled && c.Thereafter == 0 {
		errs = append(errs, errors.New("logger sampling.thereafter must be greater than 0 when sampling is enabled"))
	}
	return errs
}
