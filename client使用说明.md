# Client 使用说明

## 概述

API已经从缓存服务器中抽离出来，现在使用独立的client包进行通信。client支持Get、Set、Delete等基本操作。

## Client API

### 创建客户端

```go
import "distributed-cache-demo/jincache/client"

// 创建客户端，连接到缓存服务器
cacheClient := client.NewClient("http://localhost:8001")
```

### Get 操作

获取缓存中的值：

```go
value, err := cacheClient.Get("scores", "Tom")
if err != nil {
    if err.Error() == "key not found: Tom" {
        // key不存在
    } else {
        // 其他错误
    }
}
fmt.Println(string(value)) // 输出: 630
```

### Set 操作

设置缓存值：

```go
err := cacheClient.Set("scores", "Tom", []byte("630"))
if err != nil {
    // 处理错误
}
```

### Delete 操作

删除缓存值：

```go
err := cacheClient.Delete("scores", "Tom")
if err != nil {
    if err.Error() == "key not found: Tom" {
        // key不存在
    } else {
        // 其他错误
    }
}
```

### GetStats 操作

获取缓存统计信息：

```go
stats, err := cacheClient.GetStats("scores")
if err != nil {
    // 处理错误
}
fmt.Printf("Keys: %d, Bytes: %d\n", stats.KeysCount, stats.BytesUsed)
```

## HTTP API

启动API服务器后，可以通过HTTP接口访问缓存：

### GET /api?key=<key>

获取缓存值：

```bash
curl "http://localhost:9999/api?key=Tom"
```

响应：
- 成功：返回二进制数据
- 404：key不存在
- 500：服务器错误

### POST /api/set?key=<key>

设置缓存值：

```bash
curl -X POST "http://localhost:9999/api/set?key=Tom" -d "630"
```

响应：
- 成功：返回 "OK"
- 500：服务器错误

### DELETE /api/delete?key=<key>

删除缓存值：

```bash
curl -X DELETE "http://localhost:9999/api/delete?key=Tom"
```

响应：
- 成功：返回 "OK"
- 404：key不存在
- 500：服务器错误

### GET /api/stats

获取缓存统计信息：

```bash
curl "http://localhost:9999/api/stats"
```

响应（JSON格式）：
```json
{
  "keys_count": 3,
  "bytes_used": 15
}
```

## 启动方式

### 启动缓存服务器

```bash
# 启动节点8001
./jincache.exe -port=8001 -etcd=192.168.59.132:2379

# 启动节点8002
./jincache.exe -port=8002 -etcd=192.168.59.132:2379

# 启动节点8003
./jincache.exe -port=8003 -etcd=192.168.59.132:2379
```

### 启动API服务器

```bash
# 启动API服务器（在9000端口）
./jincache.exe -port=9000 -api -etcd=192.168.59.132:2379
```

注意：API服务器需要配置连接到任意一个缓存节点的地址。当前实现中，API服务器默认连接到`http://localhost:8001`。

## 使用示例

### Go代码示例

```go
package main

import (
    "distributed-cache-demo/jincache/client"
    "fmt"
    "log"
)

func main() {
    // 创建客户端
    cacheClient := client.NewClient("http://localhost:8001")

    // Set操作
    err := cacheClient.Set("scores", "Alice", []byte("95"))
    if err != nil {
        log.Fatal(err)
    }

    // Get操作
    value, err := cacheClient.Get("scores", "Alice")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Alice's score: %s\n", string(value))

    // Delete操作
    err = cacheClient.Delete("scores", "Alice")
    if err != nil {
        log.Fatal(err)
    }

    // GetStats操作
    stats, err := cacheClient.GetStats("scores")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Total keys: %d\n", stats.KeysCount)
}
```

### curl示例

```bash
# Set
curl -X POST "http://localhost:9999/api/set?key=Alice" -d "95"

# Get
curl "http://localhost:9999/api?key=Alice"

# Delete
curl -X DELETE "http://localhost:9999/api/delete?key=Alice"

# Stats
curl "http://localhost:9999/api/stats"
```

## 注意事项

1. **客户端连接**：当前实现中，API服务器固定连接到`http://localhost:8001`。在生产环境中，应该支持动态配置或负载均衡。

2. **容错机制**：client使用5秒超时，节点间通信有完整的容错机制（超时、重试、降级等）。

3. **分布式路由**：所有请求都会通过一致性哈希自动路由到正确的节点。

4. **数据一致性**：Set操作只会更新当前节点的缓存，不会同步到其他节点。如果需要数据同步，需要实现额外的机制。

## 架构变化

### 之前

```
客户端 → API服务器 (耦合)
         ↓
      缓存服务器
```

### 现在

```
客户端 → API服务器 (独立)
         ↓
      Client (HTTP客户端)
         ↓
      缓存服务器
```

API服务器和缓存服务器完全解耦，可以独立部署和扩展。