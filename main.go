package main

import (
	"distributed-cache-demo/jincache"
	"distributed-cache-demo/jincache/client"
	"distributed-cache-demo/jincache/discovery"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

var db = map[string]string{
	"Tom":   "630",
	"Jack":  "589",
	"Jackk": "999",
	"Sam":   "567",
}

// Config 配置结构
type Config struct {
	Port       int
	API        bool
	APIAddr    string
	EtcdEndpts []string
}

func createGroup() (*jincache.Group, string) {
	groupName := "scores"
	return jincache.NewGroup(groupName, 2<<10, jincache.GetterFunc(
		func(key string) ([]byte, error) {
			log.Println("[SlowDB] search key", key)
			if v, ok := db[key]; ok {
				return []byte(v), nil
			}
			return nil, fmt.Errorf("%s not exist", key)
		})), groupName
}

func startCacheServer(config *Config, jin *jincache.Group, groupName string) {
	// 构建节点地址
	nodeAddr := fmt.Sprintf("http://localhost:%d", config.Port)
	nodeID := fmt.Sprintf("node-%d", config.Port)

	// 创建服务发现，指定group名称
	sd, err := discovery.NewServiceDiscovery(config.EtcdEndpts, nodeID, nodeAddr, groupName)
	if err != nil {
		log.Fatalf("Failed to create service discovery: %v", err)
	}

	// 注册服务
	if err := sd.Register(); err != nil {
		log.Fatalf("Failed to register service: %v", err)
	}

	// 创建增强的HTTP池
	// hashFunc := func(data []byte) uint32 {
	// 	fmt.Println("######node name: " + string(data))
	// 	if len(data) > 1 {
	// 		fmt.Println("######hashed result: ", uint32(data[1]))
	// 		return uint32(data[1])
	// 	} else {
	// 		fmt.Println("######hashed result: ", uint32(data[0]))
	// 		return uint32(data[0])
	// 	}
	// }
	peers := jincache.NewEnhancedHTTPPool(nodeAddr, sd, jin)
	jin.RegisterPeers(peers)

	log.Printf("[Main] Cache server %s is running at %s", nodeID, nodeAddr)
	log.Printf("[Main] Etcd endpoints: %v", config.EtcdEndpts)

	// 设置优雅关闭
	go handleGracefulShutdown(sd, peers)

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", config.Port), peers))
}

func startAPIServer(apiAddr string, jin *jincache.Group) {
	// 创建缓存客户端，连接到本地缓存服务器
	cacheClient := client.NewClient("http://localhost:8001")

	// GET /api?key=<key> - 获取缓存值
	http.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "key parameter is required", http.StatusBadRequest)
			return
		}

		value, err := cacheClient.Get("scores", key)
		if err != nil {
			if err.Error() == "key not found: "+key {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(value)
	})

	// POST /api/set - 设置缓存值
	http.HandleFunc("/api/set", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "key parameter is required", http.StatusBadRequest)
			return
		}

		value, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		if err := cacheClient.Set("scores", key, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// DELETE /api/delete?key=<key> - 删除缓存值
	http.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "key parameter is required", http.StatusBadRequest)
			return
		}

		if err := cacheClient.Delete("scores", key); err != nil {
			if err.Error() == "key not found: "+key {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// GET /api/stats - 获取统计信息
	http.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		stats, err := cacheClient.GetStats("scores")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	log.Println("[Main] API server is running at", apiAddr)
	log.Println("[Main] Available endpoints:")
	log.Println("[Main]   GET    /api?key=<key>          - Get value")
	log.Println("[Main]   POST   /api/set?key=<key>      - Set value")
	log.Println("[Main]   DELETE /api/delete?key=<key>   - Delete value")
	log.Println("[Main]   GET    /api/stats              - Get statistics")
	log.Fatal(http.ListenAndServe(":9999", nil))
}

func handleGracefulShutdown(sd *discovery.ServiceDiscovery, peers *jincache.HTTPPool) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	log.Println("[Main] Shutting down gracefully...")

	// 注销服务
	if err := sd.Deregister(); err != nil {
		log.Printf("[Main] Failed to deregister service: %v", err)
	}

	// 关闭HTTP池
	peers.Close()

	// 关闭服务发现
	if err := sd.Close(); err != nil {
		log.Printf("[Main] Failed to close service discovery: %v", err)
	}

	log.Println("[Main] Shutdown complete")
	os.Exit(0)
}

func parseConfig() *Config {
	config := &Config{}

	flag.IntVar(&config.Port, "port", 8001, "jincache server port")
	flag.BoolVar(&config.API, "api", false, "Start a api server?")

	// 解析etcd端点
	var etcdEndpoints string
	flag.StringVar(&etcdEndpoints, "etcd", "192.168.59.132:2379", "etcd endpoints (comma separated)")
	flag.Parse()

	// 解析etcd端点列表
	if etcdEndpoints != "" {
		config.EtcdEndpts = []string{"http://" + etcdEndpoints}
	} else {
		config.EtcdEndpts = []string{"http://192.168.59.132:2379"} // 默认值
	}

	config.APIAddr = "http://localhost:9999"

	return config
}

func main() {
	config := parseConfig()

	// 创建缓存组
	jin, groupName := createGroup()

	// 启动API服务器（如果需要）
	if config.API {
		startAPIServer(config.APIAddr, jin)
		return
	}

	// 启动缓存服务器
	startCacheServer(config, jin, groupName)
}
