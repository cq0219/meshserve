package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func init() {
	Register("llamacpp", func(o Options) Engine {
		return &LlamaCPPEngine{opts: o, log: slog.Default()}
	})
}

// LlamaCPPEngine llama.cpp 引擎：直接管理本地进程（适合低配节点/CPU）。
// 通过 OpenAI 兼容 server 模式（llama-server）与外部通信。
type LlamaCPPEngine struct {
	opts   Options
	cmd    *exec.Cmd
	log    *slog.Logger
	addr   string
	model  string
	loaded bool
}

// Name 返回引擎标识。
func (e *LlamaCPPEngine) Name() string { return "llamacpp" }

// Addr 返回地址。
func (e *LlamaCPPEngine) Addr() string { return e.addr }

// Load 拉起 llama-server 进程并等待就绪。
func (e *LlamaCPPEngine) Load(ctx context.Context, cfg LoadConfig) error {
	if e.opts.Command == "" {
		return fmt.Errorf("llama.cpp 引擎未配置可执行文件（Options.Command 为空）")
	}
	e.model = filepath.Base(cfg.ModelPath)
	port := "8081"
	args := []string{
		"-m", cfg.ModelPath,
		"--host", "127.0.0.1",
		"--port", port,
		"-c", "4096",
	}
	if cfg.Quant != "" && cfg.Quant != "fp16" {
		args = append(args, "--no-mmap") // 保持简单，真实量化文件自带
	}
	if len(e.opts.Args) > 0 {
		args = append(args, e.opts.Args...)
	}
	e.addr = "127.0.0.1:" + port
	cmd := exec.CommandContext(ctx, e.opts.Command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	applySysProcAttr(cmd) // Unix：独立进程组便于整体终止
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 llama-server 失败: %w", err)
	}
	e.cmd = cmd
	e.loaded = true
	// 等待就绪（复用 vLLM 的轮询逻辑）
	probe := &VLLMEngine{addr: e.addr, client: &http.Client{Timeout: 30 * time.Second}}
	if err := probe.waitReady(ctx, 120*time.Second); err != nil {
		_ = e.Unload(ctx)
		return fmt.Errorf("llama-server 未就绪: %w", err)
	}
	e.log.Info("llama.cpp 引擎已就绪", "addr", e.addr, "model", e.model)
	return nil
}

// Unload 终止进程。
func (e *LlamaCPPEngine) Unload(ctx context.Context) error {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		_, _ = e.cmd.Process.Wait()
		e.cmd = nil
	}
	e.loaded = false
	return nil
}

// Chat 转发到 llama-server（OpenAI 兼容）。
func (e *LlamaCPPEngine) Chat(ctx context.Context, req ChatRequest, chunkFn func(ChatChunk) error) (*ChatChunk, error) {
	if !e.loaded || e.cmd == nil {
		return nil, errors.New("llama.cpp 引擎未就绪")
	}
	proxy := &VLLMEngine{addr: e.addr, client: &http.Client{Timeout: 30 * time.Second}}
	req.Model = e.model
	return proxy.Chat(ctx, req, chunkFn)
}

// Health 进程存活检查。
func (e *LlamaCPPEngine) Health(ctx context.Context) error {
	if !e.loaded || e.cmd == nil || e.cmd.Process == nil {
		return errors.New("llama.cpp 进程不存在")
	}
	return nil
}
