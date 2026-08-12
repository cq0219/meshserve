package gateway

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/yourorg/meshserve/internal/engine"
	"github.com/yourorg/meshserve/internal/modelrepo"
)

// LocalRouter 单机路由：模型名 → 本机已注册引擎（由 agent 提供）。
// 多副本场景按「活跃请求数」负载均衡：Resolve 返回负载升序的引擎列表（低负载优先）。
// 网关在处理请求前后调用 Acquire/Release 维护并发计数。
type LocalRouter struct {
	mu      sync.RWMutex
	repo    *modelrepo.Repo
	engines map[string][]engine.Engine // modelName → 可用引擎
	load    map[engine.Engine]int64    // engine → 活跃请求数（M3 负载均衡）
}

// NewLocalRouter 创建单机路由。
func NewLocalRouter(repo *modelrepo.Repo) *LocalRouter {
	return &LocalRouter{repo: repo, engines: make(map[string][]engine.Engine), load: make(map[engine.Engine]int64)}
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
	delete(r.load, eng)
	if len(r.engines[modelName]) == 0 {
		delete(r.engines, modelName)
	}
}

// Acquire 请求开始：引擎活跃计数 +1（网关调用）。
func (r *LocalRouter) Acquire(eng engine.Engine) {
	r.mu.Lock()
	r.load[eng]++
	r.mu.Unlock()
}

// Release 请求结束：引擎活跃计数 -1（网关调用）。
func (r *LocalRouter) Release(eng engine.Engine) {
	r.mu.Lock()
	if r.load[eng] > 0 {
		r.load[eng]--
	}
	r.mu.Unlock()
}

// Load 返回指定引擎当前活跃请求数。
func (r *LocalRouter) Load(eng engine.Engine) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.load[eng]
}

// Resolve 返回模型可用的引擎列表，按负载升序（低负载优先，M3 负载均衡）。
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
	// 稳定排序：负载低者优先（同负载保持注册顺序）
	sort.SliceStable(out, func(i, j int) bool { return r.load[out[i]] < r.load[out[j]] })
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
