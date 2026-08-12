package engine

import (
	"context"
	"fmt"
	"strings"
)

func init() {
	Register("fake", func(Options) Engine { return &FakeEngine{} })
}

// FakeEngine 内存模拟引擎：无 GPU 依赖，用于开发/CI/演示。
// 生产环境不会被调度器选中（除非显式指定 engine=fake）。
type FakeEngine struct {
	loaded bool
	addr   string
}

// Name 返回引擎标识。
func (e *FakeEngine) Name() string { return "fake" }

// Addr 返回地址。
func (e *FakeEngine) Addr() string { return e.addr }

// Load 模拟模型加载（记录状态）。
func (e *FakeEngine) Load(ctx context.Context, cfg LoadConfig) error {
	e.loaded = true
	e.addr = "fake://" + cfg.ModelPath
	return nil
}

// Unload 模拟卸载。
func (e *FakeEngine) Unload(ctx context.Context) error {
	e.loaded = false
	return nil
}

// Chat 生成模拟回复：回显最后一条用户消息 + 引擎标识。
func (e *FakeEngine) Chat(ctx context.Context, req ChatRequest, chunkFn func(ChatChunk) error) (*ChatChunk, error) {
	if !e.loaded {
		return nil, fmt.Errorf("fake 引擎未加载模型")
	}
	userMsg := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userMsg = req.Messages[i].Content
			break
		}
	}
	reply := "[fake:" + e.Name() + "] 已收到你的消息：" + truncate(userMsg, 50)
	if req.Stream && chunkFn != nil {
		_ = chunkFn(ChatChunk{ID: "fake-1", Model: req.Model, Content: reply, Done: true})
	}
	return &ChatChunk{ID: "fake-1", Model: req.Model, Content: reply, Done: true}, nil
}

// Health 健康检查。
func (e *FakeEngine) Health(ctx context.Context) error {
	if !e.loaded {
		return fmt.Errorf("fake 引擎未加载")
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
