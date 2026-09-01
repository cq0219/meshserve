package console

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/raftstore"
	"github.com/yourorg/meshserve/internal/scheduler"
)

// modelReq Web 模型注册/编辑请求体。
type modelReq struct {
	Name        string  `json:"name"`
	Engine      string  `json:"engine"`
	Path        string  `json:"path"`
	Endpoint    string  `json:"endpoint"`
	Description string  `json:"description"`
	Version     string  `json:"version"`
	Quant       string  `json:"quant"`
	Params      float64 `json:"params"`
	VRAM        uint64  `json:"vram"`
	TP          int     `json:"tp"`
	PP          int     `json:"pp"`
	Port        int     `json:"port"`
	Replicas    int     `json:"replicas"`
}

// validEngines 合法引擎集合。
var validEngines = map[string]bool{"fake": true, "vllm": true, "sglang": true, "llamacpp": true}

// registerModelHandler 注册模型：校验 → 入库 → 自动部署（PP>1 跨节点编排，其余本机）。
func registerModelHandler(repo *modelrepo.Repo, ag *agent.Agent, members *cluster.Manager,
	ppc *scheduler.PPCoordinator, registerRemote func(modelName, addr string), backend string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req modelReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误: " + err.Error()})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模型 name 不能为空"})
			return
		}
		if !validEngines[req.Engine] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法 engine: " + req.Engine})
			return
		}
		if req.Path == "" && req.Endpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path 与 endpoint 至少填一个"})
			return
		}
		if req.PP > 1 && len(aliveMembers(members, 0)) < req.PP {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "在线节点不足 PP 要求（需要 " + strconv.Itoa(req.PP) + " 个节点）"})
			return
		}
		if _, err := repo.Get(r.Context(), req.Name); err == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "模型已存在: " + req.Name})
			return
		}
		vram := req.VRAM
		if vram == 0 && req.Params > 0 {
			quant := req.Quant
			if quant == "" {
				quant = "fp16"
			}
			vram = raftstore.EstimateVRAM(req.Params, quant)
		}
		m := &raftstore.Model{
			Name:             req.Name,
			Engine:           req.Engine,
			Path:             req.Path,
			Endpoint:         req.Endpoint,
			Description:      req.Description,
			Version:          req.Version,
			Quant:            req.Quant,
			Params:           req.Params,
			VRAMBytes:        vram,
			TensorParallel:   req.TP,
			PipelineParallel: req.PP,
			Port:             req.Port,
			Replicas:         req.Replicas,
			Status:           raftstore.StatusDeploying,
		}
		created, err := repo.RegisterModel(r.Context(), m)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// 自动部署（endpoint 模式无需本机部署）
		if created.Endpoint != "" {
			_ = repo.SetStatus(r.Context(), created.Name, raftstore.StatusOnline)
			created.Status = raftstore.StatusOnline
			writeJSON(w, http.StatusCreated, created)
			return
		}
		if err := deployModel(r.Context(), created, members, ag, ppc, registerRemote, backend); err != nil {
			created.Status = raftstore.StatusError
			created.LastError = err.Error()
			_ = repo.Update(r.Context(), created)
			writeJSON(w, http.StatusCreated, created)
			return
		}
		_ = repo.SetStatus(r.Context(), created.Name, raftstore.StatusOnline)
		created.Status = raftstore.StatusOnline
		writeJSON(w, http.StatusCreated, created)
	}
}

// updateModelHandler 编辑模型元数据；engine/path/quant 等变更时重新部署。
func updateModelHandler(repo *modelrepo.Repo, ag *agent.Agent, members *cluster.Manager,
	ppc *scheduler.PPCoordinator, registerRemote func(modelName, addr string), backend string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		cur, err := repo.Get(r.Context(), name)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "模型不存在: " + name})
			return
		}
		var req modelReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体格式错误: " + err.Error()})
			return
		}
		needRedeploy := false
		if req.Engine != "" && req.Engine != cur.Engine {
			cur.Engine = req.Engine
			needRedeploy = true
		}
		if req.Path != "" && req.Path != cur.Path {
			cur.Path = req.Path
			needRedeploy = true
		}
		if req.Endpoint != "" && req.Endpoint != cur.Endpoint {
			cur.Endpoint = req.Endpoint
			needRedeploy = true
		}
		if req.Description != "" {
			cur.Description = req.Description
		}
		if req.Version != "" {
			cur.Version = req.Version
		}
		if req.Quant != "" && req.Quant != cur.Quant {
			cur.Quant = req.Quant
			needRedeploy = true
		}
		if req.Params > 0 {
			cur.Params = req.Params
			if req.VRAM == 0 && cur.VRAMBytes == 0 {
				cur.VRAMBytes = raftstore.EstimateVRAM(req.Params, cur.Quant)
			}
		}
		if req.VRAM > 0 {
			cur.VRAMBytes = req.VRAM
		}
		if req.TP > 0 {
			cur.TensorParallel = req.TP
			needRedeploy = true
		}
		if req.PP > 0 {
			if req.PP != cur.PipelineParallel {
				cur.PipelineParallel = req.PP
				needRedeploy = true
			}
		}
		if req.Port > 0 {
			cur.Port = req.Port
			needRedeploy = true
		}
		if req.Replicas > 0 {
			cur.Replicas = req.Replicas
		}
		if err := repo.Update(r.Context(), cur); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if needRedeploy && cur.Status != raftstore.StatusDisabled {
			_ = stopModelEverywhere(r.Context(), cur.Name, members, ag)
			_ = repo.SetStatus(r.Context(), cur.Name, raftstore.StatusDeploying)
			if cur.Endpoint != "" {
				_ = repo.SetStatus(r.Context(), cur.Name, raftstore.StatusOnline)
				cur.Status = raftstore.StatusOnline
			} else if err := deployModel(r.Context(), cur, members, ag, ppc, registerRemote, backend); err != nil {
				_ = repo.SetStatus(r.Context(), cur.Name, raftstore.StatusError)
				cur.Status = raftstore.StatusError
			} else {
				_ = repo.SetStatus(r.Context(), cur.Name, raftstore.StatusOnline)
				cur.Status = raftstore.StatusOnline
			}
		}
		writeJSON(w, http.StatusOK, cur)
	}
}

// toggleModelHandler 停用/启用模型。
func toggleModelHandler(repo *modelrepo.Repo, ag *agent.Agent, members *cluster.Manager,
	ppc *scheduler.PPCoordinator, registerRemote func(modelName, addr string), backend string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		m, err := repo.Get(r.Context(), name)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "模型不存在: " + name})
			return
		}
		if m.Status == raftstore.StatusDisabled {
			// 启用：重新部署
			_ = repo.SetStatus(r.Context(), name, raftstore.StatusDeploying)
			if m.Endpoint != "" {
				_ = repo.SetStatus(r.Context(), name, raftstore.StatusOnline)
				m.Status = raftstore.StatusOnline
			} else if err := deployModel(r.Context(), m, members, ag, ppc, registerRemote, backend); err != nil {
				_ = repo.SetStatus(r.Context(), name, raftstore.StatusError)
				m.Status = raftstore.StatusError
			} else {
				_ = repo.SetStatus(r.Context(), name, raftstore.StatusOnline)
				m.Status = raftstore.StatusOnline
			}
			writeJSON(w, http.StatusOK, m)
			return
		}
		// 停用：停止全部实例（本机 + 远端）
		_ = stopModelEverywhere(r.Context(), name, members, ag)
		_ = repo.SetStatus(r.Context(), name, raftstore.StatusDisabled)
		m.Status = raftstore.StatusDisabled
		writeJSON(w, http.StatusOK, m)
	}
}

// deleteModelHandler 删除模型（先停止本机与远端实例）。
func deleteModelHandler(repo *modelrepo.Repo, ag *agent.Agent, members *cluster.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, err := repo.Get(r.Context(), name); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "模型不存在: " + name})
			return
		}
		_ = stopModelEverywhere(r.Context(), name, members, ag)
		if err := repo.Delete(r.Context(), name, false); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// deriveModelStatus 从实例状态推导模型运行状态（disabled 保持手动停用；
// endpoint 外部模型无本地实例，保持已存状态，默认视为在线）。
func deriveModelStatus(m *raftstore.Model, insts []agent.Instance) string {
	if m.Status == raftstore.StatusDisabled {
		return raftstore.StatusDisabled
	}
	if m.Endpoint != "" {
		if m.Status == "" {
			return raftstore.StatusOnline
		}
		return m.Status
	}
	var ready, loading, failed bool
	for _, i := range insts {
		if i.ModelName != m.Name {
			continue
		}
		switch i.State {
		case agent.InstReady:
			ready = true
		case agent.InstLoading:
			loading = true
		case agent.InstError:
			failed = true
		}
	}
	switch {
	case ready:
		return raftstore.StatusOnline
	case loading:
		return raftstore.StatusDeploying
	case failed:
		return raftstore.StatusError
	default:
		return raftstore.StatusOffline
	}
}

// modelsHandler 模型列表（增强：状态推导 + 名称/引擎/状态筛选）。
func modelsHandler(repo *modelrepo.Repo, ag *agent.Agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := repo.List(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		insts := ag.ListInstances()
		q := r.URL.Query().Get("q")
		engine := r.URL.Query().Get("engine")
		status := r.URL.Query().Get("status")
		out := make([]*raftstore.Model, 0, len(models))
		for _, m := range models {
			if q != "" && !strings.Contains(m.Name, q) {
				continue
			}
			if engine != "" && m.Engine != engine {
				continue
			}
			m.Status = deriveModelStatus(m, insts)
			if status != "" && m.Status != status {
				continue
			}
			out = append(out, m)
		}
		writeJSON(w, http.StatusOK, out)
	}
}
