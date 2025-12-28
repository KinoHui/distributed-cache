# Docker 快速启动指南

本文档介绍如何使用 Docker 快速启动分布式缓存系统。

## 前置要求

- Docker 20.10+
- Docker Compose 2.0+

## 快速启动

### 一键启动整个集群

```bash
# 构建并启动所有服务
docker-compose up -d

# 查看服务状态
docker-compose ps

# 查看日志
docker-compose logs -f
```

这将启动以下服务：

| 服务 | 容器名 | 端口 | 说明 |
|------|--------|------|------|
| etcd | jincache-etcd | 2379, 2380 | 服务发现 |
| cache-node-1 | jincache-node-1 | 8001 | 缓存节点 1 |
| cache-node-2 | jincache-node-2 | 8002 | 缓存节点 2 |
| cache-node-3 | jincache-node-3 | 8003 | 缓存节点 3 |
| api-server | jincache-api | 9999 | API 服务器 |

### 停止服务

```bash
# 停止所有服务
docker-compose down

# 停止并删除数据卷
docker-compose down -v
```

## 服务说明

### etcd 服务
- 用于服务发现和节点注册
- 默认地址：`http://etcd:2379`
- 健康检查：每 10 秒检查一次

### 缓存节点
- 3 个缓存节点组成集群
- 使用一致性哈希进行数据分片
- 自动注册到 etcd
- 支持动态扩缩容

### API 服务器
- 提供 RESTful API 接口
- 默认端口：9999
- 自动路由到正确的缓存节点

## 使用示例

### 1. 设置缓存

```bash
curl -X POST "http://localhost:9999/api/set?key=Tom" -d "630"
```

### 2. 获取缓存

```bash
curl "http://localhost:9999/api?key=Tom"
```

### 3. 删除缓存

```bash
curl -X DELETE "http://localhost:9999/api/delete?key=Tom"
```

### 4. 获取统计信息

```bash
curl "http://localhost:9999/api/stats"
```

响应示例：
```json
{
  "keys_count": 3,
  "bytes_used": 15
}
```

## 查看日志

### 查看所有服务日志

```bash
docker-compose logs -f
```

### 查看特定服务日志

```bash
# 查看缓存节点 1 日志
docker-compose logs -f cache-node-1

# 查看 API 服务器日志
docker-compose logs -f api-server

# 查看 etcd 日志
docker-compose logs -f etcd
```

## 进入容器

```bash
# 进入缓存节点 1
docker-compose exec cache-node-1 sh

# 进入 API 服务器
docker-compose exec api-server sh

# 进入 etcd
docker-compose exec etcd sh
```

## 手动构建镜像

```bash
# 构建镜像
docker-compose build

# 重新构建并启动
docker-compose up -d --build
```

## 单独启动服务

### 只启动 etcd 和缓存节点

```bash
docker-compose up -d etcd cache-node-1 cache-node-2 cache-node-3
```

### 只启动 API 服务器

```bash
docker-compose up -d api-server
```

## 扩缩容

### 添加新节点

编辑 `docker-compose.yml`，添加新的服务：

```yaml
cache-node-4:
  build:
    context: .
    dockerfile: Dockerfile
  container_name: jincache-node-4
  ports:
    - "8004:8004"
  environment:
    - PORT=8004
    - GROUPNAME=default-group
    - ETCD_ENDPOINTS=http://etcd:2379
    - ENABLE_GETTER=true
  command: ["./jincache", "-port=8004", "-groupname=default-group", "-etcd=etcd:2379", "-enableGetter"]
  depends_on:
    etcd:
      condition: service_healthy
  networks:
    - jincache-network
  restart: unless-stopped
```

然后重新启动：

```bash
docker-compose up -d
```

### 减少节点

停止并删除节点：

```bash
docker-compose stop cache-node-3
docker-compose rm -f cache-node-3
```

## 故障排查

### 查看服务状态

```bash
docker-compose ps
```

### 重启特定服务

```bash
docker-compose restart cache-node-1
```

### 查看容器资源使用

```bash
docker stats
```

### 清理所有容器和镜像

```bash
docker-compose down --rmi all -v
```

## 网络架构

```
┌─────────────────────────────────────────────────────┐
│                    宿主机网络                         │
│   9999 (API)  8001,8002,8003 (缓存节点)  2379 (etcd) │
└────────────────────┬────────────────────────────────┘
                     │
                     ↓
┌─────────────────────────────────────────────────────┐
│              jincache-network (bridge)               │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐          │
│  │ API      │  │ Node 1   │  │ Node 2   │          │
│  │ :9999    │  │ :8001    │  │ :8002    │          │
│  └──────────┘  └──────────┘  └──────────┘          │
│  ┌──────────┐  ┌──────────┐                        │
│  │ Node 3   │  │ etcd     │                        │
│  │ :8003    │  │ :2379    │                        │
│  └──────────┘  └──────────┘                        │
└─────────────────────────────────────────────────────┘
```

## 性能调优

### 修改缓存大小

编辑 `docker-compose.yml`，修改环境变量：

```yaml
environment:
  - CACHE_SIZE=10MB  # 默认 2KB
```

### 修改超时时间

```yaml
environment:
  - REQUEST_TIMEOUT=5s  # 默认 3s
```

### 修改重试次数

```yaml
environment:
  - MAX_RETRIES=3  # 默认 2
```

## 数据持久化

当前配置不包含数据持久化，数据仅存储在内存中。如需持久化，可以：

1. 使用外部 etcd 集群
2. 添加 Redis 作为后端存储
3. 实现自定义的持久化机制

## 安全建议

生产环境使用时，建议：

1. 启用 TLS/SSL 加密通信
2. 添加身份认证机制
3. 限制网络访问
4. 使用 secrets 管理敏感信息
5. 定期更新镜像和依赖

## 更多信息

- 项目文档：[README.md](README.md)
- 客户端使用说明：[client使用说明.md](client使用说明.md)
- 交互流程：[交互流程.md](交互流程.md)