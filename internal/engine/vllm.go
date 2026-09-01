package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func init() {
	// vllm：MeshServe 直接拉起 vllm serve 进程并管理生命周期（含 TP/PP 参数注入）
	Register("vllm", func(o Options) Engine {
		return &VLLMEngine{addr: o.HTTPAddr, client: &http.Client{Timeout: 30 * time.Second}, opts: o, spawn: true}
	})
	// sglang：保持探测模式（外部启动 sglang，MeshServe 只做就绪探测与转发）
	Register("sglang", func(o Options) Engine {
		return &VLLMEngine{addr: o.HTTPAddr, client: &http.Client{Timeout: 30 * time.Second}, spawn: false}
	})
}

// VLLMEngine vLLM 引擎适配器。
// 进程拉起模式（spawn=true）：Load 执行 vllm serve 拉起进程、Unload 终止进程；
// 探测模式（spawn=false，sglang）：仅探测 OpenAI 兼容 HTTP 端点。
type VLLMEngine struct {
	addr   string // 引擎 HTTP 地址（对外，PP 时 0.0.0.0 供集群访问）
	probe  string // 本机探测地址（始终 127.0.0.1:port，就绪/健康检查用）
	client *http.Client
	model  string
	opts   Options
	spawn  bool
	cmd    *exec.Cmd
	loaded bool
	// ppWorker 表示 PP worker rank（无 HTTP API，不做就绪探测）
	ppWorker bool
}

// Name 返回引擎标识。
func (e *VLLMEngine) Name() string { return "vllm" }

// Addr 返回引擎地址。
func (e *VLLMEngine) Addr() string { return e.addr }

// Load 加载模型：进程拉起（vllm）或就绪探测（sglang/外部服务）。
func (e *VLLMEngine) Load(ctx context.Context, cfg LoadConfig) error {
	if e.spawn {
		return e.loadSpawn(ctx, cfg)
	}
	// 探测模式（sglang 或外部已启动的 vLLM）
	if e.addr == "" {
		return fmt.Errorf("引擎地址未配置（HTTPAddr 为空）")
	}
	if err := e.waitReady(ctx, 120*time.Second); err != nil {
		return fmt.Errorf("引擎服务未就绪: %w", err)
	}
	e.model = modelName(cfg.ModelPath)
	return nil
}

// loadSpawn 拉起 vllm serve 进程。PP worker rank 无 HTTP API，进程启动即视为就绪。
func (e *VLLMEngine) loadSpawn(ctx context.Context, cfg LoadConfig) error {
	if e.opts.Command == "" {
		return fmt.Errorf("vLLM 可执行文件未配置（agent.vllm_command 或 PATH 中的 vllm）")
	}
	port := cfg.Port
	if port == 0 {
		port = 8000
	}
	host := "127.0.0.1"
	if cfg.PPTotal > 1 {
		// 跨节点 PP：rank0 对外地址需对集群可见（vLLM 返回的 base_url 供其他节点转发）
		host = "0.0.0.0"
	}
	e.addr = host + ":" + strconv.Itoa(port)
	e.probe = "127.0.0.1:" + strconv.Itoa(port) // 本机探测始终走回环
	e.model = modelName(cfg.ModelPath)
	e.ppWorker = cfg.PPRank > 0

	args := e.buildServeArgs(cfg, host, port)
	cmd := exec.CommandContext(ctx, e.opts.Command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	applySysProcAttr(cmd) // Unix：独立进程组便于整体终止
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 vllm serve 失败: %w", err)
	}
	e.cmd = cmd
	e.loaded = true

	if e.ppWorker {
		// worker rank 无 HTTP API：仅确认进程存活即可
		return nil
	}
	// rank0 / 单机：等待 OpenAI API 就绪
	if err := e.waitReady(ctx, 600*time.Second); err != nil {
		_ = e.Unload(ctx)
		return fmt.Errorf("vllm serve 未就绪: %w", err)
	}
	return nil
}

// buildServeArgs 构造 vllm serve 启动参数：
// serve <path> --host --port --tensor-parallel-size --pipeline-parallel-size
// --served-model-name --distributed-executor-backend [用户自定义参数]
func (e *VLLMEngine) buildServeArgs(cfg LoadConfig, host string, port int) []string {
	args := []string{"serve", cfg.ModelPath}
	args = append(args, "--host", host, "--port", strconv.Itoa(port))
	if cfg.TensorParallel > 1 {
		args = append(args, "--tensor-parallel-size", strconv.Itoa(cfg.TensorParallel))
	}
	pp := cfg.PPTotal
	if pp <= 1 {
		pp = 0
	}
	if pp > 1 {
		args = append(args, "--pipeline-parallel-size", strconv.Itoa(pp))
		backend := cfg.DistributedBackend
		if backend == "" {
			backend = "ray"
		}
		args = append(args, "--distributed-executor-backend", backend)
	}
	if e.model != "" {
		args = append(args, "--served-model-name", e.model)
	}
	// 用户自定义参数（模型注册表单 engine_args）追加在末尾，可覆盖默认
	if len(e.opts.Args) > 0 {
		args = append(args, e.opts.Args...)
	}
	return args
}

// httpHost 返回本机可连接的 HTTP 主机（探测回环地址优先，对外地址兜底）。
// 对外地址在 PP 模式下为 0.0.0.0（仅集群可见），本机请求必须走回环探测地址。
func (e *VLLMEngine) httpHost() string {
	if e.probe != "" {
		return e.probe
	}
	return e.addr
}

// waitReady 轮询 /v1/models 直至可用或超时（优先探测本机回环地址）。
func (e *VLLMEngine) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := "http://" + e.httpHost() + "/v1/models"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := e.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
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

// Health 返回引擎健康状态。
func (e *VLLMEngine) Health(ctx context.Context) error {
	if e.spawn {
		// 进程拉起模式：先校验进程存活
		if !e.loaded || e.cmd == nil || e.cmd.Process == nil {
			return errors.New("vllm 进程不存在")
		}
		if e.ppWorker {
			return nil // worker 无 HTTP API，进程存活即健康
		}
	}
	// rank0 / 探测模式：HTTP 就绪探测
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.httpHost()+"/v1/models", nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("引擎健康检查失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("引擎健康检查返回 %d", resp.StatusCode)
	}
	return nil
}

// Unload 终止进程（探测模式为幂等空操作）。
func (e *VLLMEngine) Unload(ctx context.Context) error {
	if e.spawn && e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		_, _ = e.cmd.Process.Wait()
		e.cmd = nil
	}
	e.loaded = false
	return nil
}

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
	// 本机请求始终走回环探测地址（PP 模式下 e.addr 为 0.0.0.0 不可连接）
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+e.httpHost()+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用 vLLM 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
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
	return filepath.Base(strings.TrimRight(path, "/"))
}
