package jincache

import (
	pb "distributed-cache-demo/jincache/jincachepb"
	"distributed-cache-demo/jincache/singleflight"
	"fmt"
	"log"
	"sync"
)

// A Getter loads data for a key. 缓存中没有数据时，从外部数据源获取数据
type Getter interface {
	Get(key string) ([]byte, error)
}

// A GetterFunc implements Getter with a function.
type GetterFunc func(key string) ([]byte, error)

// Get implements Getter interface function
func (f GetterFunc) Get(key string) ([]byte, error) {
	return f(key)
}

// A Group is a cache namespace and associated data loaded spread over
type Group struct {
	name      string
	getter    Getter
	mainCache cache
	peers     PeerPicker
	// use singleflight.Group to make sure that
	// each key is only fetched once
	loader *singleflight.Group
}

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)
)

// NewGroup create a new instance of Group
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	if getter == nil {
		panic("nil Getter!")
	}

	mu.Lock()
	defer mu.Unlock()

	g := &Group{name: name, getter: getter, mainCache: cache{cacheBytes: cacheBytes}, loader: &singleflight.Group{}}

	groups[name] = g
	return g
}

// GetGroup returns the named group previously created with NewGroup, or
// nil if there's no such group.
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// Get value for a key from cache
func (g *Group) Get(key string) (ByteView, error) {
	if key == "" {
		return ByteView{}, fmt.Errorf("key is required")
	}

	// 先通过一致性哈希判断key应该路由到哪个节点
	if g.peers != nil {
		if peer, ok := g.peers.PickPeer(key); ok {
			// key应该路由到其他节点，转发请求
			log.Printf("[GeeCache] Routing key %s to peer", key)
			return g.getFromPeer(peer, key)
		}
		// PickPeer返回false表示key应该路由到当前节点
	}

	// key应该路由到当前节点，查本地缓存
	if v, ok := g.mainCache.get(key); ok {
		log.Println("[GeeCache] hit")
		return v, nil
	}

	// 缓存未命中，从数据源加载
	return g.getLocally(key)
}

// RegisterPeers registers a PeerPicker for choosing remote peer
func (g *Group) RegisterPeers(peers PeerPicker) {
	if g.peers != nil {
		panic("RegisterPeerPicker called more than once")
	}
	g.peers = peers
}

func (g *Group) load(key string) (value ByteView, err error) {
	// each key is only fetched once from data source
	// regardless of the number of concurrent callers.
	viewi, err := g.loader.Do(key, func() (interface{}, error) {
		return g.getLocally(key)
	})

	if err == nil {
		return viewi.(ByteView), nil
	}
	return
}

func (g *Group) getFromPeer(peer PeerGetter, key string) (ByteView, error) {
	req := &pb.Request{
		Group: g.name,
		Key:   key,
	}
	res := &pb.Response{}
	err := peer.Get(req, res)
	if err != nil {
		return ByteView{}, err
	}
	return ByteView{b: res.Value}, nil
}

func (g *Group) getLocally(key string) (ByteView, error) {
	bytes, err := g.getter.Get(key)
	if err != nil {
		return ByteView{}, err

	}
	value := ByteView{b: cloneBytes(bytes)}
	g.populateCache(key, value)
	return value, nil
}

func (g *Group) populateCache(key string, value ByteView) {
	g.mainCache.add(key, value)
}

// PopulateCache 公开的缓存填充方法，用于数据迁移
func (g *Group) PopulateCache(key string, value ByteView) {
	g.populateCache(key, value)
}

// Name 返回组名，实现CacheGroup接口
func (g *Group) Name() string {
	return g.name
}

// GetKeys 返回缓存中的所有key，用于数据迁移
func (g *Group) GetKeys() []string {
	return g.mainCache.Keys()
}

// Remove 从缓存中删除一个key，用于数据迁移
func (g *Group) Remove(key string) {
	g.mainCache.Remove(key)
}

// GetRemote 专门处理来自其他节点的请求，避免循环转发
func (g *Group) GetRemote(key string) (ByteView, error) {
	if key == "" {
		return ByteView{}, fmt.Errorf("key is required")
	}

	// 先检查本地缓存
	if v, ok := g.mainCache.get(key); ok {
		log.Println("[GeeCache] remote hit")
		return v, nil
	}

	// 缓存未命中，从数据源获取
	log.Println("[GeeCache] remote miss, getting from data source")
	return g.getLocally(key)
}
