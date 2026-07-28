package config

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Init 初始化配置：命令行参数 → 配置文件 → 环境变量 → 默认值
func Init() (*Config, error) {
	initFlags()
	pflag.Parse()

	v := viper.New()
	setDefaults(v)

	// 配置文件
	configFile, _ := pflag.CommandLine.GetString("config")
	v.SetConfigFile(configFile)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Println("未找到配置文件，将使用默认值和环境变量")
		} else if os.IsNotExist(err) {
			log.Println("未找到配置文件，将使用默认值和环境变量")
		} else {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	}

	// 环境变量：LANMEI_DATABASE_URL、LANMEI_BOT_GATEWAY_LISTEN_ADDR 等
	v.SetEnvPrefix("LANMEI")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 命令行参数绑定
	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		if f.Name != "config" {
			_ = v.BindPFlag(f.Name, f)
		}
	})

	// 解析为结构体
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	return &cfg, nil
}

func initFlags() {
	pflag.String("config", "./config.toml", "配置文件路径")
	pflag.String("database.url", "", "PostgreSQL 连接字符串")
	pflag.String("redis.addr", "", "Redis 地址")
	// LLM
	pflag.String("ai.llm_base_url", "", "LLM API 基础地址（OpenAI 兼容）")
	pflag.String("ai.llm_api_key", "", "LLM API 密钥")
	pflag.String("ai.llm_model", "", "LLM 模型名")
	pflag.Int("ai.llm_max_tokens", 0, "LLM 单次回复最大 token 数")
	pflag.Float64("ai.llm_temperature", 0, "LLM 生成温度")
	// Embedding
	pflag.String("ai.embedding_base_url", "", "Embedding API 基础地址")
	pflag.String("ai.embedding_api_key", "", "Embedding API 密钥")
	pflag.String("ai.embedding_model", "", "Embedding 模型名")
	pflag.Int("ai.embedding_dim", 0, "向量维度")
	// Bot
	pflag.String("bot.gateway.listen_addr", "", "网关监听地址")
	pflag.String("bot.gateway.access_token", "", "网关鉴权 token")
	pflag.String("bot.nickname", "", "机器人昵称")
	pflag.String("bot.super_users", "", "超级用户列表（格式：platform:userID,逗号分隔）")
	pflag.String("plugin.root_dir", "", "Wasm 插件根目录")
}

func setDefaults(v *viper.Viper) {
	// 数据库默认值
	v.SetDefault("database.url", "postgres://postgres:postgres@localhost:5432/lanmei?sslmode=disable")

	// Redis 默认值
	v.SetDefault("redis.addr", "localhost:6379")

	// AI 默认值
	v.SetDefault("ai.llm_base_url", "https://api.openai.com/v1")
	v.SetDefault("ai.llm_model", "gpt-4o-mini")
	v.SetDefault("ai.llm_max_tokens", 4096)
	v.SetDefault("ai.llm_temperature", 0.7)
	v.SetDefault("ai.embedding_base_url", "https://api.openai.com/v1")
	v.SetDefault("ai.embedding_model", "text-embedding-3-small")
	v.SetDefault("ai.embedding_dim", 1536)

	// Bot 默认值
	v.SetDefault("bot.gateway.listen_addr", "0.0.0.0:8080")
	v.SetDefault("bot.gateway.access_token", "")
	v.SetDefault("bot.nickname", "蓝妹")
	v.SetDefault("bot.super_users", "")

	// Plugin 默认值
	v.SetDefault("plugin.root_dir", "./data/plugins")
}
