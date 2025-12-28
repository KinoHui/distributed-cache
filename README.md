# 分布式缓存系统 (Distributed Cache System)

一个高性能、可扩展的分布式缓存系统，采用 Go 语言开发，支持动态扩缩容、服务发现、数据迁移和容错机制。

## 项目简介

本项目是一个完整的分布式缓存解决方案，适用于高并发场景下的数据缓存需求。系统采用一致性哈希算法实现数据分片，通过 etcd 实现服务发现，支持节点的动态加入和退出，具备完整的容错和数据迁移机制。

## 核心特性

- **LRU 缓存淘汰**：使用最近最少使用算法管理缓存容量
- **一致性哈希**：实现数据在多节点间的均匀分布，最小化节点变化时的数据迁移
- **服务发现**：基于 etcd 的自动服务注册与发现
- **动态扩缩容**：支持节点动态加入和退出，自动触发数据迁移
- **容错机制**：超时控制、重试机制、黑名单、健康检查
- **Singleflight**：防止缓存击穿，合并相同 key 的并发请求
- **RESTful API**：提供标准的 HTTP 接口
- **Protobuf 通信**：高效的二进制协议
- **数据迁移**：节点变化时自动迁移数据，避免数据丢失
- **Docker 支持**：提供 Docker Compose 快速部署

## 项目架构

```
┌─────────────────────────────────────────────────────────────┐
│                      应用层/API服务                          │
│                   (HTTP REST API接口)                        │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ↓
┌─────────────────────────────────────────────────────────────┐
│                    Client 层 (client包)                      │
│              - Get/Set/Delete/GetStats 操作                  │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ↓
┌─────────────────────────────────────────────────────────────┐
│              缓存节点集群 (HTTPPool + 一致性哈希)             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │
│  │ Node 1   │  │ Node 2   │  │ Node 3   │                  │
│  │ :8001    │  │ :8002    │  │ :8003    │                  │
│  └──────────┘  └──────────┘  └──────────┘                  │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ↓
┌─────────────────────────────────────────────────────────────┐
│              服务发现              │
│              - 节点注册/发现/健康检查                          │
│              - 节点变化监听                                   │
└─────────────────────────────────────────────────────────────┘
```

## 快速启动

### 方式一：Docker Compose（推荐）

```bash
# 一键启动整个集群
docker-compose up -d

# 查看服务状态
docker-compose ps

# 测试 API
curl -X POST "http://localhost:9999/api/set?key=Tom" -d "630"
curl "http://localhost:9999/api?key=Tom"

# 停止服务
docker-compose down
```

详细说明请参考 [DOCKER.md](DOCKER.md)

### 方式二：手动启动

1. **启动 etcd 服务**
```bash
# 使用 Docker 启动 etcd
docker run -d -p 2379:2379 --name etcd \
  quay.io/coreos/etcd:v3.5.9 \
  etcd --listen-client-urls http://0.0.0.0:2379 \
  --advertise-client-urls http://localhost:2379
```

2. **启动缓存节点**
```bash
# 启动节点 8001
./jincache.exe -port=8001 -groupname=default-group -etcd=localhost:2379 -enableGetter

# 启动节点 8002
./jincache.exe -port=8002 -groupname=default-group -etcd=localhost:2379 -enableGetter

# 启动节点 8003
./jincache.exe -port=8003 -groupname=default-group -etcd=localhost:2379 -enableGetter
```

3. **启动 API 服务器**
```bash
./jincache.exe -port=9999 -api -groupname=default-group -etcd=localhost:2379
```

## API 接口

### 设置缓存
```bash
curl -X POST "http://localhost:9999/api/set?key=Tom" -d "630"
```

### 获取缓存
```bash
curl "http://localhost:9999/api?key=Tom"
```

### 删除缓存
```bash
curl -X DELETE "http://localhost:9999/api/delete?key=Tom"
```

### 获取统计信息
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

## 项目结构

```
distributed-cache/
├── main.go                      # 主程序入口
├── go.mod                       # Go 模块定义
├── go.sum                       # 依赖锁定
├── Dockerfile                   # Docker 镜像构建
├── docker-compose.yml           # Docker Compose 编排
├── jincache/                    # 核心包
│   ├── jincache.go             # 主缓存逻辑
│   ├── cache.go                # 缓存接口
│   ├── byteview.go             # 字节数组视图
│   ├── http.go                 # HTTP 服务
│   ├── peer.go                 # 节点选择器
│   ├── client/                 # 客户端包
│   │   └── client.go
│   ├── consistenthash/         # 一致性哈希
│   │   └── consistenthash.go
│   ├── discovery/              # 服务发现
│   │   └── discovery.go
│   ├── jincachepb/             # Protobuf 定义
│   │   ├── jincachepb.proto
│   │   └── jincachepb.pb.go
│   ├── migration/              # 数据迁移
│   │   └── migrator.go
│   └── singleflight/           # 防缓存击穿
│       └── singleflight.go
├── lru/                         # LRU 实现
│   └── lru.go
└── test/                        # 测试文件
    ├── lru_test.go
    ├── cache_test.go
    └── consistenthash_test.go
```

## 核心技术

### 1. LRU 缓存淘汰
- 使用双向链表 + 哈希表实现 O(1) 时间复杂度的访问和删除
- 支持自定义缓存大小

### 2. 一致性哈希
- 虚拟节点技术（默认 50 个副本）
- 最小化节点变化时的数据迁移
- 支持自定义哈希函数

### 3. 服务发现
- 基于 etcd 的服务注册与发现
- 30 秒 TTL，10 秒心跳更新
- 实时监听节点变化

### 4. 容错机制
- 3 秒请求超时
- 最多重试 2 次，指数退避
- 节点失败 3 次后加入黑名单 5 分钟
- 定期清理过期黑名单

### 5. 数据迁移
- 节点变化时自动触发数据迁移
- 先迁移后清理策略，避免缓存雪崩
- 支持迁移失败重试

### 6. Singleflight
- 合并相同 key 的并发请求
- 防止缓存击穿
- 减轻后端压力

## 配置说明

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-port` | 服务端口 | 8001 |
| `-api` | 是否启动 API 服务器 | false |
| `-groupname` | 缓存组名称 | default-group |
| `-enableGetter` | 是否启用数据源加载 | false |
| `-etcd` | etcd 端点 | 192.168.59.132:2379 |

## 文档

- [Docker 使用说明](DOCKER.md)
- [客户端使用说明](client使用说明.md)
- [交互流程说明](交互流程.md)
- [数据迁移机制](数据迁移机制说明.md)
- [容错机制说明](容错机制说明.md)
- [动态扩缩容说明](动态扩缩容使用说明.md)
- [分布式缓存系统面试指南](分布式缓存系统面试指南.md)

## 技术栈

- **语言**：Go 1.21
- **服务发现**：etcd v3.5.9
- **序列化**：Protobuf
- **容器化**：Docker + Docker Compose
- **并发控制**：Singleflight

## 性能特点

- 高并发支持
- 低延迟响应
- 水平扩展能力
- 自动故障恢复
- 数据一致性保障

## 注意事项

1. 当前实现数据仅存储在内存中，重启后数据会丢失
2. Set 操作只会更新当前节点的缓存，不会同步到其他节点
3. 生产环境建议使用外部 etcd 集群
4. 建议配置适当的超时和重试参数

## License

MIT