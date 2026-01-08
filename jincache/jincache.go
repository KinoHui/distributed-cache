package jincache

import (
	pb "distributed-cache-demo/jincache/jincachepb"
	"distributed-cache-demo/jincache/singleflight"
	errors "errors"
	"fmt"
	"log"
	"sync"
)

// KeyNotFoundError 表示key不存在的错误
var KeyNotFoundError = errors.New("key not found")

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
	enableGetter bool
}

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)
)

// NewGroup create a new instance of Group
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	mu.Lock()
	defer mu.Unlock()

	enableGetter := getter != nil
	g := &Group{
		name: name,
		getter: getter,
		mainCache: cache{cacheBytes: cacheBytes},
		loader: &singleflight.Group{},
		enableGetter: enableGetter,
	}

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
			log.Printf("[JinCache] Routing key=%s to peer", key)
			return g.getFromPeer(peer, key)
		}
		// PickPeer返回false表示key应该路由到当前节点
	}

	// key应该路由到当前节点，查本地缓存
	if v, ok := g.mainCache.get(key); ok {
		log.Printf("[JinCache] Cache hit: key=%s", key)
		return v, nil
	}

	// 缓存未命中，如果启用了getter则从数据源加载
	if g.enableGetter {
		// each key is only fetched once from data source
		// regardless of the number of concurrent callers.
		viewi, err := g.loader.Do(key, func() (interface{}, error) {
			return g.getLocally(key)
		})

		if err == nil {
			return viewi.(ByteView), nil
		}
		return ByteView{}, err
	}

	return ByteView{}, KeyNotFoundError
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

func (g *Group) getFromPeer(peer PeerClient, key string) (ByteView, error) {
	req := &pb.Request{
		Group: g.name,
		Key:   key,
	}
	res := &pb.Response{}
	err := peer.Get(req, res)
	if err != nil {
		// 如果是key不存在的错误，直接返回，不降级
		if err == KeyNotFoundError {
			log.Printf("[JinCache] Key not found on peer: %s", key)
			return ByteView{}, KeyNotFoundError
		}
		log.Printf("[JinCache] Failed to get from peer: %v", err)
		// 如果启用了getter，降级到本地数据源
		if g.enableGetter {
			return g.getLocally(key)
		}
		return ByteView{}, err
	}
	return ByteView{b: res.Value}, nil
}

func (g *Group) getLocally(key string) (ByteView, error) {
	bytes, err := g.getter.Get(key)
	if err != nil {
		return ByteView{}, KeyNotFoundError
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

// Set 设置缓存值，通过一致性哈希判断应该路由到哪个节点
func (g *Group) Set(key string, value ByteView) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}

	// 先通过一致性哈希判断key应该路由到哪个节点
	if g.peers != nil {
		if peer, ok := g.peers.PickPeer(key); ok {
			// key应该路由到其他节点，转发请求
			log.Printf("[JinCache] Routing set key=%s to peer", key)
			return g.setToPeer(peer, key, value)
		}
		// PickPeer返回false表示key应该路由到当前节点
	}

	// key应该路由到当前节点，直接设置本地缓存
	g.populateCache(key, value)
	log.Printf("[JinCache] Set key=%s to local cache", key)
	return nil
}

// Delete 删除缓存值，通过一致性哈希判断应该路由到哪个节点
func (g *Group) Delete(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}

	// 先通过一致性哈希判断key应该路由到哪个节点
	if g.peers != nil {
		if peer, ok := g.peers.PickPeer(key); ok {
			// key应该路由到其他节点，转发请求
			log.Printf("[JinCache] Routing delete key=%s to peer", key)
			return g.deleteFromPeer(peer, key)
		}
		// PickPeer返回false表示key应该路由到当前节点
	}

	// key应该路由到当前节点，直接删除本地缓存
	g.mainCache.Remove(key)
	log.Printf("[JinCache] Delete key=%s from local cache", key)
	return nil
}

// setToPeer 将设置请求转发到远程节点
func (g *Group) setToPeer(peer PeerClient, key string, value ByteView) error {
	req := &pb.Request{
		Group: g.name,
		Key:   key,
		Value: value.ByteSlice(),
	}
	err := peer.Set(req)
	if err != nil {
		log.Printf("[JinCache] Failed to set to peer, falling back to local: %v", err)
		// 降级到本地缓存
		g.populateCache(key, value)
	}
	return err
}

// deleteFromPeer 将删除请求转发到远程节点
func (g *Group) deleteFromPeer(peer PeerClient, key string) error {
	req := &pb.Request{
		Group: g.name,
		Key:   key,
	}
	err := peer.Delete(req)
	if err != nil {
		log.Printf("[JinCache] Failed to delete from peer, falling back to local: %v", err)
		// 降级到本地缓存
		g.mainCache.Remove(key)
	}
	return err
}
