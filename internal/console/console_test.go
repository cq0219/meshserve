package console

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
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

// TestGPUAPI_Empty 无 GPU 环境返回空数组（200）。
func TestGPUAPI_Empty(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/gpu", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	if body := w.Body.String(); body != "[]\n" {
		t.Errorf("无 GPU 应返回空数组: %q", body)
	}
}

// TestGPUAPI_WithData 注入 fake GPU 数据：校验占用率与显存容量字段。
func TestGPUAPI_WithData(t *testing.T) {
	h := gpuHandler(func() ([]agent.GPUInfo, error) {
		return []agent.GPUInfo{
			{Name: "NVIDIA RTX 4090", VRAMTotal: 24 << 30, VRAMUsed: 18 << 30, UtilPct: 75},
			{Name: "NVIDIA RTX 4090", VRAMTotal: 24 << 30, VRAMUsed: 6 << 30, UtilPct: 12},
		}, nil
	})
	req := httptest.NewRequest(http.MethodGet, "/api/gpu", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	body := w.Body.String()
	for _, want := range []string{
		`"name":"NVIDIA RTX 4090"`,
		`"vram_total":25769803776`,
		`"vram_used":19327352832`,
		`"util_pct":75`,
		`"util_pct":12`,
	} {
		if !contains(body, want) {
			t.Errorf("GPU 响应缺少 %s: %s", want, body)
		}
	}
}

// TestInstancesAPI_MultiNode 集群级实例聚合（M4）：本机实例 + 远端节点实例。
func TestInstancesAPI_MultiNode(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, err := raftstore.Open(dir)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.ModelsDir = dir + "/models"

	// ---- 本机成员 + agent（1 个实例）----
	self, err := cluster.New(t.Context(), cluster.Options{
		NodeID: "self-node", Role: "bootstrap", BindAddr: "127.0.0.1", BindPort: 0,
		EnableTLS: false, Logger: log,
	})
	if err != nil {
		t.Fatalf("self cluster 失败: %v", err)
	}
	t.Cleanup(func() { _ = self.Shutdown() })
	ag := agent.New(cfg, log)
	_, err = ag.DeployInstance(t.Context(), "inst-local", "m-local", agent.DeploySpec{
		ModelPath: "/models/m-local", Engine: "fake",
	})
	if err != nil {
		t.Fatalf("部署本机实例失败: %v", err)
	}

	// ---- 远端节点：httptest 模拟其 /api/instances（2 个实例）----
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []agent.Instance{
			{ID: "inst-remote-1", ModelName: "m-r1", Engine: "fake", State: agent.InstReady},
			{ID: "inst-remote-2", ModelName: "m-r2", Engine: "vllm", State: agent.InstLoading},
		})
	}))
	defer ts.Close()
	tsPort := ts.Listener.Addr().(*net.TCPAddr).Port

	// 本节点实际 gossip 地址（Options.BindPort=0 时为动态端口，需从成员表获取）
	var selfAddr string
	for _, m := range self.Members() {
		if m.ID == "self-node" {
			selfAddr = fmt.Sprintf("%s:%d", m.Addr, m.Port)
		}
	}
	if selfAddr == "" {
		t.Fatal("成员表中未找到本节点地址")
	}

	remote, err := cluster.New(t.Context(), cluster.Options{
		NodeID:    "remote-node",
		Role:      "member",
		BindAddr:  "127.0.0.1",
		BindPort:  0,
		JoinAddr:  selfAddr,
		EnableTLS: false,
		Tags:      map[string]string{"console_port": itoa(uint16(tsPort))},
		Logger:    log,
	})
	if err != nil {
		t.Fatalf("remote cluster 失败: %v", err)
	}
	t.Cleanup(func() { _ = remote.Shutdown() })

	// ---- 控制台（self 视角）----
	repo, err := modelrepo.New(store, cfg.ModelsDir, log)
	if err != nil {
		t.Fatalf("repo 失败: %v", err)
	}
	h, err := Handler(store, self, repo, ag)
	if err != nil {
		t.Fatalf("Handler 失败: %v", err)
	}

	// 等待成员收敛 + 标签扩散
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		for _, m := range self.Members() {
			if m.ID == "remote-node" && m.Tags["console_port"] != "" {
				ok = true
			}
		}
		if ok {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	body := w.Body.String()
	// 本机实例带 self node_id
	if !contains(body, `"node_id":"self-node"`) || !contains(body, "inst-local") {
		t.Errorf("应包含本机实例+节点标识: %s", body)
	}
	// 远端实例带 remote node_id
	if !contains(body, `"node_id":"remote-node"`) || !contains(body, "inst-remote-1") || !contains(body, "inst-remote-2") {
		t.Errorf("应包含远端实例+节点标识: %s", body)
	}
	t.Logf("聚合实例视图: %s", body)
}

func itoa(n uint16) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
