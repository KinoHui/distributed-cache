package jincache

import (
	"context"
	"distributed-cache-demo/jincache/consistenthash"
	"distributed-cache-demo/jincache/discovery"
	pb "distributed-cache-demo/jincache/jincachepb"
	"distributed-cache-demo/jincache/migration"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

const (
	defaultBasePath = "/_jincache/"
	defaultReplicas = 50
)

// HTTPPool implements PeerPicker for a pool of HTTP peers.
type HTTPPool struct {
	// this peer's base URL, e.g. "https://example.net:8000"
	self        string
	basePath    string
	mu          sync.Mutex // guards peers and httpGetters
	peers       *consistenthash.Map
	httpGetters map[string]*httpGetter // keyed by e.g. "http://10.0.0.2:8008"
	hashFunc    consistenthash.Hash

	// 新增字段
	discovery  *discovery.ServiceDiscovery
	migrator   *migration.DataMigrator
	localGroup *Group
	stopCh     chan struct{}

	// 容错相关字段
	nodeHealth     *nodeHealth
	requestTimeout time.Duration
	maxRetries     int
}

// nodeHealth 管理节点健康状态
type nodeHealth struct {
	mu           sync.RWMutex
	failures     map[string]int       // 失败次数
	lastFailure  map[string]time.Time // 最后失败时间
	blacklist    map[string]time.Time // 黑名单（节点地址 -> 加入时间）
	maxFailures  int                  // 最大失败次数
	blacklistTTL time.Duration        // 黑名单TTL
}

func newNodeHealth(maxFailures int, blacklistTTL time.Duration) *nodeHealth {
	return &nodeHealth{
		failures:     make(map[string]int),
		lastFailure:  make(map[string]time.Time),
		blacklist:    make(map[string]time.Time),
		maxFailures:  maxFailures,
		blacklistTTL: blacklistTTL,
	}
}

// recordFailure 记录节点失败
func (nh *nodeHealth) recordFailure(nodeAddr string) {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	nh.failures[nodeAddr]++
	nh.lastFailure[nodeAddr] = time.Now()

	// 超过最大失败次数，加入黑名单
	if nh.failures[nodeAddr] >= nh.maxFailures {
		nh.blacklist[nodeAddr] = time.Now()
		log.Printf("[NodeHealth] Node %s added to blacklist (failures: %d)",
			nodeAddr, nh.failures[nodeAddr])
	}
}

// recordSuccess 记录节点成功
func (nh *nodeHealth) recordSuccess(nodeAddr string) {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	// 重置失败计数
	if nh.failures[nodeAddr] > 0 {
		delete(nh.failures, nodeAddr)
		delete(nh.lastFailure, nodeAddr)
		log.Printf("[NodeHealth] Node %s recovered, resetting failure count", nodeAddr)
	}

	// 从黑名单中移除
	if _, exists := nh.blacklist[nodeAddr]; exists {
		delete(nh.blacklist, nodeAddr)
		log.Printf("[NodeHealth] Node %s removed from blacklist", nodeAddr)
	}
}

// isBlacklisted 检查节点是否在黑名单中
func (nh *nodeHealth) isBlacklisted(nodeAddr string) bool {
	nh.mu.RLock()
	defer nh.mu.RUnlock()

	// 检查是否在黑名单中
	if _, exists := nh.blacklist[nodeAddr]; exists {
		// 检查黑名单是否过期
		if blacklistTime, ok := nh.blacklist[nodeAddr]; ok {
			if time.Since(blacklistTime) > nh.blacklistTTL {
				// 过期了，移除黑名单
				nh.mu.RUnlock()
				nh.mu.Lock()
				delete(nh.blacklist, nodeAddr)
				delete(nh.failures, nodeAddr)
				delete(nh.lastFailure, nodeAddr)
				nh.mu.Unlock()
				nh.mu.RLock()
				return false
			}
		}
		return true
	}

	return false
}

// cleanupBlacklist 清理过期的黑名单项
func (nh *nodeHealth) cleanupBlacklist() {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	now := time.Now()
	for nodeAddr, blacklistTime := range nh.blacklist {
		if now.Sub(blacklistTime) > nh.blacklistTTL {
			delete(nh.blacklist, nodeAddr)
			delete(nh.failures, nodeAddr)
			delete(nh.lastFailure, nodeAddr)
			log.Printf("[NodeHealth] Node %s removed from blacklist (expired)", nodeAddr)
		}
	}
}

// 未调用NewHTTPPool initializes an HTTP pool of peers.
func NewHTTPPool(self string) *HTTPPool {
	return &HTTPPool{
		self:     self,
		basePath: defaultBasePath,
		stopCh:   make(chan struct{}),
	}
}

// NewEnhancedHTTPPool creates an enhanced HTTP pool with service discovery
func NewEnhancedHTTPPool(self string, discovery *discovery.ServiceDiscovery, group *Group, hashFuncs ...consistenthash.Hash) *HTTPPool {
	var hashFunc consistenthash.Hash
	if len(hashFuncs) > 0 {
		hashFunc = hashFuncs[0]
	}
	pool := &HTTPPool{
		self:       self,
		basePath:   defaultBasePath,
		discovery:  discovery,
		localGroup: group,
		stopCh:     make(chan struct{}),
		migrator:   migration.NewDataMigrator(&GroupAdapter{group: group}, 100, 30*time.Second),
		// 初始化空的peers，避免空指针
		peers:       consistenthash.New(defaultReplicas, hashFunc),
		hashFunc:    hashFunc,
		httpGetters: make(map[string]*httpGetter),
		// 容错配置
		nodeHealth:     newNodeHealth(3, 5*time.Minute), // 3次失败后加入黑名单，5分钟后恢复
		requestTimeout: 3 * time.Second,                 // 3秒超时
		maxRetries:     2,                               // 最多重试2次
	}

	// 启动节点监听
	go pool.watchNodes()

	// 启动黑名单清理协程
	go pool.cleanupBlacklistRoutine()

	return pool
}

// Log info with server name
func (p *HTTPPool) Log(format string, v ...interface{}) {
	log.Printf("[Server %s] %s", p.self, fmt.Sprintf(format, v...))
}

// ServeHTTP handle all http requests
func (p *HTTPPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, p.basePath) {
		panic("HTTPPool serving unexpected path: " + r.URL.Path)
	}
	p.Log("%s %s", r.Method, r.URL.Path)

	// /<basepath>/<groupname>/<key> required
	parts := strings.SplitN(r.URL.Path[len(p.basePath):], "/", 2)
	if len(parts) != 2 {
		log.Printf("[ServeHTTP] Invalid request path: %s", r.URL.Path)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	groupName := parts[0]
	key := parts[1]

	// 处理统计信息请求
	if key == "_stats" {
		p.handleStatsRequest(w, r, groupName)
		return
	}

	log.Printf("[ServeHTTP] Processing request for group: %s, key: %s", groupName, key)

	group := GetGroup(groupName)
	if group == nil {
		log.Printf("[ServeHTTP] No such group: %s", groupName)
		http.Error(w, "no such group: "+groupName, http.StatusNotFound)
		return
	}

	// 根据HTTP方法分发
	switch r.Method {
	case "GET":
		p.handleGetRequest(w, r, group, key)
	case "PUT":
		p.handleSetRequest(w, r, group, key)
	case "DELETE":
		p.handleDeleteRequest(w, r, group, key)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetRequest 处理GET请求
func (p *HTTPPool) handleGetRequest(w http.ResponseWriter, r *http.Request, group *Group, key string) {
	// 对于来自其他节点的请求，使用GetRemote方法避免循环转发
	log.Printf("[ServeHTTP] Remote GET request, using Group.GetRemote")

	view, err := group.Get(key)
	if err != nil {
		log.Printf("[ServeHTTP] Failed to get key %s: %v", key, err)
		// 判断是否是key不存在的错误
		if err == KeyNotFoundError {
			http.Error(w, "key not found: "+key, http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	body, err := proto.Marshal(&pb.Response{Value: view.ByteSlice()})
	if err != nil {
		log.Printf("[ServeHTTP] Failed to marshal response: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[ServeHTTP] Successfully returning value for key: %s", key)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(body)
}

// handleSetRequest 处理PUT请求
func (p *HTTPPool) handleSetRequest(w http.ResponseWriter, r *http.Request, group *Group, key string) {
	log.Printf("[ServeHTTP] Processing SET request for key: %s", key)

	// 读取请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[ServeHTTP] Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 解析protobuf请求
	var req pb.Request
	if err = proto.Unmarshal(body, &req); err != nil {
		log.Printf("[ServeHTTP] Failed to unmarshal request: %v", err)
		http.Error(w, "failed to unmarshal request", http.StatusBadRequest)
		return
	}

	// 设置缓存值
	group.PopulateCache(key, ByteView{b: req.Value})

	log.Printf("[ServeHTTP] Successfully set value for key: %s", key)
	w.WriteHeader(http.StatusOK)
}

// handleDeleteRequest 处理DELETE请求
func (p *HTTPPool) handleDeleteRequest(w http.ResponseWriter, r *http.Request, group *Group, key string) {
	log.Printf("[ServeHTTP] Processing DELETE request for key: %s", key)

	// 删除缓存值
	group.Remove(key)

	log.Printf("[ServeHTTP] Successfully deleted key: %s", key)
	w.WriteHeader(http.StatusOK)
}

// handleStatsRequest 处理统计信息请求
func (p *HTTPPool) handleStatsRequest(w http.ResponseWriter, r *http.Request, groupName string) {
	group := GetGroup(groupName)
	if group == nil {
		log.Printf("[ServeHTTP] No such group: %s", groupName)
		http.Error(w, "no such group: "+groupName, http.StatusNotFound)
		return
	}

	keys := group.GetKeys()

	// 计算缓存大小（简化实现，实际可能需要更精确的计算）
	bytesUsed := int64(0)
	for _, key := range keys {
		if view, ok := group.mainCache.get(key); ok {
			bytesUsed += int64(len(view.b))
		}
	}

	stats := map[string]interface{}{
		"keys_count": len(keys),
		"bytes_used": bytesUsed,
	}

	body, err := json.Marshal(stats)
	if err != nil {
		log.Printf("[ServeHTTP] Failed to marshal stats: %v", err)
		http.Error(w, "failed to marshal stats", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

type httpGetter struct {
	baseURL    string
	httpClient *http.Client
	maxRetries int
	timeout    time.Duration
	pool       *HTTPPool // 引用pool以记录节点健康状态
}

func newHTTPGetter(baseURL string, timeout time.Duration, maxRetries int) *httpGetter {
	return &httpGetter{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		maxRetries: maxRetries,
		timeout:    timeout,
	}
}

func (h *httpGetter) Get(in *pb.Request, out *pb.Response) error {
	u := fmt.Sprintf(
		"%v%v/%v",
		h.baseURL,
		url.QueryEscape(in.GetGroup()),
		url.QueryEscape(in.GetKey()),
	)

	var lastErr error

	// 重试机制
	for attempt := 0; attempt <= h.maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避
			backoff := time.Duration(1<<uint(attempt-1)) * 100 * time.Millisecond
			log.Printf("[httpGetter] Retry attempt %d after %v", attempt, backoff)
			time.Sleep(backoff)
		}

		log.Printf("[httpGetter] Making request to: %s (attempt %d)", u, attempt+1)

		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			lastErr = err
			log.Printf("[httpGetter] Failed to create request: %v", err)
			continue
		}

		res, err := h.httpClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[httpGetter] Request failed: %v", err)
			continue
		}

		log.Printf("[httpGetter] Response status: %s", res.Status)

		if res.StatusCode == http.StatusNotFound {
			// 404表示key不存在，这是正常响应，不应该算作失败
			res.Body.Close()
			log.Printf("[httpGetter] Key not found: %s", in.GetKey())
			// 记录成功（404也是成功的响应）
			if h.pool != nil {
				nodeAddr := strings.TrimSuffix(h.baseURL, h.pool.basePath)
				h.pool.RecordPeerSuccess(nodeAddr)
			}
			return KeyNotFoundError
		}

		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			lastErr = fmt.Errorf("server returned: %v", res.Status)
			log.Printf("[httpGetter] Server error: %v", lastErr)
			continue
		}

		bytes, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("reading response body: %v", err)
			log.Printf("[httpGetter] Failed to read response: %v", lastErr)
			continue
		}

		if err = proto.Unmarshal(bytes, out); err != nil {
			lastErr = fmt.Errorf("decoding response body: %v", err)
			log.Printf("[httpGetter] Failed to decode response: %v", lastErr)
			continue
		}

		log.Printf("[httpGetter] Successfully got value for key: %s", in.GetKey())

		// 记录成功
		if h.pool != nil {
			// 从baseURL中提取节点地址（移除basePath）
			nodeAddr := strings.TrimSuffix(h.baseURL, h.pool.basePath)
			h.pool.RecordPeerSuccess(nodeAddr)
		}

		return nil
	}

	// 所有重试都失败，记录失败
	if h.pool != nil {
		nodeAddr := strings.TrimSuffix(h.baseURL, h.pool.basePath)
		h.pool.RecordPeerFailure(nodeAddr)
	}

	return lastErr
}

var _ PeerGetter = (*httpGetter)(nil)

// 未调用Set updates the pool's list of peers.
func (p *HTTPPool) Set(peers ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peers = consistenthash.New(defaultReplicas, p.hashFunc)
	p.peers.Add(peers...)
	p.httpGetters = make(map[string]*httpGetter, len(peers))
	for _, peer := range peers {
		p.httpGetters[peer] = &httpGetter{baseURL: peer + p.basePath}
	}
}

// PickPeer picks a peer according to key
func (p *HTTPPool) PickPeer(key string) (PeerGetter, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查peers是否为空
	if p.peers == nil || p.peers.IsEmpty() {
		return nil, false
	}

	// 获取一致性哈希选择的节点
	selectedPeer := p.peers.Get(key)
	if selectedPeer == "" {
		return nil, false
	}

	// 如果选择的是当前节点，返回false表示不需要转发
	if selectedPeer == p.self {
		p.Log("Key %s routes to self", key)
		return nil, false
	}

	// 检查节点是否在黑名单中
	if p.nodeHealth.isBlacklisted(selectedPeer) {
		p.Log("Node %s is blacklisted, skipping", selectedPeer)
		// 尝试选择其他健康节点
		nodes := p.peers.GetNodes()
		for _, node := range nodes {
			if node != p.self && node != selectedPeer && !p.nodeHealth.isBlacklisted(node) {
				p.Log("Picking alternative peer %s instead of blacklisted %s", node, selectedPeer)
				if getter, exists := p.httpGetters[node]; exists {
					return getter, true
				}
			}
		}
		// 没有可用的健康节点，返回nil，触发降级
		p.Log("No healthy peers available, will fallback to local")
		return nil, false
	}

	// 选择的是其他节点，返回对应的getter
	p.Log("Pick peer %s for key %s", selectedPeer, key)
	if getter, exists := p.httpGetters[selectedPeer]; exists {
		return getter, true
	}

	return nil, false
}

// watchNodes 监听节点变化
func (p *HTTPPool) watchNodes() {
	if p.discovery == nil {
		return
	}

	nodesCh := p.discovery.WatchNodes()

	for {
		select {
		case <-p.stopCh:
			return
		case nodes, ok := <-nodesCh:
			if !ok {
				return
			}
			p.handleNodeChange(nodes)
		}
	}
}

// handleNodeChange 处理节点变化
func (p *HTTPPool) handleNodeChange(nodes []discovery.NodeInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 提取活跃节点地址，包括自己
	var activeAddrs []string
	for _, node := range nodes {
		if node.Status == "active" {
			activeAddrs = append(activeAddrs, node.Address)
		}
	}

	// 获取当前节点
	var currentAddrs []string
	if p.peers != nil {
		currentAddrs = p.peers.GetNodes()
	}

	// 检查是否有变化
	if p.nodeListsEqual(currentAddrs, activeAddrs) {
		return
	}

	log.Printf("[HTTPPool] Node list changed, updating. Current: %v, New: %v",
		currentAddrs, activeAddrs)
	log.Printf("[HTTPPool] Self node: %s", p.self)

	// 保存旧的哈希环用于迁移
	oldPeers := p.peers

	// 更新节点列表
	p.peers = consistenthash.New(defaultReplicas, p.hashFunc)
	if len(activeAddrs) > 0 {
		p.peers.Add(activeAddrs...)
		log.Printf("[HTTPPool] Updated hash ring with nodes: %v", activeAddrs)
	}

	p.httpGetters = make(map[string]*httpGetter, len(activeAddrs))
	for _, peer := range activeAddrs {
		// 使用新的构造函数创建带容错功能的getter
		getter := newHTTPGetter(peer+p.basePath, p.requestTimeout, p.maxRetries)
		getter.pool = p // 设置pool引用以便记录节点健康状态
		p.httpGetters[peer] = getter
		log.Printf("[HTTPPool] Created HTTP getter for peer: %s (timeout: %v, retries: %d)",
			peer, p.requestTimeout, p.maxRetries)
	}

	// 触发数据迁移，清理不再属于当前节点的数据
	if oldPeers != nil && len(activeAddrs) > 0 {
		go p.cleanupStaleData(oldPeers, p.peers)
	}
}

// nodeListsEqual 检查两个节点列表是否相等
func (p *HTTPPool) nodeListsEqual(list1, list2 []string) bool {
	if len(list1) != len(list2) {
		return false
	}

	set1 := make(map[string]bool)
	set2 := make(map[string]bool)

	for _, addr := range list1 {
		set1[addr] = true
	}
	for _, addr := range list2 {
		set2[addr] = true
	}

	for addr := range set1 {
		if !set2[addr] {
			return false
		}
	}

	return true
}

// triggerMigration 触发数据迁移
func (p *HTTPPool) triggerMigration(oldPeers, newPeers *consistenthash.Map) {
	log.Printf("[HTTPPool] Triggering data migration")

	plan := newPeers.GetMigrationPlan(oldPeers)
	if plan == nil {
		log.Printf("[HTTPPool] No migration needed")
		return
	}

	for sourceNode, targetNodes := range plan {
		for _, targetNode := range targetNodes {
			keys, err := p.migrator.GetKeysToMigrate(sourceNode, targetNode)
			if err != nil {
				log.Printf("[HTTPPool] Failed to get keys to migrate: %v", err)
				continue
			}

			if len(keys) > 0 {
				migrationPlan := migration.MigrationPlan{
					SourceNode: sourceNode,
					TargetNode: targetNode,
					Keys:       keys,
				}

				if err := p.migrator.MigrateData(context.Background(), migrationPlan); err != nil {
					log.Printf("[HTTPPool] Failed to migrate data from %s to %s: %v",
						sourceNode, targetNode, err)
				}
			}
		}
	}
}

// cleanupStaleData 清理不再属于当前节点的数据
func (p *HTTPPool) cleanupStaleData(oldPeers, newPeers *consistenthash.Map) {
	log.Printf("[HTTPPool] Starting cleanup of stale data")

	if p.localGroup == nil {
		log.Printf("[HTTPPool] No local group, skipping cleanup")
		return
	}

	// 获取当前缓存中的所有key
	keys := p.localGroup.GetKeys()
	log.Printf("[HTTPPool] Found %d keys in cache", len(keys))

	cleanedCount := 0
	for _, key := range keys {
		// 使用新的哈希环判断key应该路由到哪个节点
		targetNode := newPeers.Get(key)

		// 如果key应该路由到其他节点，则从当前节点删除
		if targetNode != "" && targetNode != p.self {
			log.Printf("[HTTPPool] Removing stale key %s (should be on %s)", key, targetNode)
			p.localGroup.Remove(key)
			cleanedCount++
		}
	}

	log.Printf("[HTTPPool] Cleanup completed, removed %d stale keys", cleanedCount)
}

// cleanupBlacklistRoutine 定期清理黑名单
func (p *HTTPPool) cleanupBlacklistRoutine() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.nodeHealth.cleanupBlacklist()
		}
	}
}

// RecordPeerFailure 记录节点失败（供外部调用）
func (p *HTTPPool) RecordPeerFailure(peerAddr string) {
	p.nodeHealth.recordFailure(peerAddr)
}

// RecordPeerSuccess 记录节点成功（供外部调用）
func (p *HTTPPool) RecordPeerSuccess(peerAddr string) {
	p.nodeHealth.recordSuccess(peerAddr)
}

// 未调用UpdateNodes 手动更新节点列表
func (p *HTTPPool) UpdateNodes(addrs []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.peers = consistenthash.New(defaultReplicas, p.hashFunc)
	p.peers.Add(addrs...)

	p.httpGetters = make(map[string]*httpGetter, len(addrs))
	for _, peer := range addrs {
		p.httpGetters[peer] = &httpGetter{baseURL: peer + p.basePath}
	}
}

// 未调用GetHealthyNodes 获取健康节点列表
func (p *HTTPPool) GetHealthyNodes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.discovery == nil {
		return p.peers.GetNodes()
	}

	nodes, err := p.discovery.GetNodes()
	if err != nil {
		log.Printf("[HTTPPool] Failed to get nodes from discovery: %v", err)
		return p.peers.GetNodes()
	}

	var healthyNodes []string
	for _, node := range nodes {
		if node.Status == "active" {
			healthyNodes = append(healthyNodes, node.Address)
		}
	}

	return healthyNodes
}

// Close 关闭HTTPPool
func (p *HTTPPool) Close() {
	close(p.stopCh)
	if p.discovery != nil {
		p.discovery.Close()
	}
}

// GroupAdapter 适配器，将Group适配为migration.CacheGroup
type GroupAdapter struct {
	group *Group
}

func (ga *GroupAdapter) Name() string {
	return ga.group.name
}

func (ga *GroupAdapter) Get(key string) (interface{}, error) {
	return ga.group.Get(key)
}

func (ga *GroupAdapter) PopulateCache(key string, value interface{}) {
	if byteView, ok := value.(ByteView); ok {
		ga.group.PopulateCache(key, byteView)
	}
}

var _ PeerPicker = (*HTTPPool)(nil)
