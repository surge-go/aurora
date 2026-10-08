package logger

import (
	"strings"
	"testing"
)

func TestConfigValidateDefaults(t *testing.T) {
	cfg := &Config{}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := &Config{
		Level:           LevelInfo,
		Format:          FormatJSON,
		Output:          OutputFile,
		File:            "/var/log/qi/app.log",
		ErrorOutput:     "stderr",
		AddCaller:       true,
		StacktraceLevel: StacktraceLevelError,
		InitialFields: map[string]string{
			"service": "qi",
			"env":     "prod",
		},
		Encoder: &EncoderConfig{
			MessageKey:       "msg",
			LevelKey:         "level",
			TimeKey:          "ts",
			CallerKey:        "caller",
			StacktraceKey:    "stacktrace",
			TimeEncoding:     "iso8601",
			DurationEncoding: "seconds",
			LevelEncoding:    "lowercase",
		},
		Rotation: &RotationConfig{
			MaxSize:    100,
			MaxAge:     30,
			MaxBackups: 10,
			LocalTime:  true,
			Compress:   true,
		},
		Sampling: &SamplingConfig{
			Enabled:    true,
			Initial:    100,
			Thereafter: 100,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidateInvalidConfig(t *testing.T) {
	cfg := &Config{
		Level:           Level("trace"),
		Format:          Format("text"),
		Output:          Output("network"),
		File:            "/var/log/qi/app.log",
		ErrorOutput:     " ",
		CallerSkip:      -1,
		StacktraceLevel: StacktraceLevel("warn"),
		InitialFields: map[string]string{
			"": "empty",
		},
		Encoder: &EncoderConfig{
			TimeEncoding:     "rfc3339",
			DurationEncoding: "duration",
			LevelEncoding:    "upper",
		},
		Rotation: &RotationConfig{
			MaxSize:    -1,
			MaxAge:     -1,
			MaxBackups: -1,
		},
		Sampling: &SamplingConfig{
			Enabled:    true,
			Initial:    0,
			Thereafter: 0,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}

	wantContains := []string{
		"logger level must be one of",
		"logger format must be one of",
		"logger output must be one of",
		"logger file requires output to be file",
		"logger error_output must not be blank",
		"logger caller_skip must be greater than or equal to 0",
		"logger stacktrace_level must be one of",
		"logger initial_fields key must not be empty",
		"logger encoder.time_encoding must be one of",
		"logger encoder.duration_encoding must be one of",
		"logger encoder.level_encoding must be one of",
		"logger rotation requires output to be file",
		"logger rotation.max_size must be greater than or equal to 0",
		"logger rotation.max_age must be greater than or equal to 0",
		"logger rotation.max_backups must be greater than or equal to 0",
		"logger sampling.initial must be greater than 0 when sampling is enabled",
		"logger sampling.thereafter must be greater than 0 when sampling is enabled",
	}
	for _, want := range wantContains {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want to contain %q", err, want)
		}
	}
}

func TestConfigValidateFileOutputRequiresFile(t *testing.T) {
	cfg := &Config{
		Output: OutputFile,
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "logger file must not be empty when output is file") {
		t.Fatalf("Validate() error = %v, want file required error", err)
	}
}

func TestConfigValidatesMultipleOutputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   Config
		valid bool
	}{
		{"console and file", Config{Outputs: []Output{OutputStdout, OutputFile}, File: "runtime/logs/qi.log"}, true},
		{"rotation", Config{Outputs: []Output{OutputStdout, OutputFile}, File: "qi.log", Rotation: &RotationConfig{MaxSize: 10}}, true},
		{"duplicate", Config{Outputs: []Output{OutputStdout, OutputStdout}}, false},
		{"unknown", Config{Outputs: []Output{OutputStdout, "network"}}, false},
		{"ambiguous", Config{Output: OutputStdout, Outputs: []Output{OutputFile}, File: "qi.log"}, false},
		{"missing path", Config{Outputs: []Output{OutputStdout, OutputFile}}, false},
		{"unused path", Config{Outputs: []Output{OutputStdout}, File: "qi.log"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate()=%v, valid=%t", err, tc.valid)
			}
		})
	}
}

func TestConfigRejectsReservedTraceInitialFields(t *testing.T) {
	for _, key := range []string{"trace_id", "span_id", "trace_sampled"} {
		t.Run(key, func(t *testing.T) {
			cfg := &Config{InitialFields: map[string]string{key: "configured"}}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved for context trace fields") {
				t.Fatalf("Validate() error = %v, want reserved trace field error", err)
			}
		})
	}
}
