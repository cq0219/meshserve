package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	Register("vllm", func(o Options) Engine {
		return &VLLMEngine{addr: o.HTTPAddr, client: &http.Client{Timeout: 30 * time.Second}}
	})
	Register("sglang", func(o Options) Engine {
		return &VLLMEngine{addr: o.HTTPAddr, client: &http.Client{Timeout: 30 * time.Second}}
	})
}

// VLLMEngine vLLM 引擎适配器：通过 OpenAI 兼容 HTTP API 与 vLLM 进程通信。
// sglang 也提供 OpenAI 兼容端点，故复用同一实现。
type VLLMEngine struct {
	addr   string // 引擎 HTTP 地址，如 127.0.0.1:8000
	client *http.Client
	model  string
}

// Name 返回引擎标识。
func (e *VLLMEngine) Name() string { return "vllm" }

// Addr 返回引擎地址。
func (e *VLLMEngine) Addr() string { return e.addr }

// Load 校验 vLLM 服务可达（真实加载由 vLLM 启动参数负责；此处做就绪探测）。
func (e *VLLMEngine) Load(ctx context.Context, cfg LoadConfig) error {
	if e.addr == "" {
		return fmt.Errorf("vLLM 引擎地址未配置（HTTPAddr 为空）")
	}
	if err := e.waitReady(ctx, 60*time.Second); err != nil {
		return fmt.Errorf("vLLM 服务未就绪: %w", err)
	}
	e.model = modelName(cfg.ModelPath)
	return nil
}

// waitReady 轮询 /v1/models 直至可用或超时。
func (e *VLLMEngine) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := "http://" + e.addr + "/v1/models"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := e.client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("等待引擎就绪超时")
}

// Health 返回引擎健康状态（探测 /v1/models）。
func (e *VLLMEngine) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.addr+"/v1/models", nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("引擎健康检查失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("引擎健康检查返回 %d", resp.StatusCode)
	}
	return nil
}

// Unload 卸载模型（vLLM 场景为幂等空操作）。
func (e *VLLMEngine) Unload(ctx context.Context) error { return nil }

// Chat 执行对话；stream=true 时逐 chunk 回调。
func (e *VLLMEngine) Chat(ctx context.Context, req ChatRequest, chunkFn func(ChatChunk) error) (*ChatChunk, error) {
	if e.model != "" {
		req.Model = e.model
	}
	payload := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   req.Stream,
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+e.addr+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用 vLLM 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("vLLM 返回 %d: %s", resp.StatusCode, string(b))
	}
	if !req.Stream {
		return e.parseNonStream(resp.Body)
	}
	return e.parseStream(resp.Body, chunkFn)
}

// parseNonStream 解析非流式响应（OpenAI JSON 格式）。
func (e *VLLMEngine) parseNonStream(r io.Reader) (*ChatChunk, error) {
	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 vLLM 响应失败: %w", err)
	}
	content := ""
	if len(out.Choices) > 0 {
		content = out.Choices[0].Message.Content
	}
	return &ChatChunk{ID: out.ID, Model: out.Model, Content: content, Done: true}, nil
}

// parseStream 解析 SSE 流并逐 chunk 回调。
func (e *VLLMEngine) parseStream(r io.Reader, chunkFn func(ChatChunk) error) (*ChatChunk, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var last *ChatChunk
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 跳过非标准行
		}
		content := ""
		if len(chunk.Choices) > 0 {
			content = chunk.Choices[0].Delta.Content
		}
		done := len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil
		cc := ChatChunk{ID: chunk.ID, Model: chunk.Model, Content: content, Done: done}
		last = &cc
		if chunkFn != nil {
			if err := chunkFn(cc); err != nil {
				return last, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 SSE 流失败: %w", err)
	}
	return last, nil
}

func modelName(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
