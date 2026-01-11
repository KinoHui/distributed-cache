# JinCache 分布式缓存系统

<div align="center">

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-MIT-green.svg)
![Docker](https://img.shields.io/badge/Docker-Supported-blue?style=flat&logo=docker)

一个高性能、可扩展的分布式缓存系统，采用 Go 语言开发

</div>

---

## 📖 项目简介

JinCache 是一个完整的分布式缓存解决方案，专为高并发场景下的数据缓存需求而设计。系统采用一致性哈希算法实现数据分片，通过 etcd 实现服务发现，支持节点的动态加入和退出，具备完整的容错和数据迁移机制。

> 本项目基于 [geektutu](https://github.com/geektutu/7days-golang) 的分布式缓存项目二次开发，在原有基础上进行了功能增强和优化。

### 为什么选择 JinCache？

- **高性能**：基于 Go 语言并发模型，支持高并发访问
- **高可用**：自动故障检测与恢复，节点动态扩缩容
- **易扩展**：水平扩展能力强，支持动态添加/移除节点
- **容错性强**：完善的超时控制、重试机制和黑名单策略
- **生产就绪**：提供 Docker 支持，开箱即用

---

## ✨ 核心特性

### 🚀 性能优化
- **LRU 缓存淘汰**：使用最近最少使用算法，O(1) 时间复杂度的访问和删除
- **Singleflight 机制**：防止缓存击穿，合并相同 key 的并发请求
- **Protobuf 通信**：高效的二进制协议，减少网络传输开销

### 🔄 分布式能力
- **一致性哈希**：虚拟节点技术（默认 50 个副本），最小化节点变化时的数据迁移
- **服务发现**：基于 etcd 的自动服务注册与发现，实时监听节点变化
- **动态扩缩容**：支持节点动态加入和退出，自动触发数据迁移

### 🛡️ 可靠性保障
- **容错机制**：超时控制、指数退避重试、黑名单、健康检查
- **数据迁移**：节点变化时自动迁移数据，先迁移后清理策略避免缓存雪崩
- **故障恢复**：节点失败后自动添加黑名单，持续提供服务

### 🛠️ 易用性
- **RESTful API**：提供标准的 HTTP 接口，易于集成
- **Docker 支持**：提供 Docker Compose 一键部署
- **配置灵活**：支持命令行参数和 YAML 配置文件

---

## 🏗️ 架构设计

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

---

## 🚀 快速开始

### 方式一：Docker Compose（推荐）

这是最简单的方式，一键启动整个集群（etcd + 3 个缓存节点 + API 服务器）。

```bash
# 1. 克隆项目
git clone https://github.com/KinoHui/distributed-cache.git
cd distributed-cache

# 2. 启动所有服务
docker-compose up -d

# 3. 查看服务状态
docker-compose ps

# 4. 测试 API
curl -X POST "http://localhost:9999/api/set?key=Tom" -d "630"
curl "http://localhost:9999/api?key=Tom"

# 5. 停止服务
docker-compose down
```

详细说明请参考 [DOCKER.md](DOCKER.md)

### 方式二：手动启动

如果需要更灵活的配置，可以手动启动各个组件。

#### 1. 启动 etcd 服务

```bash
docker run -d -p 2379:2379 --name etcd \
  quay.io/coreos/etcd:v3.5.9 \
  etcd --listen-client-urls http://0.0.0.0:2379 \
  --advertise-client-urls http://localhost:2379
```

#### 2. 构建项目（Win示例）

```bash
# 构建 Windows 二进制
go build -o jincache.exe .

# 构建 Linux 二进制（用于 Docker）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o jincache .
```

#### 3. 启动缓存节点

```bash
# 启动节点 8001
./jincache.exe -port=8001 -groupname=default-group -etcd=localhost:2379 -enableGetter

# 启动节点 8002
./jincache.exe -port=8002 -groupname=default-group -etcd=localhost:2379 -enableGetter

# 启动节点 8003
./jincache.exe -port=8003 -groupname=default-group -etcd=localhost:2379 -enableGetter
```

#### 4. 启动 API 服务器

```bash
./jincache.exe -port=9999 -api -groupname=default-group -etcd=localhost:2379
```

### 方式三：使用配置文件

```bash
# 使用缓存节点配置
./jincache.exe -config=etc/config.yaml

# 使用 API 服务器配置
./jincache.exe -config=etc/config-api.yaml
```

---

## 📡 API 文档

### 设置缓存

```bash
curl -X POST "http://localhost:9999/api/set?key=Tom" -d "630"
```

**请求参数：**
- `key`：缓存键（URL 参数）
- `body`：缓存值（请求体）

### 获取缓存

```bash
curl "http://localhost:9999/api?key=Tom"
```

**响应示例：**
```
630
```

### 删除缓存

```bash
curl -X DELETE "http://localhost:9999/api/delete?key=Tom"
```

### 获取统计信息

```bash
curl "http://localhost:9999/api/stats"
```

**响应示例：**
```json
{
  "keys_count": 3,
  "bytes_used": 15
}
```

---

## 📁 项目结构

```
distributed-cache/
├── main.go                      # 主程序入口
├── go.mod                       # Go 模块定义
├── go.sum                       # 依赖锁定
├── Dockerfile                   # Docker 镜像构建
├── docker-compose.yml           # Docker Compose 编排
├── DOCKER.md                    # Docker 使用说明
├── README.md                    # 项目说明文档
├── etc/                         # 配置文件目录
│   ├── config.yaml             # 缓存节点配置示例
│   └── config-api.yaml         # API 服务器配置示例
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

---

## 🔧 核心技术

### 1. LRU 缓存淘汰

使用双向链表 + 哈希表实现 O(1) 时间复杂度的访问和删除。

**特性：**
- 支持自定义缓存大小
- 自动淘汰最少使用的数据
- 高效的内存管理

### 2. 一致性哈希

通过虚拟节点技术实现数据在多节点间的均匀分布。

**特性：**
- 默认 50 个虚拟节点副本
- 最小化节点变化时的数据迁移
- 支持自定义哈希函数
- 数据分布均匀

### 3. 服务发现

基于 etcd 的自动服务注册与发现机制。

**特性：**
- 30 秒 TTL，10 秒心跳更新
- 实时监听节点变化
- 自动故障检测
- 支持多 etcd 节点

### 4. 容错机制

完善的错误处理和恢复策略。

**特性：**
- 3 秒请求超时
- 最多重试 2 次，指数退避
- 节点失败 3 次后加入黑名单 5 分钟
- 定期清理过期黑名单

### 5. 数据迁移

节点变化时自动触发数据迁移，确保数据不丢失。

**特性：**
- 先迁移后清理策略，避免缓存雪崩
- 支持迁移失败重试
- 迁移过程对业务透明
- 最小化数据迁移量

### 6. Singleflight

防止缓存击穿，合并相同 key 的并发请求。

**特性：**
- 合并相同 key 的并发请求
- 减轻后端压力
- 提升系统吞吐量
- 防止缓存雪崩

---

## ⚙️ 配置说明

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-config` | 配置文件路径（YAML） | - |
| `-port` | 服务端口 | 8001 |
| `-ip` | 服务 IP 地址 | localhost |
| `-api` | 是否启动 API 服务器 | false |
| `-groupname` | 缓存组名称 | default-group |
| `-enableGetter` | 是否启用数据源加载 | false |
| `-etcd` | etcd 端点（逗号分隔） | http://192.168.59.132:2379 |
| `-cacheBytes` | 缓存大小（字节） | 2048 (2KB) |
| `-nodeID` | 节点 ID | node-{port} |
| `-leaseTTL` | 租约 TTL（秒） | 30 |
| `-httpTimeout` | HTTP 请求超时（秒） | 5 |
| `-logLevel` | 日志级别 | info |

### 配置文件示例

**缓存节点配置** (`etc/config.yaml`):

```yaml
# 服务器配置
port: 8001
ip: "localhost"
api: false

# 缓存配置
group_name: "default-group"
cache_bytes: 2048
enable_getter: false

# etcd 配置
etcd_endpoints:
  - "http://192.168.59.132:2379"
etcd_dial_timeout: 5

# 服务发现配置
node_id: ""
lease_ttl: 30
heartbeat_interval: 10

# HTTP 客户端配置
http_timeout: 5

# 日志配置
log_level: "info"
```

**API 服务器配置** (`etc/config-api.yaml`):

```yaml
# 服务器配置
port: 9001
ip: "localhost"
api: true

# 缓存配置
group_name: "default-group"
cache_bytes: 2048
enable_getter: false

# etcd 配置
etcd_endpoints:
  - "http://192.168.59.132:2379"
```

---

## 🛠️ 技术栈

- **语言**：Go 1.21+
- **服务发现**：etcd v3.5.9/v3.5.12
- **序列化**：Protobuf (google.golang.org/protobuf v1.35.2)
- **容器化**：Docker + Docker Compose
- **依赖管理**：Go Modules

---

## 📊 性能特点

- ✅ **高并发支持**：基于 Go 协程，轻松处理万级并发
- ✅ **低延迟响应**：内存缓存，微秒级响应时间
- ✅ **水平扩展能力**：支持动态添加节点，线性扩展性能
- ✅ **自动故障恢复**：节点故障自动检测与恢复
- ✅ **数据一致性保障**：一致性哈希确保数据分布均匀

---

## ⚠️ 注意事项

1. **数据持久化**：当前实现数据仅存储在内存中，重启后数据会丢失
2. **Set 操作**：Set 操作只会更新当前节点的缓存，不会同步到其他节点
3. **etcd 配置**：生产环境建议使用外部 etcd 集群
4. **超时配置**：建议根据网络环境调整超时和重试参数
5. **缓存大小**：默认缓存大小为 2KB，生产环境需要根据实际情况调整

---

## 🧪 测试

运行测试：

```bash
go test ./test/...
```

测试覆盖：
- LRU 缓存测试
- 缓存组测试
- 一致性哈希测试

---

## 📝 开发规范

- **Go 版本**：项目使用 Go 1.23.1，建议使用 Go 1.21+
- **模块路径**：`github.com/KinoHui/distributed-cache`
- **包命名**：遵循 Go 官方规范，使用小写单词
- **错误处理**：所有错误必须显式处理，不使用 panic（除非在 init 或 main 中）

---

## 🚀 待实现功能

以下功能待开发中：

- **数据持久化**：支持将缓存数据持久化到磁盘，实现数据重启后恢复
  - 支持 RDB 快照和 AOF 日志两种持久化方式
  - 定期自动保存和手动触发保存
  - 支持数据压缩和加密

- **分片主从高可用**：实现数据分片的主从复制机制
  - 主节点故障时自动故障转移
  - 从节点读写分离，提升读取性能
  - 支持多级复制和级联复制

- **LRU 优化**：改进缓存淘汰策略
  - 支持 LFU（最不经常使用）淘汰策略
  - 支持自适应淘汰策略
  - 支持热点数据识别和优先保留

- **TTL 键过期**：支持键的过期时间设置
  - 支持设置键的生存时间（TTL）
  - 支持惰性删除和定期删除
  - 支持 EXPIRE、TTL、EXISTS 等命令

---

## 📄 License

MIT License