package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/KinoHui/distributed-cache/jincache"
	"github.com/KinoHui/distributed-cache/jincache/client"
	"github.com/KinoHui/distributed-cache/jincache/discovery"

	"gopkg.in/yaml.v3"
)

var db = map[string]string{
	"Tom":  "630",
	"Jack": "589",
	// "Jackk": "999",
	"Sam": "567",
}

// 命令行参数变量
var (
	configPort         int
	configIP           string
	configAPI          bool
	configGroupName    string
	configEnableGetter bool
	configCacheBytes   int
	configNodeID       string
	configLeaseTTL     int
	configHTTPTimeout  int
	configLogLevel     string
)

// Config 配置结构
type Config struct {
	// 服务器配置
	Port int    `yaml:"port" json:"port"`
	IP   string `yaml:"ip" json:"ip"`
	API  bool   `yaml:"api" json:"api"`

	// 缓存配置
	GroupName    string `yaml:"group_name" json:"group_name"`
	CacheBytes   int64  `yaml:"cache_bytes" json:"cache_bytes"` // 缓存大小（字节）
	EnableGetter bool   `yaml:"enable_getter" json:"enable_getter"`

	// etcd 配置
	EtcdEndpoints   []string `yaml:"etcd_endpoints" json:"etcd_endpoints"`
	EtcdDialTimeout int      `yaml:"etcd_dial_timeout" json:"etcd_dial_timeout"` // etcd 连接超时（秒）

	// 服务发现配置
	NodeID            string `yaml:"node_id" json:"node_id"`                       // 节点ID（留空自动生成）
	LeaseTTL          int    `yaml:"lease_ttl" json:"lease_ttl"`                   // 租约TTL（秒）
	HeartbeatInterval int    `yaml:"heartbeat_interval" json:"heartbeat_interval"` // 心跳间隔（秒）

	// HTTP 客户端配置
	HTTPTimeout int `yaml:"http_timeout" json:"http_timeout"` // HTTP 请求超时（秒）

	// 日志配置
	LogLevel string `yaml:"log_level" json:"log_level"` // 日志级别：debug, info, warn, error
}

func createGroup(groupName string, enableGetter bool, cacheBytes int64) (*jincache.Group, string) {
	var getter jincache.Getter
	if enableGetter {
		getter = jincache.GetterFunc(
			func(key string) ([]byte, error) {
				log.Printf("[SlowDB] Loading key: %s", key)
				if v, ok := db[key]; ok {
					return []byte(v), nil
				}
				return nil, fmt.Errorf("%s not exist", key)
			})
	}
	return jincache.NewGroup(groupName, cacheBytes, getter), groupName
}

func startCacheServer(config *Config, jin *jincache.Group, groupName string) {
	// 构建节点地址
	nodeAddr := fmt.Sprintf("http://%s:%d", config.IP, config.Port)
	nodeID := config.NodeID

	// 创建服务发现，指定group名称
	sd, err := discovery.NewServiceDiscovery(config.EtcdEndpoints, nodeID, nodeAddr, groupName)
	if err != nil {
		log.Fatalf("[Main] Failed to create service discovery: %v", err)
	}

	// 注册服务
	if err := sd.Register(); err != nil {
		log.Fatalf("[Main] Failed to register service: %v", err)
	}

	// 创建增强的HTTP池
	peers := jincache.NewEnhancedHTTPPool(nodeAddr, sd, jin)
	jin.RegisterPeers(peers)

	log.Printf("[Main] Starting cache server...")
	log.Printf("[Main] Node ID: %s", nodeID)
	log.Printf("[Main] Address: %s", nodeAddr)
	log.Printf("[Main] Group: %s", groupName)
	log.Printf("[Main] Cache size: %d bytes", config.CacheBytes)
	log.Printf("[Main] Etcd endpoints: %v", config.EtcdEndpoints)

	// 设置优雅关闭
	go handleGracefulShutdown(sd, peers)

	// 启动HTTP服务
	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("[Main] ✓ Cache server started successfully on %s", addr)
	log.Fatal(http.ListenAndServe(addr, peers))
}

func startAPIServer(apiPort int, jin *jincache.Group, etcdEndpoints []string, groupName string) {
	// 创建缓存客户端，基于 etcd 和 group 名称
	cacheClient, err := client.NewClient(etcdEndpoints, groupName)
	if err != nil {
		log.Fatalf("[Main] Failed to create cache client: %v", err)
	}
	defer cacheClient.Close()

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

		value, err := cacheClient.Get(jin.Name(), key)
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

		if err := cacheClient.Set(jin.Name(), key, value); err != nil {
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

		if err := cacheClient.Delete(jin.Name(), key); err != nil {
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

		stats, err := cacheClient.GetStats(jin.Name())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	apiPortStr := strconv.Itoa(apiPort)
	log.Printf("[Main] Starting API server...")
	log.Printf("[Main] Port: %s", apiPortStr)
	log.Printf("[Main] Group: %s", groupName)
	log.Printf("[Main] Etcd endpoints: %v", etcdEndpoints)
	log.Println("[Main] Available endpoints:")
	log.Println("[Main]   GET    /api?key=<key>          - Get value")
	log.Println("[Main]   POST   /api/set?key=<key>      - Set value")
	log.Println("[Main]   DELETE /api/delete?key=<key>   - Delete value")
	log.Println("[Main]   GET    /api/stats              - Get statistics")
	log.Printf("[Main] ✓ API server started successfully on port %s", apiPortStr)
	log.Fatal(http.ListenAndServe(":"+apiPortStr, nil))
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

	log.Println("[Main] ✓ Shutdown complete")
	os.Exit(0)
}

// loadConfigFromFile 从 YAML 文件加载配置
func loadConfigFromFile(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %v", err)
	}

	// 设置默认值
	setConfigDefaults(&config)

	return &config, nil
}

// setConfigDefaults 设置配置的默认值
func setConfigDefaults(config *Config) {
	if config.Port == 0 {
		config.Port = 8001
	}
	if config.IP == "" {
		config.IP = "localhost"
	}
	if config.GroupName == "" {
		config.GroupName = "default-group"
	}
	if config.CacheBytes == 0 {
		config.CacheBytes = 2 << 10 // 2KB
	}
	if len(config.EtcdEndpoints) == 0 {
		config.EtcdEndpoints = []string{"http://192.168.59.132:2379"}
	}
	if config.EtcdDialTimeout == 0 {
		config.EtcdDialTimeout = 5
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 30
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = 10
	}
	if config.HTTPTimeout == 0 {
		config.HTTPTimeout = 5
	}
	if config.LogLevel == "" {
		config.LogLevel = "info"
	}
}

// parseConfig 解析配置（配置文件 + 命令行参数）
func parseConfig() *Config {
	var configFile string
	var etcdEndpoints string

	// 定义命令行参数
	flag.StringVar(&configFile, "config", "", "path to config file (yaml)")
	flag.IntVar(&configPort, "port", 0, "jincache server port")
	flag.StringVar(&configIP, "ip", "", "jincache server ip")
	flag.BoolVar(&configAPI, "api", false, "Start a api server?")
	flag.StringVar(&configGroupName, "groupname", "", "cache group name")
	flag.BoolVar(&configEnableGetter, "enableGetter", false, "enable getter for loading data from source")
	flag.StringVar(&etcdEndpoints, "etcd", "", "etcd endpoints (comma separated)")
	flag.IntVar(&configCacheBytes, "cacheBytes", 0, "cache size in bytes")
	flag.StringVar(&configNodeID, "nodeID", "", "node ID")
	flag.IntVar(&configLeaseTTL, "leaseTTL", 0, "lease TTL in seconds")
	flag.IntVar(&configHTTPTimeout, "httpTimeout", 0, "HTTP timeout in seconds")
	flag.StringVar(&configLogLevel, "logLevel", "", "log level (debug, info, warn, error)")

	flag.Parse()

	// 从配置文件加载
	var config *Config
	if configFile != "" {
		var err error
		config, err = loadConfigFromFile(configFile)
		if err != nil {
			log.Fatalf("Failed to load config file: %v", err)
		}
		log.Printf("[Config] Loaded config from file: %s", configFile)
	} else {
		config = &Config{}
		setConfigDefaults(config)
	}

	// 命令行参数覆盖配置文件
	if configPort != 0 {
		config.Port = configPort
	}
	if configIP != "" {
		config.IP = configIP
	}
	if configAPI {
		config.API = configAPI
	}
	if configGroupName != "" {
		config.GroupName = configGroupName
	}
	if configEnableGetter {
		config.EnableGetter = configEnableGetter
	}
	if etcdEndpoints != "" {
		// 解析逗号分隔的 etcd 端点
		endpoints := splitEndpoints(etcdEndpoints)
		// 确保每个端点都有 http:// 前缀
		config.EtcdEndpoints = normalizeEndpoints(endpoints)
	}
	if configCacheBytes != 0 {
		config.CacheBytes = int64(configCacheBytes)
	}
	if configNodeID != "" {
		config.NodeID = configNodeID
	}
	if configLeaseTTL != 0 {
		config.LeaseTTL = configLeaseTTL
	}
	if configHTTPTimeout != 0 {
		config.HTTPTimeout = configHTTPTimeout
	}
	if configLogLevel != "" {
		config.LogLevel = configLogLevel
	}

	// 生成默认节点ID
	if config.NodeID == "" {
		config.NodeID = fmt.Sprintf("node-%d", config.Port)
	}

	return config
}

// splitEndpoints 分割逗号分隔的端点列表
func splitEndpoints(endpoints string) []string {
	if endpoints == "" {
		return nil
	}
	var result []string
	for _, ep := range strings.Split(endpoints, ",") {
		ep = strings.TrimSpace(ep)
		if ep != "" {
			result = append(result, ep)
		}
	}
	return result
}

// normalizeEndpoints 规范化端点列表，确保每个端点都有 http:// 或 https:// 前缀
func normalizeEndpoints(endpoints []string) []string {
	if endpoints == nil {
		return nil
	}
	result := make([]string, len(endpoints))
	for i, ep := range endpoints {
		if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
			result[i] = "http://" + ep
		} else {
			result[i] = ep
		}
	}
	return result
}

func main() {
	config := parseConfig()

	// 输出配置信息
	log.Println("========================================")
	log.Println("JinCache Distributed Cache System")
	log.Println("========================================")
	log.Printf("[Config] Port: %d", config.Port)
	log.Printf("[Config] IP: %s", config.IP)
	log.Printf("[Config] API: %v", config.API)
	log.Printf("[Config] Group: %s", config.GroupName)
	log.Printf("[Config] Cache size: %d bytes", config.CacheBytes)
	log.Printf("[Config] Enable getter: %v", config.EnableGetter)
	log.Printf("[Config] Etcd: %v", config.EtcdEndpoints)
	log.Printf("[Config] Node ID: %s", config.NodeID)
	log.Printf("[Config] Lease TTL: %d seconds", config.LeaseTTL)
	log.Printf("[Config] HTTP timeout: %d seconds", config.HTTPTimeout)
	log.Printf("[Config] Log level: %s", config.LogLevel)
	log.Println("========================================")

	// 创建缓存组
	jin, groupName := createGroup(config.GroupName, config.EnableGetter, config.CacheBytes)

	// 启动API服务器（如果需要）
	if config.API {
		startAPIServer(config.Port, jin, config.EtcdEndpoints, config.GroupName)
		return
	}

	// 启动缓存服务器
	startCacheServer(config, jin, groupName)
}
