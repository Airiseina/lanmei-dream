package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/bot"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/infra"
	"github.com/DaWesen/lanmei-dream/internal/signin"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── 基础设施（PostgreSQL+pgvector + Redis）──
	inf, err := infra.Setup(ctx, &infra.Config{
		DatabaseURL:  env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/lanmei?sslmode=disable"),
		RedisAddr:    env("REDIS_ADDR", "localhost:6379"),
		EmbeddingDim: envInt("EMBEDDING_DIM", 1024),
	})
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
	wsURL := env("WS_URL", "ws://127.0.0.1:3001")
	accessToken := os.Getenv("ACCESS_TOKEN")
	nick := env("BOT_NICKNAME", "蓝妹")
	superUsers := parseSuperUsers(os.Getenv("SUPER_USERS"))

	b := bot.New(&bot.BotConfig{
		WebSocketURL: wsURL,
		AccessToken:  accessToken,
		NickName:     nick,
		SuperUsers:   superUsers,
	}, cmdSys, chatSvc, inf.DB, inf.StateStore, llmClient)

	// 优雅退出
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("正在关闭...")
		cancel()
		os.Exit(0)
	}()

	log.Printf("蓝妹启动，WebSocket → %s", wsURL)
	b.Run()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func parseSuperUsers(s string) []int64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var users []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			users = append(users, id)
		}
	}
	return users
}
