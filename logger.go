package hxdna

import (
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	globalLogger *zap.SugaredLogger
	logMu        sync.RWMutex
	logInitOnce  sync.Once
)

// SetupLogger initialises the global logger. Call once at startup before Serve,
// or let Serve call it automatically via ServeConfig.
// mode "production" → JSON output; anything else → colored human-readable.
// level accepts: debug, info, warn, error.
func SetupLogger(service, version, level, mode string) error {
	var cfg zap.Config
	switch mode {
	case "production":
		cfg = zap.NewProductionConfig()
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoder(func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(t.UTC().Format("2006-01-02T15:04:05Z0700"))
		})
	default:
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoder(func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(t.Format(time.Stamp))
		})
	}
	cfg.Level = zap.NewAtomicLevelAt(parseLogLevel(level))
	z, err := cfg.Build(zap.WithCaller(false))
	if err != nil {
		return err
	}
	z = z.With(zap.String("service", service), zap.String("version", version))
	zap.ReplaceGlobals(z)

	logMu.Lock()
	globalLogger = z.Sugar()
	logMu.Unlock()

	return nil
}

// L returns the global sugared logger, initialising a default development logger if
// SetupLogger has not been called yet.
func L() *zap.SugaredLogger {
	logInitOnce.Do(func() {
		logMu.RLock()
		uninitialised := globalLogger == nil
		logMu.RUnlock()
		if uninitialised {
			_ = SetupLogger("worker", "dev", "info", "development")
		}
	})
	logMu.RLock()
	defer logMu.RUnlock()
	return globalLogger
}

func parseLogLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
