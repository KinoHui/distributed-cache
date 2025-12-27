package main

import (
	"distributed-cache-demo/jincache"
	"distributed-cache-demo/jincache/discovery"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

var db = map[string]string{
	"Tom":  "630",
	"Jack": "589",
	"Sam":  "567",
}

// Config 配置结构
type Config struct {
	Port       int
	API        bool
	APIAddr    string
	EtcdEndpts []string
}

func createGroup() *jincache.Group {
	return jincache.NewGroup("scores", 2<<10, jincache.GetterFunc(
		func(key string) ([]byte, error) {
			log.Println("[SlowDB] search key", key)
			if v, ok := db[key]; ok {
				return []byte(v), nil
			}
			return nil, fmt.Errorf("%s not exist", key)
		}))
}

func startCacheServer(config *Config, jin *jincache.Group) {
	// 构建节点地址
	nodeAddr := fmt.Sprintf("http://localhost:%d", config.Port)
	nodeID := fmt.Sprintf("node-%d", config.Port)

	// 创建服务发现
	sd, err := discovery.NewServiceDiscovery(config.EtcdEndpts, nodeID, nodeAddr)
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
	http.Handle("/api", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			key := r.URL.Query().Get("key")
			view, err := jin.Get(key)
			if err != nil {
				// 判断是否是key不存在的错误
				if err == jincache.KeyNotFoundError {
					http.Error(w, "key not found: "+key, http.StatusNotFound)
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(view.ByteSlice())
		}))
	log.Println("[Main] Frontend server is running at", apiAddr)
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
	jin := createGroup()

	// 启动API服务器（如果需要）
	if config.API {
		go startAPIServer(config.APIAddr, jin)
	}

	// 启动缓存服务器
	startCacheServer(config, jin)
}
