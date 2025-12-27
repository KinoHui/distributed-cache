package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"go.etcd.io/etcd/client/v3"
)

// NodeInfo 节点信息
type NodeInfo struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Group    string `json:"group"`
	Status   string `json:"status"`
	LastSeen int64  `json:"last_seen"`
}

// ServiceDiscovery 服务发现
type ServiceDiscovery struct {
	client     *clientv3.Client
	nodeID     string
	nodeAddr   string
	groupName  string
	leaseID    clientv3.LeaseID
	ctx        context.Context
	cancel     context.CancelFunc
	stopCh     chan struct{}
}

// NewServiceDiscovery 创建服务发现实例
func NewServiceDiscovery(etcdEndpoints []string, nodeID, nodeAddr, groupName string) (*ServiceDiscovery, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdEndpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to etcd: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &ServiceDiscovery{
		client:    client,
		nodeID:    nodeID,
		nodeAddr:  nodeAddr,
		groupName: groupName,
		ctx:       ctx,
		cancel:    cancel,
		stopCh:    make(chan struct{}),
	}, nil
}

// Register 注册服务
func (sd *ServiceDiscovery) Register() error {
	// 创建租约，30秒TTL
	lease, err := sd.client.Grant(sd.ctx, 30)
	if err != nil {
		return fmt.Errorf("failed to create lease: %v", err)
	}
	sd.leaseID = lease.ID

	// 创建节点信息
	nodeInfo := NodeInfo{
		ID:       sd.nodeID,
		Address:  sd.nodeAddr,
		Group:    sd.groupName,
		Status:   "active",
		LastSeen: time.Now().Unix(),
	}

	// 序列化节点信息
	data, err := json.Marshal(nodeInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal node info: %v", err)
	}

	// 注册节点到etcd，key格式为 /jincache/groups/{group}/nodes/{nodeID}
	key := fmt.Sprintf("/jincache/groups/%s/nodes/%s", sd.groupName, sd.nodeID)
	_, err = sd.client.Put(sd.ctx, key, string(data), clientv3.WithLease(sd.leaseID))
	if err != nil {
		return fmt.Errorf("failed to register node: %v", err)
	}

	log.Printf("[Discovery] Registered node %s (group: %s) at %s", sd.nodeID, sd.groupName, sd.nodeAddr)

	// 启动心跳续约
	go sd.keepAlive()

	// 启动心跳更新
	go sd.updateHeartbeat()

	return nil
}

// keepAlive 保持租约活跃
func (sd *ServiceDiscovery) keepAlive() {
	kaCh, kaErr := sd.client.KeepAlive(sd.ctx, sd.leaseID)
	if kaErr != nil {
		log.Printf("[Discovery] Failed to keep alive: %v", kaErr)
		return
	}

	for {
		select {
		case <-sd.ctx.Done():
			return
		case ka := <-kaCh:
			if ka == nil {
				log.Printf("[Discovery] Keep alive channel closed")
				return
			}
			log.Printf("[Discovery] Keep alive response: TTL=%d", ka.TTL)
		}
	}
}

// updateHeartbeat 定期更新心跳时间
func (sd *ServiceDiscovery) updateHeartbeat() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sd.ctx.Done():
			return
		case <-ticker.C:
			sd.updateNodeInfo()
		}
	}
}

// updateNodeInfo 更新节点信息
func (sd *ServiceDiscovery) updateNodeInfo() {
	nodeInfo := NodeInfo{
		ID:       sd.nodeID,
		Address:  sd.nodeAddr,
		Group:    sd.groupName,
		Status:   "active",
		LastSeen: time.Now().Unix(),
	}

	data, err := json.Marshal(nodeInfo)
	if err != nil {
		log.Printf("[Discovery] Failed to marshal node info: %v", err)
		return
	}

	key := fmt.Sprintf("/jincache/groups/%s/nodes/%s", sd.groupName, sd.nodeID)
	_, err = sd.client.Put(sd.ctx, key, string(data), clientv3.WithLease(sd.leaseID))
	if err != nil {
		log.Printf("[Discovery] Failed to update node info: %v", err)
	}
}

// GetNodes 获取所有活跃节点（同一group）
func (sd *ServiceDiscovery) GetNodes() ([]NodeInfo, error) {
	// 只获取同一group的节点
	prefix := fmt.Sprintf("/jincache/groups/%s/nodes/", sd.groupName)
	resp, err := sd.client.Get(sd.ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes: %v", err)
	}

	var nodes []NodeInfo
	for _, kv := range resp.Kvs {
		var nodeInfo NodeInfo
		if err := json.Unmarshal(kv.Value, &nodeInfo); err != nil {
			log.Printf("[Discovery] Failed to unmarshal node info: %v", err)
			continue
		}
		nodes = append(nodes, nodeInfo)
	}

	return nodes, nil
}

// WatchNodes 监听节点变化（同一group）
func (sd *ServiceDiscovery) WatchNodes() chan []NodeInfo {
	nodesCh := make(chan []NodeInfo, 1)

	go func() {
		defer close(nodesCh)

		// 只监听同一group的节点
		prefix := fmt.Sprintf("/jincache/groups/%s/nodes/", sd.groupName)
		watchCh := sd.client.Watch(sd.ctx, prefix, clientv3.WithPrefix())

		for {
			select {
			case <-sd.ctx.Done():
				return
			case watchResp := <-watchCh:
				if watchResp.Err() != nil {
					log.Printf("[Discovery] Watch error: %v", watchResp.Err())
					continue
				}

				// 获取最新节点列表
				nodes, err := sd.GetNodes()
				if err != nil {
					log.Printf("[Discovery] Failed to get nodes after watch event: %v", err)
					continue
				}

				select {
				case nodesCh <- nodes:
				default:
					// 非阻塞发送，避免阻塞
				}
			}
		}
	}()

	return nodesCh
}

// Deregister 注销服务
func (sd *ServiceDiscovery) Deregister() error {
	// 取消租约
	_, err := sd.client.Revoke(sd.ctx, sd.leaseID)
	if err != nil {
		log.Printf("[Discovery] Failed to revoke lease: %v", err)
	}

	// 删除节点信息
	key := fmt.Sprintf("/jincache/groups/%s/nodes/%s", sd.groupName, sd.nodeID)
	_, err = sd.client.Delete(sd.ctx, key)
	if err != nil {
		log.Printf("[Discovery] Failed to delete node: %v", err)
	}

	log.Printf("[Discovery] Deregistered node %s (group: %s)", sd.nodeID, sd.groupName)
	return nil
}

// Close 关闭服务发现
func (sd *ServiceDiscovery) Close() error {
	sd.cancel()
	
	// 安全关闭stopCh
	select {
	case <-sd.stopCh:
		// channel已经关闭
	default:
		close(sd.stopCh)
	}
	
	err := sd.Deregister()
	if err != nil {
		return err
	}

	return sd.client.Close()
}