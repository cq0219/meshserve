// Agent 管理 HTTP API：供集群控制面（任意节点）远程部署/停止/查询实例。
// 监听于 agent.rpc_addr（默认 0.0.0.0:9100），与健康探针共用端口。
package agent

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// DeployRequest 远端部署请求（控制面 → 目标节点 agent）。
type DeployRequest struct {
	InstanceID string     `json:"instance_id"`
	ModelName  string     `json:"model_name"`
	Spec       DeploySpec `json:"spec"`
}

// StopRequest 远端停止请求。
type StopRequest struct {
	InstanceID string `json:"instance_id"`
}

// Handler 构造 agent 管理 HTTP handler。
func Handler(a *Agent, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()

	// liveness：进程存活
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// 部署实例（阻塞至就绪或超时；PP worker 仅拉起进程）
	mux.HandleFunc("POST /api/deploy", func(w http.ResponseWriter, r *http.Request) {
		var req DeployRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
			return
		}
		if req.InstanceID == "" || req.ModelName == "" {
			writeErr(w, http.StatusBadRequest, "instance_id 与 model_name 必填")
			return
		}
		inst, err := a.DeployInstance(r.Context(), req.InstanceID, req.ModelName, req.Spec)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, inst)
	})

	// 停止实例
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		var req StopRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
			return
		}
		if req.InstanceID == "" {
			writeErr(w, http.StatusBadRequest, "instance_id 必填")
			return
		}
		if err := a.StopInstance(r.Context(), req.InstanceID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"instance_id": req.InstanceID, "state": "stopped"})
	})

	// 实例列表
	mux.HandleFunc("GET /api/instances", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, a.ListInstances())
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
