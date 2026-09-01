// Agent RPC 客户端：集群控制面（协调节点）向远端节点 agent 发起部署/停止/查询。
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client 远端 agent 的 HTTP 客户端。
type Client struct {
	base string // http://host:port
	hc   *http.Client
}

// NewClient 创建客户端，addr 形如 "10.0.0.5:9100"。
func NewClient(addr string) *Client {
	return &Client{base: "http://" + addr, hc: &http.Client{Timeout: 10 * time.Minute}}
}

// Health 探测远端 agent 存活（GET /healthz）。
func (c *Client) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("远端 agent 不可达: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("远端 agent 健康检查失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Deploy 远端部署实例（阻塞至就绪或超时）。
func (c *Client) Deploy(ctx context.Context, req DeployRequest) error {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/deploy", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("调用远端 agent 部署失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("远端部署失败: %s", e.Error)
	}
	return nil
}

// Stop 远端停止实例。
func (c *Client) Stop(ctx context.Context, instanceID string) error {
	body, _ := json.Marshal(StopRequest{InstanceID: instanceID})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/stop", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("调用远端 agent 停止失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("远端停止失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Instances 查询远端实例列表。
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/instances", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("查询远端实例失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查询远端实例失败: HTTP %d", resp.StatusCode)
	}
	var out []Instance
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
