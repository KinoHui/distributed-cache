package consistenthash

import (
	"hash/crc32"
	"sort"
	"strconv"
)

// Hash maps bytes to uint32
type Hash func(data []byte) uint32

// Map constains all hashed keys
type Map struct {
	hash     Hash
	replicas int            // 虚拟节点倍数
	keys     []int          // Sorted key和节点名称的哈希环
	hashMap  map[int]string // 虚拟节点的hash映射到真实节点的名称
}

// New creates a Map instance
func New(replicas int, fn Hash) *Map {
	m := &Map{
		replicas: replicas,
		hash:     fn,
		hashMap:  make(map[int]string),
	}
	if m.hash == nil {
		m.hash = crc32.ChecksumIEEE
	}
	return m
}

// Add adds some keys to the hash.添加节点，参数keys为待添加节点的名称
func (m *Map) Add(keys ...string) {
	for _, key := range keys {
		for i := 0; i < m.replicas; i++ {
			hash := int(m.hash([]byte(strconv.Itoa(i) + key)))
			m.keys = append(m.keys, hash)
			m.hashMap[hash] = key
		}
	}
	sort.Ints(m.keys)
}

// AddNode 动态添加单个节点
func (m *Map) AddNode(key string) {
	for i := 0; i < m.replicas; i++ {
		hash := int(m.hash([]byte(strconv.Itoa(i) + key)))
		// 检查是否已存在
		if _, exists := m.hashMap[hash]; !exists {
			m.keys = append(m.keys, hash)
			m.hashMap[hash] = key
		}
	}
	sort.Ints(m.keys)
}

// RemoveNode 动态删除单个节点
func (m *Map) RemoveNode(key string) {
	var newKeys []int
	
	// 删除该节点的所有虚拟节点
	for _, hash := range m.keys {
		if m.hashMap[hash] != key {
			newKeys = append(newKeys, hash)
		} else {
			delete(m.hashMap, hash)
		}
	}
	
	m.keys = newKeys
}

// GetNodes 获取所有真实节点
func (m *Map) GetNodes() []string {
	nodeSet := make(map[string]bool)
	for _, key := range m.keys {
		nodeSet[m.hashMap[key]] = true
	}
	
	var nodes []string
	for node := range nodeSet {
		nodes = append(nodes, node)
	}
	return nodes
}

// Get gets the closest item in the hash to the provided key.
func (m *Map) Get(key string) string {
	if len(m.keys) == 0 {
		return ""
	}

	hash := int(m.hash([]byte(key)))
	// Binary search for appropriate replica.
	idx := sort.Search(len(m.keys), func(i int) bool {
		return m.keys[i] >= hash
	})

	return m.hashMap[m.keys[idx%len(m.keys)]]
}

// GetMigrationPlan 生成数据迁移计划
// 返回 map[源节点]目标节点列表
func (m *Map) GetMigrationPlan(oldMap *Map) map[string][]string {
	if oldMap == nil {
		return nil
	}

	plan := make(map[string][]string)
	
	// 获取新旧节点的所有真实节点
	oldNodes := oldMap.GetNodes()
	newNodes := m.GetNodes()
	
	// 为每个新节点计算需要从哪些旧节点迁移数据
	for _, newNode := range newNodes {
		// 检查新节点在旧哈希环上的数据分布
		for _, oldNode := range oldNodes {
			// 模拟一些key来检查数据分布变化
			// 这里简化处理，实际实现中可以根据具体需求优化
			if m.shouldMigrate(oldNode, newNode, oldMap) {
				plan[oldNode] = append(plan[oldNode], newNode)
			}
		}
	}
	
	return plan
}

// shouldMigrate 判断是否需要从旧节点迁移数据到新节点
func (m *Map) shouldMigrate(oldNode, newNode string, oldMap *Map) bool {
	// 简化实现：检查一些测试key的分布
	testKeys := []string{"test1", "test2", "test3", "test4", "test5"}
	
	for _, key := range testKeys {
		oldTarget := oldMap.Get(key)
		newTarget := m.Get(key)
		
		// 如果key原本在oldNode，现在需要从newNode获取，说明需要迁移
		if oldTarget == oldNode && newTarget == newNode {
			return true
		}
	}
	
	return false
}

// IsEmpty 检查哈希环是否为空
func (m *Map) IsEmpty() bool {
	return len(m.keys) == 0
}
