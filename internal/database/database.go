package database

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB 封装 GORM 数据库连接
type DB struct {
	Orm    *gorm.DB
	logger *zap.Logger
}

// Connect 创建 GORM 连接并验证连通性
func Connect(ctx context.Context, connString string, logger *zap.Logger) (*DB, error) {
	orm, err := gorm.Open(postgres.Open(connString), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
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
