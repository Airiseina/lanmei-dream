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
	"github.com/DaWesen/lanmei-dream/internal/bot"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/infra"
	"github.com/DaWesen/lanmei-dream/internal/signin"
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

	// ── AI 对话服务 ──
	// TODO: 等用户指定 LLM 和 Embedding 提供方后，在此初始化具体实现
	var (
		llmClient llm.LLMClient
		embedder  embedding.Embedder
	)

	var chatSvc *ai.ChatService
	if llmClient != nil && embedder != nil {
		chatSvc = ai.NewChatService(llmClient, embedder, inf.MemStore, inf.DB)
		log.Println("AI 对话服务就绪")
	} else {
		log.Println("⚠ LLM/Embedding 未配置，角色扮演不可用（命令系统正常）")
	}

	// ── 命令系统 ──
	cmdSys := command.New()
	signinPlugin := signin.New(inf.DB)
	cmdSys.Register(command.Command{
		Name:        "签到",
		Description: "每日签到",
		Handler:     signinPlugin.HandleSignin,
	})
	cmdSys.Register(command.Command{
		Name:        "帮助",
		Description: "显示可用命令",
		Handler:     cmdSys.HelpHandler,
	})

	// ── ZeroBot + Conduit ──
	b := bot.New(&cfg.Bot, cmdSys, chatSvc, inf.DB, inf.StateStore, llmClient)

	// 优雅退出
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("正在关闭...")
		cancel()
		os.Exit(0)
	}()

	log.Printf("蓝妹启动，WebSocket → %s", cfg.Bot.WebSocketURL)
	b.Run()
}
