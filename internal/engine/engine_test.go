package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestFakeEngine_Chat 验证 fake 引擎完整对话流程。
func TestFakeEngine_Chat(t *testing.T) {
	e := &FakeEngine{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := e.Load(ctx, LoadConfig{ModelPath: "/models/qwen"}); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	req := ChatRequest{
		Model:    "qwen",
		Messages: []ChatMessage{{Role: "user", Content: "你好，世界"}},
		Stream:   true,
	}
	var chunks []ChatChunk
	chunk, err := e.Chat(ctx, req, func(c ChatChunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if chunk == nil || !chunk.Done {
		t.Error("期望 done=true")
	}
	if !strings.Contains(chunk.Content, "你好") {
		t.Errorf("回复应包含用户消息，实际: %q", chunk.Content)
	}
	if len(chunks) != 1 {
		t.Errorf("期望 1 个流式 chunk，实际 %d", len(chunks))
	}
	if err := e.Health(ctx); err != nil {
		t.Errorf("Health 应通过: %v", err)
	}
}

// TestFakeEngine_NotLoaded 未加载时 Chat 应报错。
func TestFakeEngine_NotLoaded(t *testing.T) {
	e := &FakeEngine{}
	if _, err := e.Chat(context.Background(), ChatRequest{}, nil); err == nil {
		t.Fatal("未加载模型时应返回错误")
	}
}

// TestRegistry_UnknownFallsBack 未知引擎回退 fake。
func TestRegistry_UnknownFallsBack(t *testing.T) {
	e := Create("not-exist", Options{})
	if e.Name() != "fake" {
		t.Errorf("未知引擎应回退 fake，实际 %s", e.Name())
	}
}

// TestRegistered 已注册引擎包含 fake/vllm。
func TestRegistered(t *testing.T) {
	names := Registered()
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	for _, want := range []string{"fake", "vllm", "llamacpp"} {
		if !found[want] {
			t.Errorf("缺少已注册引擎: %s（现有: %v）", want, names)
		}
	}
}

// TestFakeEngine_Shard 分片参数透传（M3）：tp/pp/quant 记录并可通过 Shard 展示。
func TestFakeEngine_Shard(t *testing.T) {
	e := &FakeEngine{}
	ctx := context.Background()
	err := e.Load(ctx, LoadConfig{
		ModelPath:      "/models/qwen",
		TensorParallel: 4,
		Quant:          "int8",
		VRAMQuotaBytes: 20 << 30,
		Extra:          map[string]string{"pipeline_parallel": "2"},
	})
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if e.tp != 4 || e.pp != 2 || e.quant != "int8" {
		t.Errorf("分片参数记录错误: tp=%d pp=%d quant=%s", e.tp, e.pp, e.quant)
	}
	if got := e.Shard(); got != "tp=4/pp=2/int8" {
		t.Errorf("Shard 描述错误: %s", got)
	}
}

// TestFakeEngine_ShardDefault 未指定分片时默认 tp=1/pp=0/fp16。
func TestFakeEngine_ShardDefault(t *testing.T) {
	e := &FakeEngine{}
	ctx := context.Background()
	_ = e.Load(ctx, LoadConfig{ModelPath: "/models/x", Quant: "fp16"})
	if e.tp != 0 {
		t.Errorf("默认 tp 应为 0，实际 %d", e.tp)
	}
}
