package jincache

import (
	"context"
	"distributed-cache-demo/jincache/consistenthash"
	"distributed-cache-demo/jincache/discovery"
	"distributed-cache-demo/jincache/migration"
	pb "distributed-cache-demo/jincache/jincachepb"
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
	defaultBasePath = "/_geecache/"
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
	
	// 新增字段
	discovery  *discovery.ServiceDiscovery
	migrator   *migration.DataMigrator
	localGroup *Group
	stopCh     chan struct{}
}

// NewHTTPPool initializes an HTTP pool of peers.
func NewHTTPPool(self string) *HTTPPool {
	return &HTTPPool{
		self:     self,
		basePath: defaultBasePath,
		stopCh:   make(chan struct{}),
	}
}

// NewEnhancedHTTPPool creates an enhanced HTTP pool with service discovery
func NewEnhancedHTTPPool(self string, discovery *discovery.ServiceDiscovery, group *Group) *HTTPPool {
	pool := &HTTPPool{
		self:       self,
		basePath:   defaultBasePath,
		discovery:  discovery,
		localGroup: group,
		stopCh:     make(chan struct{}),
		migrator:   migration.NewDataMigrator(&GroupAdapter{group: group}, 100, 30*time.Second),
		// 初始化空的peers，避免空指针
		peers:      consistenthash.New(defaultReplicas, nil),
		httpGetters: make(map[string]*httpGetter),
	}
	
	// 启动节点监听
	go pool.watchNodes()
	
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

	log.Printf("[ServeHTTP] Processing request for group: %s, key: %s", groupName, key)

	group := GetGroup(groupName)
	if group == nil {
		log.Printf("[ServeHTTP] No such group: %s", groupName)
		http.Error(w, "no such group: "+groupName, http.StatusNotFound)
		return
	}

	// 对于来自其他节点的请求，使用GetRemote方法避免循环转发
	log.Printf("[ServeHTTP] Remote request, using Group.GetRemote")
	
	// 使用GetRemote方法，它会检查缓存但不会转发给其他节点
	view, err := group.GetRemote(key)
	if err != nil {
		log.Printf("[ServeHTTP] Failed to get key %s remotely: %v", key, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

type httpGetter struct {
	baseURL string
}

func (h *httpGetter) Get(in *pb.Request, out *pb.Response) error {
	u := fmt.Sprintf(
		"%v%v/%v",
		h.baseURL,
		url.QueryEscape(in.GetGroup()),
		url.QueryEscape(in.GetKey()),
	)
	
	log.Printf("[httpGetter] Making request to: %s", u)
	
	res, err := http.Get(u)
	if err != nil {
		log.Printf("[httpGetter] Request failed: %v", err)
		return err
	}
	defer res.Body.Close()

	log.Printf("[httpGetter] Response status: %s", res.Status)

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", res.Status)
	}

	bytes, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %v", err)
	}

	if err = proto.Unmarshal(bytes, out); err != nil {
		return fmt.Errorf("decoding response body: %v", err)
	}

	log.Printf("[httpGetter] Successfully got value for key: %s", in.GetKey())
	return nil
}

var _ PeerGetter = (*httpGetter)(nil)

// Set updates the pool's list of peers.
func (p *HTTPPool) Set(peers ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peers = consistenthash.New(defaultReplicas, nil)
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
	
	// 如果只有一个节点（自己），不选择peer
	nodes := p.peers.GetNodes()
	if len(nodes) <= 1 {
		return nil, false
	}
	
	// 获取一致性哈希选择的节点
	selectedPeer := p.peers.Get(key)
	
	// 如果选择的是自己，尝试选择下一个节点
	if selectedPeer == p.self {
		// 简单处理：选择列表中的第一个其他节点
		for _, node := range nodes {
			if node != p.self {
				selectedPeer = node
				break
			}
		}
	}
	
	// 如果最终选择的不是自己，且有对应的getter
	if selectedPeer != "" && selectedPeer != p.self {
		p.Log("Pick peer %s", selectedPeer)
		if getter, exists := p.httpGetters[selectedPeer]; exists {
			return getter, true
		}
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

	// 更新节点列表
	p.peers = consistenthash.New(defaultReplicas, nil)
	if len(activeAddrs) > 0 {
		p.peers.Add(activeAddrs...)
		log.Printf("[HTTPPool] Updated hash ring with nodes: %v", activeAddrs)
	}
	
	p.httpGetters = make(map[string]*httpGetter, len(activeAddrs))
	for _, peer := range activeAddrs {
		p.httpGetters[peer] = &httpGetter{baseURL: peer + p.basePath}
		log.Printf("[HTTPPool] Created HTTP getter for peer: %s", peer)
	}

	// 暂时禁用数据迁移功能
	// TODO: 实现完整的数据迁移功能
	// if oldPeers != nil && len(activeAddrs) > 0 {
	//     go p.triggerMigration(oldPeers, p.peers)
	// }
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

// UpdateNodes 手动更新节点列表
func (p *HTTPPool) UpdateNodes(addrs []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.peers = consistenthash.New(defaultReplicas, nil)
	p.peers.Add(addrs...)
	
	p.httpGetters = make(map[string]*httpGetter, len(addrs))
	for _, peer := range addrs {
		p.httpGetters[peer] = &httpGetter{baseURL: peer + p.basePath}
	}
}

// GetHealthyNodes 获取健康节点列表
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
