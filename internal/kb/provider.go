package kb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Capabilities 声明 Provider 支持的召回能力。
type Capabilities struct {
	Modes []RecallMode // 支持的召回模式
}

// Supports 判断是否支持指定模式
func (c Capabilities) Supports(mode RecallMode) bool {
	for _, m := range c.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Provider 知识库 Provider 抽象。
//
// 每个知识库产品（本地数据库、飞书、腾讯 IMA 等）实现此接口，屏蔽底层差异。
// Search 需按 req.Modes 分发到各自原生能力，返回按模式分组的排序结果；
// 不支持的模式直接跳过（不报错），单个模式失败也不应中断整体。
type Provider interface {
	// Name 返回 provider 类型标识（local / feishu）
	Name() string
	// Capabilities 返回能力声明
	Capabilities() Capabilities
	// Search 执行召回。
	Search(ctx context.Context, req *RecallRequest) (*RecallResult, error)
	// Close 释放资源（HTTP 客户端/连接池）
	Close() error
}

// Syncer 可选接口：实现此接口的 Provider 支持启动时内容同步
// （本地 provider 的 docs_dir 文件摄入）。
type Syncer interface {
	// Sync 执行一次内容同步（幂等）。
	Sync(ctx context.Context) error
}

// Ingester 可选接口：支持内容写入的 Provider（本地知识库）。
// kb_add 工具通过此接口录入知识，Provider 内部负责向量化。
type Ingester interface {
	// Store 存储/更新一条分块（按 Provider 内唯一标识幂等）。
	Store(ctx context.Context, chunk *Chunk) error
}

// Deps 注入给 Provider 工厂的公共依赖（由 main 组装）。
type Deps struct {
	Orm      *gorm.DB           // 本地 provider 的存储
	Embedder embedding.Embedder // 向量计算（可为 nil，缺省时 vector 模式降级）
	Logger   *zap.Logger
}

// Factory 依据配置构造一个 Provider 实例。
type Factory func(ctx context.Context, kbb *KnowledgeBase, cfg map[string]any, deps Deps) (Provider, error)

var (
	factoryMu sync.RWMutex
	factories = map[string]Factory{}
)

// RegisterProvider 注册 provider 工厂（未来 provider 接入点，如 feishu/ima/graph）。
// 名称不可重复；在 NewService 之前调用。
func RegisterProvider(name string, f Factory) error {
	if name == "" {
		return fmt.Errorf("kb: provider 名称不能为空")
	}
	if f == nil {
		return fmt.Errorf("kb: provider %q 工厂不能为空", name)
	}
	factoryMu.Lock()
	defer factoryMu.Unlock()
	if _, dup := factories[name]; dup {
		return fmt.Errorf("kb: provider %q 已注册", name)
	}
	factories[name] = f
	return nil
}

// createProvider 按知识库配置构造 provider 实例。
func createProvider(ctx context.Context, kbb *KnowledgeBase, deps Deps) (Provider, error) {
	factoryMu.RLock()
	f, ok := factories[kbb.Provider]
	factoryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("kb: 未知 provider %q（可用：%s）", kbb.Provider, providerNames())
	}
	return f(ctx, kbb, kbb.Config, deps)
}

func providerNames() string {
	factoryMu.RLock()
	defer factoryMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// ── 配置辅助 ──

// ResolveSecret 解析配置中的敏感值。
// 若值为字符串且以 "env:" 开头，则从环境变量读取对应变量名；
// 其它类型返回空字符串。
func ResolveSecret(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if strings.HasPrefix(s, "env:") {
		return os.Getenv(strings.TrimPrefix(s, "env:"))
	}
	return s
}

// ConfigString 读取字符串配置项，缺失或类型不符返回 def。
func ConfigString(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// ConfigInt 读取整数配置项，缺失或类型不符返回 def。
func ConfigInt(cfg map[string]any, key string, def int) int {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}

// ConfigFloat 读取浮点配置项，缺失或类型不符返回 def。
func ConfigFloat(cfg map[string]any, key string, def float64) float64 {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return def
}

// ConfigBool 读取布尔配置项，缺失或类型不符返回 def。
func ConfigBool(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// ConfigStrings 读取字符串数组配置项，缺失或类型不符返回 def。
func ConfigStrings(cfg map[string]any, key string, def []string) []string {
	if v, ok := cfg[key]; ok {
		if items, ok := v.([]any); ok {
			out := make([]string, 0, len(items))
			for _, it := range items {
				if s, ok := it.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return def
}
