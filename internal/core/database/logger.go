package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils"
)

// buildLogger 构造基于 slog 的 GORM logger。
//
// log 为 nil 时使用丢弃日志的 slog logger，避免未初始化日志系统时产生隐式输出或 panic。
func buildLogger(cfg *LoggerConfig, log *slog.Logger) logger.Interface {
	logConfig := logger.Config{
		SlowThreshold:             cfg.SlowThreshold,
		Colorful:                  cfg.Colorful,
		IgnoreRecordNotFoundError: cfg.IgnoreRecordNotFoundError,
		ParameterizedQueries:      cfg.ParameterizedQueries,
		LogLevel:                  toGORMLogLevel(cfg.Level),
	}
	if logConfig.SlowThreshold == 0 {
		logConfig.SlowThreshold = 200 * time.Millisecond
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return slogGORMLogger{
		logger: log,
		config: logConfig,
		logSQL: cfg.LogSQL,
	}
}

// slogGORMLogger 使用 slog 承接 GORM logger.Interface。
//
// LogSQL=false 时普通 SQL Trace 不输出；错误和慢查询仍会输出耗时、影响行数和错误信息，
// 但 SQL 文本会被固定隐藏，避免敏感参数或完整 SQL 进入日志。
type slogGORMLogger struct {
	logger *slog.Logger
	config logger.Config
	logSQL bool
}

func (l slogGORMLogger) LogMode(level logger.LogLevel) logger.Interface {
	config := l.config
	config.LogLevel = level
	return slogGORMLogger{
		logger: l.logger,
		config: config,
		logSQL: l.logSQL,
	}
}

func (l slogGORMLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.config.LogLevel < logger.Info {
		return
	}
	l.withContext(ctx).LogAttrs(ctx, slog.LevelInfo, fmt.Sprintf(msg, data...), slog.String("source", utils.FileWithLineNum()))
}

func (l slogGORMLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.config.LogLevel < logger.Warn {
		return
	}
	l.withContext(ctx).LogAttrs(ctx, slog.LevelWarn, fmt.Sprintf(msg, data...), slog.String("source", utils.FileWithLineNum()))
}

func (l slogGORMLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.config.LogLevel < logger.Error {
		return
	}
	l.withContext(ctx).LogAttrs(ctx, slog.LevelError, fmt.Sprintf(msg, data...), slog.String("source", utils.FileWithLineNum()))
}

func (l slogGORMLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.config.LogLevel <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)
	elapsedMS := float64(elapsed.Nanoseconds()) / 1e6

	switch {
	case err != nil && l.config.LogLevel >= logger.Error && (!errors.Is(err, logger.ErrRecordNotFound) || !l.config.IgnoreRecordNotFoundError):
		sql, rows := l.sqlAndRows(fc)
		l.withContext(ctx).LogAttrs(ctx, slog.LevelError, "gorm sql error", l.traceFields(elapsed, elapsedMS, rows, sql, slog.Any("error", err))...)
	case l.config.SlowThreshold != 0 && elapsed > l.config.SlowThreshold && l.config.LogLevel >= logger.Warn:
		sql, rows := l.sqlAndRows(fc)
		l.withContext(ctx).LogAttrs(ctx, slog.LevelWarn, "gorm slow sql", l.traceFields(elapsed, elapsedMS, rows, sql, slog.Duration("slow_threshold", l.config.SlowThreshold))...)
	case l.logSQL && l.config.LogLevel == logger.Info:
		sql, rows := l.sqlAndRows(fc)
		l.withContext(ctx).LogAttrs(ctx, slog.LevelInfo, "gorm sql", l.traceFields(elapsed, elapsedMS, rows, sql)...)
	}
}

func (l slogGORMLogger) ParamsFilter(_ context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if l.config.ParameterizedQueries {
		return sql, nil
	}
	return sql, params
}

func (l slogGORMLogger) withContext(_ context.Context) *slog.Logger {
	if l.logger == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return l.logger
}

func (l slogGORMLogger) sqlAndRows(fc func() (string, int64)) (string, int64) {
	sql, rows := fc()
	if !l.logSQL {
		sql = "[SQL hidden]"
	}
	return sql, rows
}

func (l slogGORMLogger) traceFields(elapsed time.Duration, elapsedMS float64, rows int64, sql string, extra ...slog.Attr) []slog.Attr {
	fields := []slog.Attr{
		slog.String("source", utils.FileWithLineNum()),
		slog.Duration("elapsed", elapsed),
		slog.Float64("elapsed_ms", elapsedMS),
		slog.Int64("rows", rows),
		slog.String("sql", sql),
	}
	return append(fields, extra...)
}

func toGORMLogLevel(level LogLevel) logger.LogLevel {
	switch level {
	case LogLevelSilent:
		return logger.Silent
	case LogLevelError:
		return logger.Error
	case LogLevelInfo:
		return logger.Info
	case LogLevelWarn, "":
		return logger.Warn
	default:
		return logger.Warn
	}
}
