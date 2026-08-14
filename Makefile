# ============================================================
# 蓝妹（lanmei-dream）一键启动
# 前置：docker compose（全栈）、Go、Node.js（本地开发）
# ============================================================

.PHONY: help up down restart logs ps build dev-server dev-web dev

# 默认显示帮助
.DEFAULT_GOAL := help

help: ## 显示所有命令
	@echo "蓝妹启动命令："
	@echo "  make up          构建并启动全部服务（含管理面板，统一入口 http://<host>:80）"
	@echo "  make down        停止全部服务"
	@echo "  make restart     重启 lanmei 服务"
	@echo "  make logs        跟随 lanmei 日志"
	@echo "  make ps          查看服务状态"
	@echo "  make build       编译 Go 后端"
	@echo "  make dev-server  本地运行后端（管理面板 :8090）"
	@echo "  make dev-web     本地运行管理面板前端（vite :5173，/api 代理到 :8090）"
	@echo ""
	@echo "本地开发请开两个终端：make dev-server + make dev-web"

# ── Docker 全栈（生产/一键，含管理面板）──────────────────────
up: ## 构建并启动全部服务
	docker compose up -d --build

down: ## 停止全部服务
	docker compose down

restart: ## 重启 lanmei
	docker compose restart lanmei

logs: ## 跟随 lanmei 日志
	docker compose logs -f lanmei

ps: ## 服务状态
	docker compose ps

# ── 本地开发 ────────────────────────────────────────────────
build: ## 编译 Go 后端
	go build ./cmd/lanmei

dev-server: ## 本地运行后端（需 config.toml [manager] enabled=true + .env 配置 MANAGER 凭据）
	go run ./cmd/lanmei

dev-web: ## 本地运行管理面板前端
	cd manager && npm install && npm run dev
