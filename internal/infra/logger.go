package infra

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/DaWesen/lanmei-dream/internal/config"
)

// InitLogger 创建 zap Logger：JSON 编码、ISO8601 时间、带调用点与 error 级堆栈。
// 级别按 cfg.Level 取 debug/warn/error，未知值回退 info；
// cfg.Persistent 且 cfg.Path 非空时写入 lumberjack 轮转文件，否则写 stdout。
func InitLogger(cfg *config.LogConfig) *zap.Logger {
	level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	switch cfg.Level {
	case "debug":
		level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	case "warn":
		level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	case "error":
		level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder

	var writeSyncer zapcore.WriteSyncer
	if cfg.Persistent && cfg.Path != "" {
		writeSyncer = zapcore.AddSync(&lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    cfg.MaxSize,
			MaxAge:     cfg.MaxAge,
			MaxBackups: cfg.MaxBackups,
			Compress:   cfg.Compression,
		})
	} else {
		writeSyncer = zapcore.AddSync(os.Stdout)
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		writeSyncer,
		level,
	)

	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
}
