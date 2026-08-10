package database

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB 封装 GORM 数据库连接
type DB struct {
	Orm       *gorm.DB
	logger    *zap.Logger
	userCache UserCache // 用户映射缓存（可 nil：不缓存直查数据库）
}

// SetUserCache 注入用户映射缓存（Redis 实现见 infra 包）。
// 高频消息场景防数据库击穿；nil 或重复调用时覆盖生效。
func (db *DB) SetUserCache(c UserCache) { db.userCache = c }

// Connect 创建 GORM 连接并验证连通性
func Connect(ctx context.Context, connString string, logger *zap.Logger) (*DB, error) {
	// gorm 默认 logger 的 IgnoreRecordNotFoundError=false，会把"记录不存在"
	// 当作错误打印（如插件 KV 首次读写、用户首条消息等正常业务分支），
	// 这里显式开启忽略，仅保留慢 SQL 与真实错误日志。
	orm, err := gorm.Open(postgres.Open(connString), &gorm.Config{
		Logger: gormlogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	sqlDB, err := orm.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &DB{Orm: orm, logger: logger}, nil
}

// Close 关闭连接
func (db *DB) Close() {
	sqlDB, err := db.Orm.DB()
	if err != nil {
		return
	}
	sqlDB.Close()
}
