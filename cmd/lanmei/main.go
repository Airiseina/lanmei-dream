package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/bizplugin"
	"github.com/DaWesen/lanmei-dream/internal/bot"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/gateway"
	"github.com/DaWesen/lanmei-dream/internal/infra"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── 配置初始化 ──
	cfg, err := config.Init()
	if err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}

	// ── 基础设施（PostgreSQL+pgvector + Redis）──
	inf, err := infra.Setup(ctx, &cfg.Database, &cfg.Redis)
	if err != nil {
		log.Fatalf("基础设施初始化失败: %v", err)
	}
	defer inf.Close()

	authorizer, err := pluginpkg.NewService(inf.DB.Orm)
	if err != nil {
		log.Fatalf("插件授权服务初始化失败: %v", err)
	}
	superUserPrincipals := pluginpkg.ParseSuperUsers(cfg.Bot.ParseSuperUsers())
	if err := authorizer.InitBuiltinPolicies(superUserPrincipals); err != nil {
		log.Fatalf("插件内置权限初始化失败: %v", err)
	}

	var (
		llmClient llm.LLMClient
		embedder  embedding.Embedder
	)

	// ── LLM 客户端（eino，支持 OpenAI/DeepSeek/Qwen/Moonshot/Ark/Ollama）──
	if cfg.AI.LLMAPIKey != "" {
		einoLLM, err := llm.NewEinoClient(ctx, &llm.EinoOptions{
			BaseURL:     cfg.AI.LLMBaseURL,
			APIKey:      cfg.AI.LLMAPIKey,
			Model:       cfg.AI.LLMModel,
			MaxTokens:   cfg.AI.LLMMaxTokens,
			Temperature: cfg.AI.LLMTemperature,
		})
		if err != nil {
			log.Fatalf("LLM 初始化失败: %v", err)
		}
		llmClient = einoLLM
		log.Printf("LLM 就绪: %s %s", cfg.AI.LLMBaseURL, cfg.AI.LLMModel)
	} else {
		log.Println("⚠ LLM API Key 未配置，角色扮演不可用")
	}

	// ── Embedder 客户端（eino，支持多 provider）──
	if cfg.AI.EmbeddingAPIKey != "" {
		einoEmb, err := embedding.NewEinoEmbedder(ctx, &embedding.EinoOptions{
			BaseURL:   cfg.AI.EmbeddingBaseURL,
			APIKey:    cfg.AI.EmbeddingAPIKey,
			Model:     cfg.AI.EmbeddingModel,
			Dimension: cfg.AI.EmbeddingDim,
		})
		if err != nil {
			log.Fatalf("Embedder 初始化失败: %v", err)
		}
		embedder = einoEmb
		log.Printf("Embedder 就绪: %s %s (dim=%d)", cfg.AI.EmbeddingBaseURL, cfg.AI.EmbeddingModel, cfg.AI.EmbeddingDim)
	} else {
		log.Println("⚠ Embedding API Key 未配置，RAG 检索不可用")
	}

	var chatSvc *ai.ChatService
	if llmClient != nil && embedder != nil {
		chatSvc = ai.NewChatService(llmClient, embedder, inf.MemStore, inf.DB)
		log.Println("AI 对话服务就绪")
	}

	// ── 命令系统 ──
	cmdSys := command.New()
	if err := cmdSys.Register(command.Command{
		Name:        "帮助",
		Description: "显示可用命令",
		Handler:     cmdSys.HelpHandler,
	}); err != nil {
		log.Fatalf("注册帮助命令失败: %v", err)
	}

	// ── 插件系统 ──
	// 创建插件注册表（引擎在 bot.New 中创建，随后通过 SetEngine 注入）
	pluginReg := pluginpkg.NewRegistry(nil, inf.StateStore, inf.DB, cmdSys)

	// ── 网关（反向 WebSocket 服务端）──
	gwServer := gateway.NewServer(&gateway.ListenConfig{
		ListenAddr:  cfg.Bot.Gateway.ListenAddr,
		AccessToken: cfg.Bot.Gateway.AccessToken,
	}, nil) // handler 稍后由 Bot 设置

	b := bot.New(&cfg.Bot, cmdSys, chatSvc, inf.DB, inf.StateStore, llmClient, pluginReg, gwServer)

	// 将 Bot 注册为网关事件处理器
	gwServer.SetHandler(b)

	wasmManager, err := pluginpkg.NewWasmManager(&cfg.Plugin, inf.DB, pluginReg, authorizer, nil)
	if err != nil {
		log.Fatalf("Wasm 插件管理器初始化失败: %v", err)
	}
	if err := cmdSys.Register(pluginpkg.NewWasmInstallCommand(ctx, wasmManager)); err != nil {
		log.Fatalf("注册插件管理命令失败: %v", err)
	}
	if err := wasmManager.LoadEnabled(ctx, pluginpkg.SystemPrincipal("startup")); err != nil {
		log.Fatalf("恢复已启用 Wasm 插件失败: %v", err)
	}

	if _, wasmSigninLoaded := pluginReg.Get("signin"); !wasmSigninLoaded {
		if err := pluginReg.Register(bizplugin.NewSigninPlugin(inf.DB)); err != nil {
			log.Fatalf("注册签到插件失败: %v", err)
		}
	}

	if err := pluginReg.InitPlugins(ctx); err != nil {
		log.Fatalf("插件初始化失败: %v", err)
	}
	if err := pluginReg.StartPlugins(ctx); err != nil {
		log.Fatalf("插件启动失败: %v", err)
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("正在关闭...")
		pluginReg.StopPlugins(ctx)
		gwServer.Shutdown()
		cancel()
		os.Exit(0)
	}()

	log.Printf("蓝妹启动，网关监听 → %s", cfg.Bot.Gateway.ListenAddr)
	b.Run()
}
