package config

import (
	"strings"
)

// Config 应用总配置，按模块拆分为子结构体
type Config struct {
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	AI       AIConfig       `mapstructure:"ai"`
	Bot      BotConfig      `mapstructure:"bot"`
	Plugin   PluginConfig   `mapstructure:"plugin"`
}

// PluginConfig 插件系统配置
type PluginConfig struct {
	RootDir string `mapstructure:"root_dir"`
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
	NickName   string      `mapstructure:"nickname"`
	SuperUsers string      `mapstructure:"super_users"` // 格式：platform:userID,... 如 qq:123456,wechat:wxid_xxx
	Gateway    GatewayConfig `mapstructure:"gateway"`
}

// GatewayConfig 网关（反向 WebSocket 服务端）配置
type GatewayConfig struct {
	ListenAddr  string `mapstructure:"listen_addr"`  // 监听地址，如 "0.0.0.0:8080"
	AccessToken string `mapstructure:"access_token"` // 鉴权 token（空则不鉴权）
}

// ParseSuperUsers 返回原始超级用户字符串，供 plugin.ParseSuperUsers 使用
func (c *BotConfig) ParseSuperUsers() string {
	return strings.TrimSpace(c.SuperUsers)
}
