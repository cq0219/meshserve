// Package console 提供 Web 控制台：REST API + 内嵌前端（Go embed，单二进制）。
// 对应架构方案 M2 交付：集群总览、节点管理、模型管理、实例监控。
package console

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
)

//go:embed web/*
var webFS embed.FS

// Handler 返回控制台 HTTP 处理器（API + 静态资源）。
func Handler(store *raftstore.Store, members *cluster.Manager, repo *modelrepo.Repo, ag *agent.Agent) (http.Handler, error) {
	mux := http.NewServeMux()

	// API
	mux.HandleFunc("GET /api/status", statusHandler(store, members))
	mux.HandleFunc("GET /api/nodes", nodesHandler(members))
	mux.HandleFunc("GET /api/models", modelsHandler(repo))
	mux.HandleFunc("GET /api/instances", instancesHandler(ag))

	// 静态资源（内嵌前端）
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", fileServer)

	return mux, nil
}

// ---------- API Handlers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// statusHandler 集群整体状态。
func statusHandler(store *raftstore.Store, members *cluster.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clusterID, _ := store.ClusterID()
		nodes := members.Members()
		online := 0
		for _, n := range nodes {
			if n.State == cluster.StateAlive {
				online++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"cluster_id":   clusterID,
			"node_count":   len(nodes),
			"node_online":  online,
			"leader":       store.Leader(),
			"self_node_id": members.Self().ID,
		})
	}
}

// nodesHandler 节点列表。
func nodesHandler(members *cluster.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes := members.Members()
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
		writeJSON(w, http.StatusOK, nodes)
	}
}

// modelsHandler 模型列表。
func modelsHandler(repo *modelrepo.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := repo.List(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, models)
	}
}

// instancesHandler 本机实例列表。
func instancesHandler(ag *agent.Agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ag.ListInstances())
	}
}
