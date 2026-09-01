package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return Handler(newTestAgent(t), nil)
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w, out
}

// TestAgentAPI_Health 健康检查返回 200 ok。
func TestAgentAPI_Health(t *testing.T) {
	h := newTestHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q, want 200 ok", w.Code, w.Body.String())
	}
}

// TestAgentAPI_DeployStop 部署→查询→停止→查询全流程（fake 引擎立即就绪）。
func TestAgentAPI_DeployStop(t *testing.T) {
	h := newTestHandler(t)

	spec := DeploySpec{
		ModelPath:        "/models/qwen3-8b",
		Engine:           "fake",
		TensorParallel:   1,
		PipelineParallel: 2,
		PPRank:           0,
		Port:             8000,
		Args:             []string{"--max-model-len", "8192"},
	}
	w, out := doJSON(t, h, http.MethodPost, "/api/deploy", DeployRequest{
		InstanceID: "inst-pp0", ModelName: "qwen3-8b", Spec: spec,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy = %d, want 201 (%v)", w.Code, out)
	}
	if out["id"] != "inst-pp0" || out["state"] != "ready" {
		t.Fatalf("deploy 返回异常: %v", out)
	}
	// rank0 的 pp_rank=0 被 omitempty 省略，port 与 args 应透传
	if out["port"] != float64(8000) {
		t.Fatalf("port 未透传: %v", out)
	}
	args, _ := out["args"].([]any)
	if len(args) != 2 || args[0] != "--max-model-len" {
		t.Fatalf("args 未透传: %v", out)
	}

	// 实例列表应包含新实例
	w, out = doJSON(t, h, http.MethodGet, "/api/instances", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("instances = %d, want 200", w.Code)
	}
	var insts []Instance
	if err := json.Unmarshal(w.Body.Bytes(), &insts); err != nil {
		t.Fatalf("解析实例列表失败: %v", err)
	}
	if len(insts) != 1 || insts[0].ID != "inst-pp0" {
		t.Fatalf("实例列表异常: %+v", insts)
	}

	// 停止
	w, out = doJSON(t, h, http.MethodPost, "/api/stop", StopRequest{InstanceID: "inst-pp0"})
	if w.Code != http.StatusOK {
		t.Fatalf("stop = %d, want 200 (%v)", w.Code, out)
	}

	// 停止后列表为空
	w, _ = doJSON(t, h, http.MethodGet, "/api/instances", nil)
	insts = nil
	_ = json.Unmarshal(w.Body.Bytes(), &insts)
	if len(insts) != 0 {
		t.Fatalf("停止后实例列表应为空: %+v", insts)
	}

	// 停止不存在实例 → 500
	w, _ = doJSON(t, h, http.MethodPost, "/api/stop", StopRequest{InstanceID: "nope"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("stop 不存在实例 = %d, want 500", w.Code)
	}
}

// TestAgentAPI_DeployValidation 参数缺失返回 400。
func TestAgentAPI_DeployValidation(t *testing.T) {
	h := newTestHandler(t)

	cases := []map[string]any{
		{},                              // 全缺
		{"model_name": "m", "spec": map[string]any{"engine": "fake"}}, // 缺 instance_id
		{"instance_id": "i", "spec": map[string]any{"engine": "fake"}}, // 缺 model_name
	}
	for i, c := range cases {
		w, out := doJSON(t, h, http.MethodPost, "/api/deploy", c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("case %d: deploy = %d, want 400 (%v)", i, w.Code, out)
		}
	}

	// 非法 JSON → 400
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", strings.NewReader("{not-json"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON = %d, want 400", w.Code)
	}
}

// TestAgentClient 客户端往返：httptest 起 agent server，用 Client 远程部署/停止/查询。
func TestAgentClient(t *testing.T) {
	h := newTestHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	c := NewClient(addr)
	ctx := context.Background()

	// 健康
	if err := c.Health(ctx); err != nil {
		t.Fatalf("Health = %v", err)
	}

	// 部署
	err := c.Deploy(ctx, DeployRequest{
		InstanceID: "inst-remote",
		ModelName:  "qwen3-8b",
		Spec:       DeploySpec{ModelPath: "/models/qwen3-8b", Engine: "fake", PPRank: 1, PipelineParallel: 2},
	})
	if err != nil {
		t.Fatalf("Deploy = %v", err)
	}

	// 查询
	insts, err := c.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances = %v", err)
	}
	if len(insts) != 1 || insts[0].PPRank != 1 || insts[0].PipelineParallel != 2 {
		t.Fatalf("远端实例异常: %+v", insts)
	}

	// 停止
	if err := c.Stop(ctx, "inst-remote"); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	insts, _ = c.Instances(ctx)
	if len(insts) != 0 {
		t.Fatalf("停止后实例应为空: %+v", insts)
	}

	// 部署错误透传（缺 model_name）
	err = c.Deploy(ctx, DeployRequest{InstanceID: "x"})
	if err == nil || !strings.Contains(err.Error(), "model_name") {
		t.Fatalf("Deploy 缺参应报错: %v", err)
	}
}
