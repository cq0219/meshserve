package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildFakeVLLM 编译 testdata/fakevllm 为临时可执行文件。
func buildFakeVLLM(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakevllm")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakevllm")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译 fakevllm 失败: %v\n%s", err, out)
	}
	return bin
}

// freePort 获取一个空闲 TCP 端口。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// argsLog 读取 fakevllm 的参数日志。
func argsLog(t *testing.T, port int) string {
	t.Helper()
	p := filepath.Join(os.TempDir(), fmt.Sprintf("fakevllm-args-%d.log", port))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取参数日志失败: %v", err)
	}
	return string(b)
}

// TestVLLMEngine_Spawn rank0 进程拉起：vllm serve 带 PP/TP/backend 参数，就绪探测通过，Unload 杀进程。
func TestVLLMEngine_Spawn(t *testing.T) {
	bin := buildFakeVLLM(t)
	port := freePort(t)
	e := &VLLMEngine{
		addr: "127.0.0.1:" + fmt.Sprintf("%d", port),
		opts: Options{Command: bin, Args: []string{"--max-model-len", "4096"}},
		client: &http.Client{Timeout: 3 * time.Second},
		spawn:  true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := e.Load(ctx, LoadConfig{
		ModelPath:          "/models/qwen3-8b",
		TensorParallel:     1,
		PPRank:             0,
		PPTotal:            2,
		DistributedBackend: "ray",
		Port:               port,
	})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	defer func() { _ = e.Unload(context.Background()) }()

	// 进程存活 + HTTP 就绪
	if err := e.Health(ctx); err != nil {
		t.Errorf("Health 失败: %v", err)
	}
	// 启动参数注入断言
	args := argsLog(t, port)
	for _, want := range []string{"serve", "/models/qwen3-8b", "--pipeline-parallel-size", "2",
		"--distributed-executor-backend", "ray", "--served-model-name", "qwen3-8b", "--max-model-len"} {
		if !strings.Contains(args, want) {
			t.Errorf("启动参数缺少 %q: %s", want, args)
		}
	}

	// Chat 转发可用
	if _, err := e.Chat(ctx, ChatRequest{Model: "qwen3-8b", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, nil); err != nil {
		t.Errorf("Chat 失败: %v", err)
	}

	// Unload 杀进程
	_ = e.Unload(context.Background())
	if err := e.Health(ctx); err == nil {
		t.Error("Unload 后 Health 应失败（进程已终止）")
	}
}

// TestVLLMEngine_PPWorker PP worker rank：进程拉起即成功，不做 HTTP 就绪探测。
func TestVLLMEngine_PPWorker(t *testing.T) {
	bin := buildFakeVLLM(t)
	port := freePort(t)
	e := &VLLMEngine{
		opts:   Options{Command: bin},
		client: &http.Client{Timeout: 3 * time.Second},
		spawn:  true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := e.Load(ctx, LoadConfig{
		ModelPath:      "/models/qwen3-8b",
		PPRank:         1, // worker
		PPTotal:        2,
		Port:           port,
		Quant:          "fp16",
	})
	if err != nil {
		t.Fatalf("worker Load 失败: %v", err)
	}
	defer func() { _ = e.Unload(context.Background()) }()
	// 进程存活即健康（无 HTTP API）
	if err := e.Health(ctx); err != nil {
		t.Errorf("worker Health 失败: %v", err)
	}
	if !e.ppWorker {
		t.Error("ppWorker 标志应为 true")
	}
}

// TestVLLMEngine_Probe 探测模式（sglang/外部 vLLM）：仅就绪探测，不拉起进程。
func TestVLLMEngine_Probe(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer ts.Close()
	e := &VLLMEngine{addr: strings.TrimPrefix(ts.URL, "http://"), client: ts.Client(), spawn: false}
	if err := e.Load(context.Background(), LoadConfig{ModelPath: "/models/x", Port: 0}); err != nil {
		t.Fatalf("探测 Load 失败: %v", err)
	}
	if err := e.Health(context.Background()); err != nil {
		t.Errorf("探测 Health 失败: %v", err)
	}
}
