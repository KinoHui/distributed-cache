package migration

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	pb "github.com/KinoHui/distributed-cache/jincache/jincachepb"
	"google.golang.org/protobuf/proto"
)

// CacheGroup 缓存组接口，避免循环导入
type CacheGroup interface {
	Name() string
	Get(key string) (interface{}, error)
	PopulateCache(key string, value interface{})
}

// DataMigrator 数据迁移器
type DataMigrator struct {
	localGroup CacheGroup
	batchSize  int
	timeout    time.Duration
}

// NewDataMigrator 创建数据迁移器
func NewDataMigrator(group CacheGroup, batchSize int, timeout time.Duration) *DataMigrator {
	return &DataMigrator{
		localGroup: group,
		batchSize:  batchSize,
		timeout:    timeout,
	}
}

// MigrationPlan 迁移计划
type MigrationPlan struct {
	SourceNode string
	TargetNode string
	Keys       []string
}

// MigrateData 执行数据迁移
func (dm *DataMigrator) MigrateData(ctx context.Context, plan MigrationPlan) error {
	if len(plan.Keys) == 0 {
		return nil
	}

	log.Printf("[Migration] Starting migration from %s to %s, %d keys",
		plan.SourceNode, plan.TargetNode, len(plan.Keys))

	// 分批迁移
	for i := 0; i < len(plan.Keys); i += dm.batchSize {
		end := i + dm.batchSize
		if end > len(plan.Keys) {
			end = len(plan.Keys)
		}

		batch := plan.Keys[i:end]
		if err := dm.migrateBatch(ctx, batch, plan.SourceNode, plan.TargetNode); err != nil {
			return fmt.Errorf("failed to migrate batch %d-%d: %v", i, end-1, err)
		}

		log.Printf("[Migration] Migrated batch %d-%d/%d", i, end-1, len(plan.Keys))
	}

	log.Printf("[Migration] Completed migration from %s to %s", plan.SourceNode, plan.TargetNode)
	return nil
}

// migrateBatch 迁移一批数据
func (dm *DataMigrator) migrateBatch(ctx context.Context, keys []string, sourceNode, targetNode string) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(keys))

	for _, key := range keys {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()

			if err := dm.migrateKey(ctx, k, sourceNode); err != nil {
				errCh <- fmt.Errorf("failed to migrate key %s: %v", k, err)
			}
		}(key)
	}

	wg.Wait()
	close(errCh)

	// 检查是否有错误
	for err := range errCh {
		return err
	}

	return nil
}

// migrateKey 迁移单个key
func (dm *DataMigrator) migrateKey(ctx context.Context, key, sourceNode string) error {
	// 从源节点获取数据
	req := &pb.Request{
		Group: dm.localGroup.Name(),
		Key:   key,
	}

	// 通过HTTP客户端从源节点获取数据
	data, err := dm.fetchFromSource(ctx, sourceNode, req)
	if err != nil {
		return fmt.Errorf("failed to fetch key %s from source: %v", key, err)
	}

	// 将数据存储到本地缓存
	dm.localGroup.PopulateCache(key, data)

	log.Printf("[Migration] Migrated key %s from %s", key, sourceNode)
	return nil
}

// fetchFromSource 从源节点获取数据
func (dm *DataMigrator) fetchFromSource(ctx context.Context, sourceNode string, req *pb.Request) ([]byte, error) {
	// 构造请求URL
	u := fmt.Sprintf(
		"%v%v/%v",
		sourceNode,
		url.QueryEscape(req.GetGroup()),
		url.QueryEscape(req.GetKey()),
	)

	// 创建HTTP请求
	httpReq, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// 发送请求
	client := &http.Client{Timeout: dm.timeout}
	res, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch from source: %v", err)
	}
	defer res.Body.Close()

	// 检查响应状态
	if res.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("key not found on source node")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source node returned status: %d", res.StatusCode)
	}

	// 读取响应体
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	// 解析protobuf响应
	var pbRes pb.Response
	if err := proto.Unmarshal(data, &pbRes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	return pbRes.Value, nil
}

// GetKeysToMigrate 获取需要迁移的keys
// 这是一个简化实现，实际中需要根据缓存的具体实现来获取keys
func (dm *DataMigrator) GetKeysToMigrate(sourceNode, targetNode string) ([]string, error) {
	// 简化实现：返回一些测试keys
	// 实际实现中需要：
	// 1. 从源节点获取所有keys
	// 2. 根据一致性哈希算法判断哪些keys需要迁移到目标节点

	testKeys := []string{"key1", "key2", "key3", "key4", "key5"}
	log.Printf("[Migration] Found %d keys to migrate from %s to %s",
		len(testKeys), sourceNode, targetNode)

	return testKeys, nil
}

// VerifyMigration 验证迁移结果
func (dm *DataMigrator) VerifyMigration(ctx context.Context, keys []string) error {
	log.Printf("[Migration] Verifying migration of %d keys", len(keys))

	for _, key := range keys {
		// 检查本地缓存是否有该key
		_, err := dm.localGroup.Get(key)
		if err != nil {
			return fmt.Errorf("verification failed for key %s: %v", key, err)
		}
	}

	log.Printf("[Migration] Verification completed for %d keys", len(keys))
	return nil
}
