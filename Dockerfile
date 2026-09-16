# 多阶段构建：builder 编译静态二进制，运行阶段只携带二进制与运行时资源。

FROM golang:1.26-alpine AS builder

# 国内网络加速：Alpine 包源替换为腾讯云镜像（默认 dl-cdn.alpinelinux.org 境外访问慢）
RUN sed -i 's#dl-cdn.alpinelinux.org#mirrors.cloud.tencent.com#g' /etc/apk/repositories \
    && apk add --no-cache git

WORKDIR /app

# 国内网络加速：替换默认的 proxy.golang.org（境外访问超时）
ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 产出静态链接二进制（无 glibc 依赖），运行阶段用 alpine 即可；
# -s -w 去掉符号表与调试信息，减小镜像体积。
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /lanmei ./cmd/lanmei

# 运行阶段：alpine 基础镜像，仅补证书与时区。
FROM alpine:3.21

# 国内网络加速：Alpine 包源替换为腾讯云镜像。
# ca-certificates 供 HTTPS 出站调用（LLM、飞书等），tzdata 让 ENV TZ 生效。
RUN sed -i 's#dl-cdn.alpinelinux.org#mirrors.cloud.tencent.com#g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /lanmei /app/lanmei

# 运行时资源：config / prompts / skills / quizdata 默认打包进镜像。
# 目录内均为非敏感配置（密钥走环境变量），作为裸镜像自包含兜底；
# 使用 docker-compose 部署时会被只读挂载（./config:ro 等）覆盖。
COPY --from=builder /app/config /app/config
COPY --from=builder /app/prompts /app/prompts
COPY --from=builder /app/skills /app/skills
COPY --from=builder /app/quizdata /app/quizdata

# 环境变量通过 docker-compose / .env 注入，不硬编码
ENV TZ=Asia/Shanghai

ENTRYPOINT ["/app/lanmei"]
