package console

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
)

// adminEnv 模型管理测试环境：返回 handler + repo + 临时模型目录。
type adminEnv struct {
	h    http.Handler
	repo *modelrepo.Repo
	dir  string
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, err := raftstore.Open(dir)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.ModelsDir = filepath.Join(dir, "models")
	if err := os.MkdirAll(cfg.ModelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := modelrepo.New(store, cfg.ModelsDir, log)
	if err != nil {
		t.Fatalf("repo 失败: %v", err)
	}
	members, err := cluster.New(t.Context(), cluster.Options{
		NodeID: "admin-node", Role: "bootstrap", BindAddr: "127.0.0.1", BindPort: 0,
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
	return &adminEnv{h: h, repo: repo, dir: dir}
}

// makeModelDir 创建模型权重目录。
func (e *adminEnv) makeModelDir(t *testing.T, name string) string {
	d := filepath.Join(e.dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"model":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *adminEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

// jsonPath 将文件路径转义为 JSON 字符串字面量（Windows 反斜杠）。
func jsonPath(p string) string { return strings.ReplaceAll(p, "\\", "\\\\") }

// TestRegisterModelAPI 注册本地 fake 模型 → 201 + online。
func TestRegisterModelAPI(t *testing.T) {
	e := newAdminEnv(t)
	mdir := e.makeModelDir(t, "demo")
	w := e.do(http.MethodPost, "/api/models",
		`{"name":"demo","engine":"fake","path":"`+jsonPath(mdir)+`","quant":"fp16","params":0.5,"description":"demo 模型"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{`"name":"demo"`, `"status":"online"`, `"description":"demo 模型"`} {
		if !contains(w.Body.String(), want) {
			t.Errorf("响应缺少 %s: %s", want, w.Body.String())
		}
	}
}

// TestRegisterModelAPI_Endpoint 注册外部端点 → 201 + online（无需本地部署）。
func TestRegisterModelAPI_Endpoint(t *testing.T) {
	e := newAdminEnv(t)
	w := e.do(http.MethodPost, "/api/models",
		`{"name":"ext","engine":"vllm","endpoint":"http://10.0.0.5:8000/v1","params":8}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d: %s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), `"status":"online"`) {
		t.Errorf("端点模式应直接 online: %s", w.Body.String())
	}
}

// TestRegisterModelAPI_Errors 校验错误：重名/非法引擎/缺路径。
func TestRegisterModelAPI_Errors(t *testing.T) {
	e := newAdminEnv(t)
	mdir := e.makeModelDir(t, "dup")
	// 正常注册
	if w := e.do(http.MethodPost, "/api/models", `{"name":"dup","engine":"fake","path":"`+jsonPath(mdir)+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("首次注册失败: %d %s", w.Code, w.Body.String())
	}
	// 重名 → 409
	if w := e.do(http.MethodPost, "/api/models", `{"name":"dup","engine":"fake","path":"`+jsonPath(mdir)+`"}`); w.Code != http.StatusConflict {
		t.Errorf("重名应 409，实际 %d", w.Code)
	}
	// 非法 engine → 400
	if w := e.do(http.MethodPost, "/api/models", `{"name":"bad","engine":"tpu","path":"`+jsonPath(mdir)+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("非法 engine 应 400，实际 %d", w.Code)
	}
	// path/endpoint 都空 → 400
	if w := e.do(http.MethodPost, "/api/models", `{"name":"no","engine":"fake"}`); w.Code != http.StatusBadRequest {
		t.Errorf("缺路径应 400，实际 %d", w.Code)
	}
	// 路径不存在 → 400
	if w := e.do(http.MethodPost, "/api/models", `{"name":"ghost","engine":"fake","path":"/nope/ghost"}`); w.Code != http.StatusBadRequest {
		t.Errorf("路径不存在应 400，实际 %d", w.Code)
	}
}

// TestToggleModelAPI 停用 → disabled；启用 → online。
func TestToggleModelAPI(t *testing.T) {
	e := newAdminEnv(t)
	mdir := e.makeModelDir(t, "tgl")
	e.do(http.MethodPost, "/api/models", `{"name":"tgl","engine":"fake","path":"`+jsonPath(mdir)+`"}`)
	// 停用
	w := e.do(http.MethodPost, "/api/models/tgl/toggle", "{}")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"status":"disabled"`) {
		t.Fatalf("停用失败: %d %s", w.Code, w.Body.String())
	}
	// 启用
	w = e.do(http.MethodPost, "/api/models/tgl/toggle", "{}")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"status":"online"`) {
		t.Fatalf("启用失败: %d %s", w.Code, w.Body.String())
	}
}

// TestDeleteModelAPI 删除 → 204，列表消失。
func TestDeleteModelAPI(t *testing.T) {
	e := newAdminEnv(t)
	mdir := e.makeModelDir(t, "del")
	e.do(http.MethodPost, "/api/models", `{"name":"del","engine":"fake","path":"`+jsonPath(mdir)+`"}`)
	w := e.do(http.MethodDelete, "/api/models/del", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除应 204，实际 %d", w.Code)
	}
	// 删除后再删 → 404
	if w := e.do(http.MethodDelete, "/api/models/del", ""); w.Code != http.StatusNotFound {
		t.Errorf("删除不存在应 404，实际 %d", w.Code)
	}
	// 列表不含
	w = e.do(http.MethodGet, "/api/models", "")
	if strings.Contains(w.Body.String(), `"name":"del"`) {
		t.Errorf("删除后列表不应包含: %s", w.Body.String())
	}
}

// TestModelsAPI_Filter 列表筛选：q / engine / status。
func TestModelsAPI_Filter(t *testing.T) {
	e := newAdminEnv(t)
	e.makeModelDir(t, "qwen-7b")
	e.makeModelDir(t, "llama-8b")
	e.do(http.MethodPost, "/api/models", `{"name":"qwen-7b","engine":"fake","path":"`+jsonPath(filepath.Join(e.dir, "qwen-7b"))+`"}`)
	e.do(http.MethodPost, "/api/models", `{"name":"llama-8b","engine":"vllm","endpoint":"http://10.0.0.6:8000/v1"}`)

	// q 筛选
	w := e.do(http.MethodGet, "/api/models?q=qwen", "")
	if !contains(w.Body.String(), "qwen-7b") || contains(w.Body.String(), "llama-8b") {
		t.Errorf("q 筛选失败: %s", w.Body.String())
	}
	// engine 筛选
	w = e.do(http.MethodGet, "/api/models?engine=vllm", "")
	if !contains(w.Body.String(), "llama-8b") || contains(w.Body.String(), "qwen-7b") {
		t.Errorf("engine 筛选失败: %s", w.Body.String())
	}
	// status 筛选（两个都应 online）
	w = e.do(http.MethodGet, "/api/models?status=online", "")
	if !contains(w.Body.String(), "qwen-7b") || !contains(w.Body.String(), "llama-8b") {
		t.Errorf("status 筛选失败: %s", w.Body.String())
	}
}
