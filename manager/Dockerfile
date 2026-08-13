# ── 管理面板前端镜像：多阶段构建（node 构建 → nginx 托管）──
#
# 构建上下文：manager/（docker-compose 中 build.context: ./manager）
# 或：docker build -f manager/Dockerfile manager

# ── 阶段 1：前端构建 ──
FROM node:24-alpine AS fe-builder

WORKDIR /app

# 国内网络加速：替换默认 registry（境外超时）
RUN npm config set registry https://registry.npmmirror.com

# 先拷贝依赖清单，利用层缓存
COPY package.json package-lock.json ./
# vue 3.6.0-rc.3 与部分依赖存在 peer 冲突，需 --legacy-peer-deps（与本地 npm install 一致）
RUN npm ci --legacy-peer-deps --no-audit --no-fund

COPY . .
RUN npm run build

# ── 阶段 2：nginx 托管 ──
FROM nginx:1.31.3-alpine3.24

# 站点配置（/api 反向代理到 lanmei:8090，SPA 回退等）
COPY nginx.conf /etc/nginx/conf.d/default.conf

COPY --from=fe-builder /app/dist /usr/share/nginx/html

EXPOSE 80
