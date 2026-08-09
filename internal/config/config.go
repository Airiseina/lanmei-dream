package config

import (
	"strings"
)

// Config 应用总配置，按模块拆分为子结构体
type Config struct {
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	AI        AIConfig        `mapstructure:"ai"`
	Bot       BotConfig       `mapstructure:"bot"`
	Plugin    PluginConfig    `mapstructure:"plugin"`
	Log       LogConfig       `mapstructure:"log"`
	Prompts   PromptsConfig   `mapstructure:"prompts"`
	Skills    SkillsConfig    `mapstructure:"skills"`
	Knowledge KnowledgeConfig `mapstructure:"knowledge"`
}

// PluginConfig 插件系统配置
type PluginConfig struct {
	// RootDir Wasm 插件根目录
	RootDir string `mapstructure:"root_dir"`
	// Builtins 内置业务插件开关（配置驱动注册，替代 main.go 硬编码注册）
	Builtins PluginBuiltinsConfig `mapstructure:"builtins"`
}

// PluginBuiltinsConfig 内置业务插件开关。
// 每个开关对应一个内置插件；false 时不注册该插件。
// 若同名插件已由 Wasm 动态加载，注册表会自动跳过内置注册，避免重复。
type PluginBuiltinsConfig struct {
	// Signin 签到插件
	Signin bool `mapstructure:"signin"`
	// Welcome 入群欢迎插件
	Welcome bool `mapstructure:"welcome"`
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
	// LLM 配置（OpenAI 兼容 API）
	LLMBaseURL     string  `mapstructure:"llm_base_url"`
	LLMAPIKey      string  `mapstructure:"llm_api_key"`
	LLMModel       string  `mapstructure:"llm_model"`
	LLMMaxTokens   int     `mapstructure:"llm_max_tokens"`
	LLMTemperature float64 `mapstructure:"llm_temperature"`

	// Embedding 配置（OpenAI 兼容 API）
	EmbeddingBaseURL string `mapstructure:"embedding_base_url"`
	EmbeddingAPIKey  string `mapstructure:"embedding_api_key"`
	EmbeddingModel   string `mapstructure:"embedding_model"`
	EmbeddingDim     int    `mapstructure:"embedding_dim"`
}

// BotConfig 机器人配置
type BotConfig struct {
	NickName   string          `mapstructure:"nickname"`
	SuperUsers string          `mapstructure:"super_users"` // 格式：platform:userID,... 如 qq:123456,wechat:wxid_xxx
	Gateway    GatewayConfig   `mapstructure:"gateway"`
	Stream     StreamConfig    `mapstructure:"stream"`
	Media      MediaConfig     `mapstructure:"media"`
	RateLimit  RateLimitConfig `mapstructure:"ratelimit"`
	Topic      TopicConfig     `mapstructure:"topic"`
}

// MediaConfig 多媒体处理配置（RustFS 对象存储 + 视觉理解）
type MediaConfig struct {
	// Endpoint RustFS 服务端点（S3 兼容），如 http://localhost:9000
	Endpoint string `mapstructure:"endpoint"`
	// AccessKey / SecretKey S3 凭据（敏感值建议用环境变量注入）
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	// Bucket 媒体对象存储桶名
	Bucket string `mapstructure:"bucket"`
	// Region S3 区域标识（RustFS 通常任意值即可）
	Region string `mapstructure:"region"`
	// MaxDownloadBytes 单张多媒体下载大小上限
	MaxDownloadBytes int64 `mapstructure:"max_download_bytes"`
	// VisionEnabled 视觉理解开关（需配置支持多模态的模型）
	VisionEnabled bool `mapstructure:"vision_enabled"`
	// VisionModel 视觉模型名（空则复用主对话模型）
	VisionModel string `mapstructure:"vision_model"`
}

// RateLimitConfig 消息去重与回复限流配置
type RateLimitConfig struct {
	// ReplyPerGroupPerMin 每群每分钟回复上限
	ReplyPerGroupPerMin int `mapstructure:"reply_per_group_per_min"`
	// LLMPerUserPerMin 每用户每分钟 LLM 对话上限
	LLMPerUserPerMin int `mapstructure:"llm_per_user_per_min"`
	// ReplyTotalPerMin 全局每分钟回复上限
	ReplyTotalPerMin int `mapstructure:"reply_total_per_min"`
}

// TopicConfig 群聊话题（Topic）系统配置
type TopicConfig struct {
	// Enabled topic 系统总开关（false 时回退为现行为：群聊全回复）
	Enabled bool `mapstructure:"enabled"`
	// Nicknames Bot 名字与别名（为空时使用 bot.nickname，内置"蓝莓"外号）
	Nicknames []string `mapstructure:"nicknames"`
	// TopicWindowMsgs 活跃窗口：最近 N 条群消息内提及/回复即保持 Active
	TopicWindowMsgs int `mapstructure:"topic_window_msgs"`
	// CoolingTimeoutMinutes 冷却超时（分钟）后归档
	CoolingTimeoutMinutes int `mapstructure:"cooling_timeout_minutes"`
	// SemanticThreshold 语义相关阈值（0~1），0 表示关闭语义判定（纯成员制）
	SemanticThreshold float64 `mapstructure:"semantic_threshold"`
	// CreditEnabled 回复配额（防刷屏）
	CreditEnabled bool `mapstructure:"credit_enabled"`
	// LLMRecheck 弱信号 LLM 复核开关（成本敏感，默认关闭）
	LLMRecheck bool `mapstructure:"llm_recheck"`
	// LLMRecheckIntervalMsgs 每 N 条群消息最多复核 1 条弱信号
	LLMRecheckIntervalMsgs int `mapstructure:"llm_recheck_interval_msgs"`
	// ArchiveIntervalSeconds 归档扫描间隔（秒）
	ArchiveIntervalSeconds int `mapstructure:"archive_interval_seconds"`
}

// StreamConfig 流式回复配置
type StreamConfig struct {
	// TypingSpeedMS 打字速度（毫秒/字）。
	// 段落发送间隔 = 下一段字数 × 打字速度，模拟真人打字耗时。
	// 设为 0 则禁用间隔机制（段落间不延迟）。
	TypingSpeedMS int `mapstructure:"typing_speed_ms"`

	// MinIntervalMS 最小发送间隔（毫秒）。
	// 无论字数多短，间隔不小于此值，保证平台不乱序。
	MinIntervalMS int `mapstructure:"min_interval_ms"`

	// MaxIntervalMS 最大发送间隔（毫秒）。
	// 无论字数多长，间隔不大于此值，避免过长等待。
	// 设为 0 表示不设上限。
	MaxIntervalMS int `mapstructure:"max_interval_ms"`

	// JitterPct 间隔抖动比例（0.0-1.0）。
	// 实际间隔 = 基础间隔 × (1 ± JitterPct)，增加真实感。
	// 0 表示无抖动。
	JitterPct float64 `mapstructure:"jitter_pct"`
}

// GatewayConfig 网关（反向 WebSocket 服务端）配置
type GatewayConfig struct {
	ListenAddr  string `mapstructure:"listen_addr"`  // 监听地址，如 "0.0.0.0:8080"
	AccessToken string `mapstructure:"access_token"` // 鉴权 token（空则不鉴权）
}

// PromptsConfig Prompt 系统路径配置
type PromptsConfig struct {
	Dir    string `mapstructure:"dir"`
	Config string `mapstructure:"config"`
}

// SkillsConfig Skill 系统路径配置
type SkillsConfig struct {
	Dir    string `mapstructure:"dir"`
	Config string `mapstructure:"config"`
}

// KnowledgeConfig 知识库系统配置
type KnowledgeConfig struct {
	// Enabled 知识库总开关，默认 false
	Enabled bool `mapstructure:"enabled"`

	// DefaultModes 隐式召回与 kb_search 工具的默认召回模式，可选值：vector/fuzzy/time
	// 空数组表示使用各 provider 的全部能力
	DefaultModes []string `mapstructure:"default_recall_modes"`

	// AutoRecallLimit 每轮对话隐式召回注入上下文的条数，默认 3
	AutoRecallLimit int `mapstructure:"auto_recall_limit"`

	// Weights 多路召回合并权重
	Weights RecallWeightsConfig `mapstructure:"weights"`

	// Bases 知识库列表
	Bases []KnowledgeBaseConfig `mapstructure:"bases"`
}

// RecallWeightsConfig 各召回模式的合并权重（多路命中时按权重累加排名分）
type RecallWeightsConfig struct {
	Vector float64 `mapstructure:"vector"`
	Fuzzy  float64 `mapstructure:"fuzzy"`
	Time   float64 `mapstructure:"time"`
}

// KnowledgeBaseConfig 单个知识库的配置。
// Config 为 provider 私有配置（如 local 的 docs_dir、feishu 的 app_id 等），
// 由对应 provider 工厂解析；敏感值支持 "env:VAR_NAME" 语法从环境变量读取。
type KnowledgeBaseConfig struct {
	ID          string         `mapstructure:"id"`
	Name        string         `mapstructure:"name"`
	Description string         `mapstructure:"description"`
	Provider    string         `mapstructure:"provider"` // local / feishu
	Enabled     bool           `mapstructure:"enabled"`
	RecallLimit int            `mapstructure:"recall_limit"` // 单模式召回上限，默认 5
	Config      map[string]any `mapstructure:"config"`
}

// LogConfig 日志相关配置
type LogConfig struct {
	Level       string `mapstructure:"level"`       // 日志级别：debug/info/warn/error，默认 info
	Persistent  bool   `mapstructure:"persistent"`  // 是否持久化到文件，默认 false
	Path        string `mapstructure:"path"`        // 日志文件路径（Persistent=true 时），默认 ./logs/lanmei.log
	Compression bool   `mapstructure:"compression"` // 是否压缩旧日志文件，默认 true
	MaxSize     int    `mapstructure:"max_size"`    // 单个日志文件最大大小（MB），默认 100
	MaxAge      int    `mapstructure:"max_age"`     // 日志文件最大保留天数，默认 30
	MaxBackups  int    `mapstructure:"max_backups"` // 最多保留的旧日志文件数，默认 10
}

// ParseSuperUsers 返回原始超级用户字符串，供 plugin.ParseSuperUsers 使用
func (c *BotConfig) ParseSuperUsers() string {
	return strings.TrimSpace(c.SuperUsers)
}
