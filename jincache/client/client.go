package client

import (
	"bytes"
	"distributed-cache-demo/jincache/jincachepb"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"google.golang.org/protobuf/proto"
)

// Client 缓存客户端
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 创建新的缓存客户端
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Get 获取缓存值
func (c *Client) Get(group, key string) ([]byte, error) {
	u := fmt.Sprintf("%s/_jincache/%s/%s", c.baseURL, url.QueryEscape(group), url.QueryEscape(key))

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
	u := fmt.Sprintf("%s/_jincache/%s/%s", c.baseURL, url.QueryEscape(group), url.QueryEscape(key))

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
	u := fmt.Sprintf("%s/_jincache/%s/%s", c.baseURL, url.QueryEscape(group), url.QueryEscape(key))

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

// GetStats 获取缓存统计信息
func (c *Client) GetStats(group string) (*StatsResponse, error) {
	u := fmt.Sprintf("%s/_jincache/%s/_stats", c.baseURL, url.QueryEscape(group))

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
