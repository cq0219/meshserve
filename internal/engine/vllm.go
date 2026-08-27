package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	Register("vllm", func(o Options) Engine {
		return newVLLMEngine(o)
	})
	Register("sglang", func(o Options) Engine {
		e := newVLLMEngine(o)
		e.sglang = true
		return e
	})
}

// newVLLMEngine 创建 vLLM/sglang 引擎。
func newVLLMEngine(o Options) *VLLMEngine {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	timeout := o.VLLMTimeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return &VLLMEngine{
		addr:      o.HTTPAddr,
		bin:       o.VLLMBin,
		timeout:   timeout,
		extraArgs: o.VLLMExtraArgs,
		log:       log,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// VLLMEngine vLLM 引擎适配器（M6 进程拉起模式）：
// 1. 若目标端口已有就绪的 vLLM 服务（用户手动启动）→ 直接复用；
// 2. 否则由 MeshServe 拉起 `vllm serve <model>` 子进程并管理生命周期（就绪轮询/优雅停止/健康自愈）。
// sglang 也提供 OpenAI 兼容端点，复用同一实现（sglang 标记）。
type VLLMEngine struct {
	addr      string        // 引擎 HTTP 地址 127.0.0.1:<port>（agent 分配）
	bin       string        // vLLM 可执行文件（默认 "vllm"）
	timeout   time.Duration // 启动就绪等待
	extraArgs []string      // 附加启动参数
	sglang    bool
	log       *slog.Logger

	mu     sync.Mutex
	cmd    *exec.Cmd // 拉起的 vLLM 子进程（复用外部服务时为 nil）
	client *http.Client
	model  string
}

// Name 返回引擎标识。
func (e *VLLMEngine) Name() string {
	if e.sglang {
		return "sglang"
	}
	return "vllm"
}

// Addr 返回引擎地址。
func (e *VLLMEngine) Addr() string { return e.addr }

// Load 加载模型：复用已就绪服务，或拉起 vLLM 进程并等待就绪。
func (e *VLLMEngine) Load(ctx context.Context, cfg LoadConfig) error {
	if e.addr == "" {
		return fmt.Errorf("vLLM 引擎地址未配置（HTTPAddr 为空）")
	}
	e.mu.Lock()
	e.model = cfg.ModelName
	if e.model == "" {
		e.model = modelName(cfg.ModelPath)
	}
	e.mu.Unlock()

	// 1. 目标端口已有就绪服务 → 复用（外部手动启动场景）
	if e.waitReady(ctx, 5*time.Second) == nil {
		e.log.Info("vLLM 服务已存在，复用", "addr", e.addr, "model", e.model)
		return nil
	}
	// 2. 拉起 vLLM 进程
	if err := e.spawn(ctx, cfg); err != nil {
		return err
	}
	// 3. 轮询就绪（同时检测进程退出）
	if err := e.waitSpawnedReady(ctx); err != nil {
		_ = e.stopProcess()
		return err
	}
	e.log.Info("vLLM 进程已就绪", "addr", e.addr, "model", e.model)
	return nil
}

// spawn 启动 vLLM 子进程。
func (e *VLLMEngine) spawn(ctx context.Context, cfg LoadConfig) error {
	bin := e.bin
	if bin == "" {
		bin = "vllm"
	}
	// vllm_bin 支持"可执行 + 固定前缀参数"（如 "python /opt/fake_vllm.py"），默认直接 "vllm"
	parts := strings.Fields(bin)
	exe := parts[0]
	prefix := parts[1:]
	path, err := exec.LookPath(exe)
	if err != nil {
		return fmt.Errorf("未找到 %s 可执行文件：请安装 vLLM（pip install vllm）或配置 agent.vllm_bin（当前需拉起的模型: %s）", exe, cfg.ModelPath)
	}
	port := portOf(e.addr)
	if port == "" {
		return fmt.Errorf("引擎地址缺少端口: %s", e.addr)
	}
	args := append(append([]string{}, prefix...), "serve", cfg.ModelPath, "--host", "127.0.0.1", "--port", port)
	if e.model != "" {
		args = append(args, "--served-model-name", e.model)
	}
	if cfg.TensorParallel > 1 {
		args = append(args, "--tensor-parallel-size", strconv.Itoa(cfg.TensorParallel))
	}
	args = append(args, e.extraArgs...)
	cmd := exec.Command(path, args...) // 生命周期独立于请求 ctx（由 Unload/自愈管理）
	cmd.Stdout = &logWriter{log: e.log, tag: bin}
	cmd.Stderr = &logWriter{log: e.log, tag: bin}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s 失败: %w", bin, err)
	}
	e.mu.Lock()
	e.cmd = cmd
	e.mu.Unlock()
	e.log.Info("vLLM 进程已启动", "bin", bin, "args", strings.Join(args, " "), "pid", cmd.Process.Pid)
	return nil
}

// waitSpawnedReady 轮询引擎就绪；进程提前退出时返回退出错误。
func (e *VLLMEngine) waitSpawnedReady(ctx context.Context) error {
	deadline := time.Now().Add(e.timeout)
	for {
		if e.waitReady(ctx, 2*time.Second) == nil {
			return nil
		}
		e.mu.Lock()
		exited := e.cmd != nil && e.cmd.ProcessState != nil
		e.mu.Unlock()
		if exited {
			return fmt.Errorf("vLLM 进程启动失败（已退出），请查看日志（模型路径或参数是否有效）")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 vLLM 就绪超时（%s），进程已终止", e.timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitReady 轮询 /v1/models 直至可用或超时。
func (e *VLLMEngine) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := "http://" + e.addr + "/v1/models"
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

// stopProcess 停止 vLLM 子进程（幂等）。
func (e *VLLMEngine) stopProcess() error {
	e.mu.Lock()
	cmd := e.cmd
	e.cmd = nil
	e.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	e.log.Info("vLLM 进程已停止", "pid", cmd.Process.Pid)
	return nil
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
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("引擎健康检查返回 %d", resp.StatusCode)
	}
	return nil
}

// Unload 卸载模型并停止拉起的 vLLM 进程（复用外部服务时为空操作）。
func (e *VLLMEngine) Unload(ctx context.Context) error {
	return e.stopProcess()
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+e.addr+"/v1/chat/completions", bytes.NewReader(body))
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
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

// portOf 提取 host:port 中的端口。
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// logWriter 将子进程 stdout/stderr 转发到 slog。
type logWriter struct {
	log *slog.Logger
	tag string
}

func (w *logWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line != "" {
		w.log.Info("["+w.tag+"] "+line, "src", "subprocess")
	}
	return len(p), nil
}
