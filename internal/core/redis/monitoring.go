package redis

import (
	"fmt"
	"sync"

	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
)

func instrumentClient(client goredis.UniversalClient, cfg *MonitoringConfig) (func(), error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.TracingEnabled {
		options := []redisotel.TracingOption{
			redisotel.WithDBStatement(cfg.DBStatementEnabled),
			redisotel.WithCallerEnabled(cfg.CallerEnabled),
			redisotel.WithDialFilter(!cfg.DialEnabled),
		}
		if cfg.TracerProvider != nil {
			options = append(options, redisotel.WithTracerProvider(cfg.TracerProvider))
		}
		if err := redisotel.InstrumentTracing(client, options...); err != nil {
			return nil, fmt.Errorf("redis register tracing instrumentation: %w", err)
		}
	}
	if !cfg.MetricsEnabled {
		return nil, nil
	}
	closeMetrics := make(chan struct{})
	var closeOnce sync.Once
	release := func() { closeOnce.Do(func() { close(closeMetrics) }) }
	options := []redisotel.MetricsOption{redisotel.WithCloseChan(closeMetrics)}
	if cfg.MeterProvider != nil {
		options = append(options, redisotel.WithMeterProvider(cfg.MeterProvider))
	}
	if err := redisotel.InstrumentMetrics(client, options...); err != nil {
		return release, fmt.Errorf("redis register metrics instrumentation: %w", err)
	}
	return release, nil
}
