package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestVLLM_ReuseExisting 目标端口已有就绪服务 → 复用，不拉起进程。
func TestVLLM_ReuseExisting(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")

	e := newVLLMEngine(Options{HTTPAddr: addr, VLLMBin: "vllm", VLLMTimeout: 5 * time.Second})
	if err := e.Load(context.Background(), LoadConfig{ModelPath: "/models/qwen3-8b", ModelName: "qwen3-8b"}); err != nil {
		t.Fatalf("复用已有服务应成功: %v", err)
	}
	if e.cmd != nil {
		t.Error("复用模式不应拉起进程")
	}
	if e.model != "qwen3-8b" {
		t.Errorf("模型名未生效: %q", e.model)
	}
	// Health 应正常
	if err := e.Health(context.Background()); err != nil {
		t.Errorf("Health 失败: %v", err)
	}
	// Unload 空操作
	if err := e.Unload(context.Background()); err != nil {
		t.Errorf("Unload 失败: %v", err)
	}
}

// TestVLLM_BinMissing 无就绪服务且 vllm 可执行文件缺失 → 明确错误。
func TestVLLM_BinMissing(t *testing.T) {
	// 分配一个无人监听的空闲端口（临时 Listen 后关闭）
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	e := newVLLMEngine(Options{HTTPAddr: addr, VLLMBin: "vllm-definitely-not-exist-xyz", VLLMTimeout: 2 * time.Second})
	err = e.Load(context.Background(), LoadConfig{ModelPath: "/models/qwen3-8b"})
	if err == nil {
		t.Fatal("缺少可执行文件应返回错误")
	}
	if !strings.Contains(err.Error(), "未找到") || !strings.Contains(err.Error(), "vllm-definitely-not-exist-xyz") {
		t.Errorf("错误信息应提示安装/配置 vllm_bin: %v", err)
	}
}

// TestVLLM_AddrEmpty 未配置地址 → 明确错误。
func TestVLLM_AddrEmpty(t *testing.T) {
	e := newVLLMEngine(Options{})
	err := e.Load(context.Background(), LoadConfig{ModelPath: "/models/x"})
	if err == nil || !strings.Contains(err.Error(), "HTTPAddr 为空") {
		t.Errorf("地址为空应报错: %v", err)
	}
}

// TestVLLM_PortOf 端口提取。
func TestVLLM_PortOf(t *testing.T) {
	if got := portOf("127.0.0.1:8001"); got != "8001" {
		t.Errorf("portOf 失败: %q", got)
	}
	if got := portOf("bad"); got != "" {
		t.Errorf("非法地址应返回空: %q", got)
	}
}
