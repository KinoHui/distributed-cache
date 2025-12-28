# 多阶段构建：构建阶段
FROM golang:1.21-alpine AS builder

# 设置工作目录
WORKDIR /app

# 安装必要的工具
RUN apk add --no-cache git

# 复制 go.mod 和 go.sum
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建二进制文件
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o jincache .

# 运行阶段
FROM alpine:3.19

# 安装 ca-certificates（用于 HTTPS 请求）
RUN apk --no-cache add ca-certificates tzdata

# 设置时区为上海
ENV TZ=Asia/Shanghai

# 创建非 root 用户
RUN addgroup -g 1000 jincache && \
    adduser -D -u 1000 -G jincache jincache

# 设置工作目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /app/jincache .

# 更改文件所有者
RUN chown -R jincache:jincache /app

# 切换到非 root 用户
USER jincache

# 暴露端口
EXPOSE 8001 8002 8003 9999

# 默认启动命令（可通过 docker-compose 覆盖）
CMD ["./jincache"]