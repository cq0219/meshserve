package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/meshserve/internal/engine"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
	"log/slog"
	"os"
	"path/filepath"
)

// 构造带 fake 引擎路由的测试网关。
func testGateway(t *testing.T) (*Gateway, *LocalRouter) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, err := raftstore.Open(dir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	repo, err := modelrepo.New(store, filepath.Join(dir, "models"), log)
	if err != nil {
		t.Fatalf("创建模型仓库失败: %v", err)
	}
	router := NewLocalRouter(repo)
	gw := New(router, NewTokenBucket(0), log)
	return gw, router
}

func deployFake(t *testing.T, router *LocalRouter, model string) {
	t.Helper()
	e := &engine.FakeEngine{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.Load(ctx, engine.LoadConfig{ModelPath: "/models/" + model}); err != nil {
		t.Fatalf("加载引擎失败: %v", err)
	}
	router.RegisterEngine(model, e)
}

// TestChatCompletions_NonStream 非流式对话端点。
func TestChatCompletions_NonStream(t *testing.T) {
	gw, router := testGateway(t)
	deployFake(t, router, "qwen")

	body := `{"model":"qwen","messages":[{"role":"user","content":"测试"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("object 字段错误: %v", resp["object"])
	}
}

// TestChatCompletions_Stream 流式对话端点（SSE）。
func TestChatCompletions_Stream(t *testing.T) {
	gw, router := testGateway(t)
	deployFake(t, router, "qwen")

	body := `{"model":"qwen","messages":[{"role":"user","content":"流式测试"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	out := w.Body.String()
	if !strings.Contains(out, "data:") {
		t.Errorf("流式响应缺少 data: 前缀: %q", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("流式响应缺少 [DONE] 标记")
	}
}

// TestChatCompletions_Validation 请求校验：model 与 messages 必填。
func TestChatCompletions_Validation(t *testing.T) {
	gw, _ := testGateway(t)
	cases := []struct {
		name string
		body string
		code int
	}{
		{"缺 model", `{"messages":[{"role":"user","content":"hi"}]}`, http.StatusBadRequest},
		{"缺 messages", `{"model":"qwen"}`, http.StatusBadRequest},
		{"非法角色", `{"model":"qwen","messages":[{"role":"admin","content":"hi"}]}`, http.StatusBadRequest},
		{"空 body", ``, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(c.body))
			w := httptest.NewRecorder()
			gw.Handler().ServeHTTP(w, req)
			if w.Code != c.code {
				t.Errorf("期望 %d，实际 %d", c.code, w.Code)
			}
		})
	}
}

// TestModels_NotFound 未部署模型返回 404。
func TestModels_NotFound(t *testing.T) {
	gw, _ := testGateway(t)
	body := `{"model":"unknown","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("期望 404，实际 %d", w.Code)
	}
}

// TestModels_List 模型列表端点。
func TestModels_List(t *testing.T) {
	gw, router := testGateway(t)
	deployFake(t, router, "qwen")
	deployFake(t, router, "deepseek")

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "qwen") || !strings.Contains(w.Body.String(), "deepseek") {
		t.Errorf("模型列表缺失: %s", w.Body.String())
	}
}

// TestRateLimit 限流：超过速率返回 429。
func TestRateLimit(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, _ := raftstore.Open(dir)
	t.Cleanup(func() { store.Close() })
	repo, _ := modelrepo.New(store, dir, log)
	router := NewLocalRouter(repo)
	gw := New(router, NewTokenBucket(2), log) // 每秒 2 个

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	limited := false
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		gw.Handler().ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("令牌桶限流未生效（连续 10 请求应出现 429）")
	}
}

// TestRecover_Panic 路由 panic 应被中间件恢复为 500。
func TestRecover_Panic(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, _ := raftstore.Open(dir)
	t.Cleanup(func() { store.Close() })
	repo, _ := modelrepo.New(store, dir, log)
	router := NewLocalRouter(repo)
	gw := New(router, nil, log)

	// 触发 panic 的 handler 包装测试
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := gw.recoverMiddleware(mux)
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("panic 应返回 500，实际 %d", w.Code)
	}
}

var _ = time.Second // keep import
