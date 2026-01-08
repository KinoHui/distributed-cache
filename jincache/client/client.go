package client

import (
	"bytes"
	"context"
	"distributed-cache-demo/jincache/discovery"
	"distributed-cache-demo/jincache/jincachepb"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"
)

const (
	nodeCacheTTL = 30 * time.Second // 节点列表缓存过期时间
)

// Client 缓存客户端
type Client struct {
	etcdClient *clientv3.Client
	groupName  string
	nodes      []string     // 节点列表缓存
	nodeIndex  int          // 轮询索引
	mu         sync.RWMutex // 保护 node 列表
	lastUpdate time.Time    // 最后更新时间
	httpClient *http.Client
}

// NewClient 创建新的缓存客户端（基于 etcd 和 group）
func NewClient(etcdEndpoints []string, groupName string) (*Client, error) {
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdEndpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to etcd: %v", err)
	}

	return &Client{
		etcdClient: etcdClient,
		groupName:  groupName,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}, nil
}

// Deprecated: NewClientWithURL 已弃用，请使用 NewClient
func NewClientWithURL(baseURL string) *Client {
	return &Client{
		etcdClient: nil,
		groupName:  "",
		nodes:      []string{baseURL},
		nodeIndex:  0,
		lastUpdate: time.Now(),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// refreshNodes 从 etcd 刷新节点列表
func (c *Client) refreshNodes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prefix := fmt.Sprintf("/jincache/groups/%s/nodes/", c.groupName)
	resp, err := c.etcdClient.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("failed to get nodes from etcd: %v", err)
	}

	var nodes []string
	for _, kv := range resp.Kvs {
		var nodeInfo discovery.NodeInfo
		if err := json.Unmarshal(kv.Value, &nodeInfo); err != nil {
			log.Printf("[Client] Failed to unmarshal node info: %v", err)
			continue
		}
		nodes = append(nodes, nodeInfo.Address)
	}

	if len(nodes) == 0 {
		return fmt.Errorf("no available nodes for group %s", c.groupName)
	}

	c.mu.Lock()
	c.nodes = nodes
	c.nodeIndex = 0
	c.lastUpdate = time.Now()
	c.mu.Unlock()

	log.Printf("[Client] Refreshed node list for group %s: %v", c.groupName, nodes)
	return nil
}

// getNodes 获取节点列表，如果过期则刷新
func (c *Client) getNodes() ([]string, error) {
	c.mu.RLock()
	needRefresh := len(c.nodes) == 0 || time.Since(c.lastUpdate) > nodeCacheTTL
	c.mu.RUnlock()

	if needRefresh {
		if err := c.refreshNodes(); err != nil {
			return nil, err
		}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nodes, nil
}

// selectNode 使用轮询算法选择节点
func (c *Client) selectNode() (string, error) {
	nodes, err := c.getNodes()
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	node := nodes[c.nodeIndex]
	c.nodeIndex = (c.nodeIndex + 1) % len(nodes)
	return node, nil
}

// Get 获取缓存值
func (c *Client) Get(group, key string) ([]byte, error) {
	// 第一次尝试
	node, err := c.selectNode()
	if err != nil {
		return nil, err
	}

	value, err := c.doGet(node, group, key)
	if err == nil {
		return value, nil
	}

	log.Printf("[Client] First attempt failed: %v, retrying with next node", err)

	// 第二次尝试：重试下一个节点
	node, err = c.selectNode()
	if err != nil {
		return nil, err
	}

	value, err = c.doGet(node, group, key)
	if err == nil {
		return value, nil
	}

	log.Printf("[Client] Second attempt failed: %v, refreshing node list and retrying", err)

	// 第三次尝试：刷新节点列表后再次请求
	if err := c.refreshNodes(); err != nil {
		return nil, fmt.Errorf("failed to refresh nodes: %v", err)
	}

	node, err = c.selectNode()
	if err != nil {
		return nil, err
	}

	return c.doGet(node, group, key)
}

// doGet 执行实际的 GET 请求
func (c *Client) doGet(baseURL, group, key string) ([]byte, error) {
	u := fmt.Sprintf("%s/_jincache/%s/%s", baseURL, url.QueryEscape(group), url.QueryEscape(key))

	log.Printf("[Client] GET %s", u)

	resp, err := c.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("failed to get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned: %v", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %v", err)
	}

	var pbResp jincachepb.Response
	if err = proto.Unmarshal(body, &pbResp); err != nil {
		return nil, fmt.Errorf("decoding response body: %v", err)
	}

	return pbResp.Value, nil
}

// Set 设置缓存值
func (c *Client) Set(group, key string, value []byte) error {
	// 第一次尝试
	node, err := c.selectNode()
	if err != nil {
		return err
	}

	err = c.doSet(node, group, key, value)
	if err == nil {
		return nil
	}

	log.Printf("[Client] First attempt failed: %v, retrying with next node", err)

	// 第二次尝试：重试下一个节点
	node, err = c.selectNode()
	if err != nil {
		return err
	}

	err = c.doSet(node, group, key, value)
	if err == nil {
		return nil
	}

	log.Printf("[Client] Second attempt failed: %v, refreshing node list and retrying", err)

	// 第三次尝试：刷新节点列表后再次请求
	if err := c.refreshNodes(); err != nil {
		return fmt.Errorf("failed to refresh nodes: %v", err)
	}

	node, err = c.selectNode()
	if err != nil {
		return err
	}

	return c.doSet(node, group, key, value)
}

// doSet 执行实际的 SET 请求
func (c *Client) doSet(baseURL, group, key string, value []byte) error {
	u := fmt.Sprintf("%s/_jincache/%s/%s", baseURL, url.QueryEscape(group), url.QueryEscape(key))

	req := &jincachepb.Request{
		Group: group,
		Key:   key,
		Value: value,
	}

	body, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding request: %v", err)
	}

	log.Printf("[Client] PUT %s", u)

	httpReq, err := http.NewRequest("PUT", u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to set: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", resp.Status)
	}

	return nil
}

// Delete 删除缓存值
func (c *Client) Delete(group, key string) error {
	// 第一次尝试
	node, err := c.selectNode()
	if err != nil {
		return err
	}

	err = c.doDelete(node, group, key)
	if err == nil {
		return nil
	}

	log.Printf("[Client] First attempt failed: %v, retrying with next node", err)

	// 第二次尝试：重试下一个节点
	node, err = c.selectNode()
	if err != nil {
		return err
	}

	err = c.doDelete(node, group, key)
	if err == nil {
		return nil
	}

	log.Printf("[Client] Second attempt failed: %v, refreshing node list and retrying", err)

	// 第三次尝试：刷新节点列表后再次请求
	if err := c.refreshNodes(); err != nil {
		return fmt.Errorf("failed to refresh nodes: %v", err)
	}

	node, err = c.selectNode()
	if err != nil {
		return err
	}

	return c.doDelete(node, group, key)
}

// doDelete 执行实际的 DELETE 请求
func (c *Client) doDelete(baseURL, group, key string) error {
	u := fmt.Sprintf("%s/_jincache/%s/%s", baseURL, url.QueryEscape(group), url.QueryEscape(key))

	log.Printf("[Client] DELETE %s", u)

	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return fmt.Errorf("creating request: %v", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("key not found: %s", key)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", resp.Status)
	}

	return nil
}

// StatsResponse 统计信息响应
type StatsResponse struct {
	KeysCount int   `json:"keys_count"`
	BytesUsed int64 `json:"bytes_used"`
}

// GetStats 获取缓存统计信息（聚合所有节点的统计信息）
func (c *Client) GetStats(group string) (*StatsResponse, error) {
	// 刷新节点列表，确保获取最新的节点
	if err := c.refreshNodes(); err != nil {
		return nil, fmt.Errorf("failed to refresh nodes: %v", err)
	}

	c.mu.RLock()
	nodes := make([]string, len(c.nodes))
	copy(nodes, c.nodes)
	c.mu.RUnlock()

	if len(nodes) == 0 {
		return nil, fmt.Errorf("no available nodes for group %s", group)
	}

	log.Printf("[Client] Getting stats from %d nodes for group %s", len(nodes), group)

	// 并发获取所有节点的统计信息
	type nodeStats struct {
		node  string
		stats *StatsResponse
		err   error
	}

	statsCh := make(chan nodeStats, len(nodes))
	var wg sync.WaitGroup

	// 为每个节点启动一个 goroutine 获取统计信息
	for _, node := range nodes {
		wg.Add(1)
		go func(nodeAddr string) {
			defer wg.Done()
			stats, err := c.doGetStats(nodeAddr, group)
			statsCh <- nodeStats{node: nodeAddr, stats: stats, err: err}
		}(node)
	}

	// 等待所有请求完成
	go func() {
		wg.Wait()
		close(statsCh)
	}()

	// 聚合统计信息
	var totalKeysCount int
	var totalBytesUsed int64
	var successCount int
	var failedNodes []string

	for ns := range statsCh {
		if ns.err != nil {
			log.Printf("[Client] Failed to get stats from node %s: %v", ns.node, ns.err)
			failedNodes = append(failedNodes, ns.node)
			continue
		}
		totalKeysCount += ns.stats.KeysCount
		totalBytesUsed += ns.stats.BytesUsed
		successCount++
		log.Printf("[Client] Got stats from node %s: keys=%d, bytes=%d",
			ns.node, ns.stats.KeysCount, ns.stats.BytesUsed)
	}

	log.Printf("[Client] Stats aggregation completed: %d nodes succeeded, %d nodes failed",
		successCount, len(failedNodes))

	if successCount == 0 {
		return nil, fmt.Errorf("failed to get stats from any node, failed nodes: %v", failedNodes)
	}

	return &StatsResponse{
		KeysCount: totalKeysCount,
		BytesUsed: totalBytesUsed,
	}, nil
}

// doGetStats 执行实际的 GetStats 请求
func (c *Client) doGetStats(baseURL, group string) (*StatsResponse, error) {
	u := fmt.Sprintf("%s/_jincache/%s/_stats", baseURL, url.QueryEscape(group))

	log.Printf("[Client] GET %s", u)

	resp, err := c.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned: %v", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %v", err)
	}

	var stats StatsResponse
	if err = json.Unmarshal(body, &stats); err != nil {
		return nil, fmt.Errorf("decoding response body: %v", err)
	}

	return &stats, nil
}

// Close 关闭客户端
func (c *Client) Close() error {
	if c.etcdClient != nil {
		return c.etcdClient.Close()
	}
	return nil
}
