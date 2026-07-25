package config

import (
	"strconv"
	"strings"
)

// Config 应用总配置，按模块拆分为子结构体
type Config struct {
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	AI       AIConfig       `mapstructure:"ai"`
	Bot      BotConfig      `mapstructure:"bot"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	URL string `mapstructure:"url"`
}

// RedisConfig Redis 配置
type RedisConfig struct {
	Addr string `mapstructure:"addr"`
}

// AIConfig AI 相关配置
type AIConfig struct {
	EmbeddingDim int `mapstructure:"embedding_dim"`
}

// BotConfig 机器人配置
type BotConfig struct {
	WebSocketURL string `mapstructure:"ws_url"`
	AccessToken  string `mapstructure:"access_token"`
	NickName     string `mapstructure:"nickname"`
	SuperUsers   string `mapstructure:"super_users"`
}

// ParseSuperUsers 将 SuperUsers 逗号分隔字符串转换为 int64 列表
// 例如 "123,456" → []int64{123, 456}
func (c *BotConfig) ParseSuperUsers() []int64 {
	if c.SuperUsers == "" {
		return nil
	}
	parts := strings.Split(c.SuperUsers, ",")
	users := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			users = append(users, id)
		}
	}
	return users
}
