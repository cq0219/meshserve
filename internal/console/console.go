// Package console 提供 Web 控制台：REST API + 内嵌前端（Go embed，单二进制）。
// 对应架构方案 M2 交付：集群总览、节点管理、模型管理、实例监控。
// M4 增强：/api/instances 提供集群级实例视图（跨节点聚合）。
package console

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
	"github.com/yourorg/meshserve/internal/scheduler"
)

//go:embed web/*
var webFS embed.FS

// Handler 返回控制台 HTTP 处理器（API + 静态资源）。
// ppc 用于 PP>1 模型跨节点编排；registerRemote 将 PP rank0 地址注册到网关远端路由。
func Handler(store *raftstore.Store, members *cluster.Manager, repo *modelrepo.Repo, ag *agent.Agent,
	ppc *scheduler.PPCoordinator, registerRemote func(modelName, addr string), backend string) (http.Handler, error) {
	mux := http.NewServeMux()

	// API
	mux.HandleFunc("GET /api/status", statusHandler(store, members))
	mux.HandleFunc("GET /api/nodes", nodesHandler(members))
	mux.HandleFunc("GET /api/models", modelsHandler(repo, ag))
	mux.HandleFunc("POST /api/models", registerModelHandler(repo, ag, members, ppc, registerRemote, backend))
	mux.HandleFunc("PATCH /api/models/{name}", updateModelHandler(repo, ag, members, ppc, registerRemote, backend))
	mux.HandleFunc("POST /api/models/{name}/toggle", toggleModelHandler(repo, ag, members, ppc, registerRemote, backend))
	mux.HandleFunc("DELETE /api/models/{name}", deleteModelHandler(repo, ag, members))
	mux.HandleFunc("GET /api/instances", instancesHandler(members, ag))
	mux.HandleFunc("GET /api/gpu", gpuHandler(agent.CollectGPU))

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

// instancesHandler 集群级实例视图（M4）：聚合本机 + 所有在线节点的实例。
// 远端节点通过其 console 端口 HTTP 拉取 /api/instances（标签 console_port 由 gossip 扩散）。
func instancesHandler(members *cluster.Manager, ag *agent.Agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		self := members.Self().ID
		all := []map[string]any{}

		// 1. 本机实例（直读）
		for _, inst := range ag.ListInstances() {
			all = append(all, instanceView(inst, self))
		}

		// 2. 远端在线节点（HTTP 拉取，失败跳过——不阻塞本地视图）
		client := &http.Client{Timeout: 2 * time.Second}
		for _, m := range members.Members() {
			if m.ID == self || m.State != cluster.StateAlive {
				continue
			}
			port := m.Tags["console_port"]
			if port == "" {
				continue
			}
			host := hostOf(m.Addr)
			url := "http://" + host + ":" + port + "/api/instances"
			resp, err := client.Get(url)
			if err != nil {
				continue // 远端 console 未就绪/不可达：跳过
			}
			var remote []agent.Instance
			_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&remote)
			_ = resp.Body.Close()
			for _, inst := range remote {
				all = append(all, instanceView(inst, m.ID))
			}
		}
		writeJSON(w, http.StatusOK, all)
	}
}

// instanceView 实例 + 所属节点 ID（供前端按节点分组展示）。
func instanceView(inst agent.Instance, nodeID string) map[string]any {
	raw, _ := json.Marshal(inst)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	v["node_id"] = nodeID
	return v
}

// hostOf 提取 host:port 中的主机部分。
func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// gpuHandler 本机实时 GPU 监控（M4-2）：每张卡的型号、总显存、已用显存、利用率。
// 每次请求实时执行 nvidia-smi 采集；无 GPU 或采集失败返回空数组（前端显示占位）。
// collect 可注入以便测试。
func gpuHandler(collect func() ([]agent.GPUInfo, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gpus, err := collect()
		if err != nil || len(gpus) == 0 {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, gpus)
	}
}
