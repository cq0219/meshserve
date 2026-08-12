package console

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
	"log/slog"
	"os"
)

// 构造控制台处理器（fake 组件）。
func testHandler(t *testing.T) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, err := raftstore.Open(dir)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_ = store.SetClusterMeta("cluster-test", "tok")

	cfg := config.Default()
	cfg.DataDir = dir
	cfg.ModelsDir = dir + "/models"
	repo, err := modelrepo.New(store, cfg.ModelsDir, log)
	if err != nil {
		t.Fatalf("repo 失败: %v", err)
	}
	// 注册一个模型
	if err := os.MkdirAll(cfg.ModelsDir+"/qwen", 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = repo.RegisterLocal(t.Context(), "qwen", cfg.ModelsDir+"/qwen", "fake", "fp16", 1<<30)
	if err != nil {
		t.Fatalf("注册模型失败: %v", err)
	}

	// 成员管理器（本机自环）
	members, err := cluster.New(t.Context(), cluster.Options{
		NodeID: "console-node", Role: "bootstrap", BindAddr: "127.0.0.1", BindPort: 0,
		EnableTLS: false, Logger: log,
	})
	if err != nil {
		t.Fatalf("cluster 失败: %v", err)
	}
	t.Cleanup(func() { _ = members.Shutdown() })

	ag := agent.New(cfg, log)

	h, err := Handler(store, members, repo, ag)
	if err != nil {
		t.Fatalf("Handler 失败: %v", err)
	}
	return h
}

// TestStatusAPI 集群状态 API。
func TestStatusAPI(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	body := w.Body.String()
	if body == "" || body == "{}" {
		t.Errorf("状态响应异常: %s", body)
	}
	t.Logf("status: %s", body)
}

// TestModelsAPI 模型列表 API。
func TestModelsAPI(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	if !contains(w.Body.String(), "qwen") {
		t.Errorf("模型列表应包含 qwen: %s", w.Body.String())
	}
}

// TestNodesAPI 节点 API。
func TestNodesAPI(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	if !contains(w.Body.String(), "console-node") {
		t.Errorf("节点列表应包含本节点: %s", w.Body.String())
	}
}

// TestInstancesAPI 实例 API。
func TestInstancesAPI(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
}

// TestIndexServed 前端首页可访问。
func TestIndexServed(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("首页应 200，实际 %d", w.Code)
	}
	if !contains(w.Body.String(), "MeshServe 控制台") {
		t.Errorf("首页内容异常: %.120s", w.Body.String())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
