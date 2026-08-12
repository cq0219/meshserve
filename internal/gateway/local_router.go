package gateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/yourorg/meshserve/internal/engine"
	"github.com/yourorg/meshserve/internal/modelrepo"
)

// LocalRouter 单机路由：模型名 → 本机已注册引擎（由 agent 提供）。
// 满足 Router 接口；多副本/跨节点路由由集群版 Router 提供（M2）。
type LocalRouter struct {
	mu      sync.RWMutex
	repo    *modelrepo.Repo
	engines map[string][]engine.Engine // modelName → 可用引擎
}

// NewLocalRouter 创建单机路由。
func NewLocalRouter(repo *modelrepo.Repo) *LocalRouter {
	return &LocalRouter{repo: repo, engines: make(map[string][]engine.Engine)}
}

// RegisterEngine 注册模型到引擎映射（agent 部署成功后调用）。
func (r *LocalRouter) RegisterEngine(modelName string, eng engine.Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engines[modelName] = append(r.engines[modelName], eng)
}

// UnregisterEngine 移除映射（实例停止时调用）。
func (r *LocalRouter) UnregisterEngine(modelName string, eng engine.Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.engines[modelName]
	for i, e := range list {
		if e == eng {
			r.engines[modelName] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(r.engines[modelName]) == 0 {
		delete(r.engines, modelName)
	}
}

// Resolve 返回模型可用的引擎列表。
func (r *LocalRouter) Resolve(model string) ([]engine.Engine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	engines := r.engines[model]
	if len(engines) == 0 {
		// 模型已注册但未部署副本
		if _, err := r.repo.Get(context.TODO(), model); err == nil {
			return nil, fmt.Errorf("模型 %q 已注册但未部署实例，请先执行 model deploy", model)
		}
		return nil, fmt.Errorf("模型 %q 不存在", model)
	}
	out := make([]engine.Engine, len(engines))
	copy(out, engines)
	return out, nil
}

// Models 返回已部署模型的列表。
func (r *LocalRouter) Models() ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.engines))
	for m := range r.engines {
		out = append(out, m)
	}
	return out, nil
}
