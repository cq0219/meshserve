// Package agent 实现节点代理：资源采集、推理实例生命周期管理、自愈。
// 对应方案 4.3 节点代理模块。
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/engine"
	"github.com/yourorg/meshserve/internal/raftstore"
)

// GPUInfo GPU 资源信息。
type GPUInfo struct {
	Name      string `json:"name"`
	VRAMTotal uint64 `json:"vram_total"`
	VRAMUsed  uint64 `json:"vram_used"`
	UtilPct   int    `json:"util_pct"`
}

// InstanceState 推理实例状态。
type InstanceState string

const (
	// InstLoading 加载中
	InstLoading InstanceState = "loading"
	// InstReady 就绪
	InstReady InstanceState = "ready"
	// InstError 错误
	InstError InstanceState = "error"
	// InstStopped 已停止
	InstStopped InstanceState = "stopped"
)

// Instance 推理实例。
type Instance struct {
	ID        string        `json:"id"`
	ModelName string        `json:"model_name"`
	Engine    string        `json:"engine"`
	State     InstanceState `json:"state"`
	Addr      string        `json:"addr,omitempty"`
	VRAMUsed  uint64        `json:"vram_used,omitempty"`
	// TensorParallel / PipelineParallel / Quant 部署分片信息（自愈恢复时复用，M3）
	TensorParallel   int       `json:"tensor_parallel,omitempty"`
	PipelineParallel int       `json:"pipeline_parallel,omitempty"`
	Quant            string    `json:"quant,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	LastError        string    `json:"last_error,omitempty"`
}

// DeploySpec 实例部署规格（由调度器决策/恢复流程传入，M3 分片）。
type DeploySpec struct {
	// ModelPath 模型权重路径
	ModelPath string
	// Engine 引擎名：vllm|sglang|llamacpp|fake
	Engine string
	// VRAMQuota 显存配额（字节）
	VRAMQuota uint64
	// TensorParallel 张量并行大小（同机多卡）
	TensorParallel int
	// PipelineParallel 流水线并行大小
	PipelineParallel int
	// Quant 量化档位：fp16|bf16|int8|int4
	Quant string
}

// Agent 节点代理。
type Agent struct {
	mu        sync.Mutex
	cfg       *config.Config
	log       *slog.Logger
	instances map[string]*Instance
	engines   map[string]engine.Engine // instanceID → engine
	stop      chan struct{}
}

// New 创建节点代理。
func New(cfg *config.Config, log *slog.Logger) *Agent {
	return &Agent{
		cfg:       cfg,
		log:       log,
		instances: make(map[string]*Instance),
		engines:   make(map[string]engine.Engine),
		stop:      make(chan struct{}),
	}
}

// CollectGPU 采集 GPU 信息（nvidia-smi；不可用时返回空列表 + 错误）。
func CollectGPU() ([]GPUInfo, error) {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total,memory.used,utilization.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, errors.New("nvidia-smi 不可用（无 NVIDIA GPU 或驱动未安装）")
	}
	var gpus []GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		g := GPUInfo{Name: strings.TrimSpace(parts[0])}
		// 解析失败保持零值，不中断采集
		_, _ = fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &g.VRAMTotal)
		_, _ = fmt.Sscanf(strings.TrimSpace(parts[2]), "%d", &g.VRAMUsed)
		_, _ = fmt.Sscanf(strings.TrimSpace(parts[3]), "%d", &g.UtilPct)
		g.VRAMTotal *= 1024 * 1024 // MiB → bytes
		g.VRAMUsed *= 1024 * 1024
		gpus = append(gpus, g)
	}
	return gpus, nil
}

// DeployInstance 部署并启动推理实例（阻塞至就绪或超时）。
// spec 携带模型路径、引擎、显存配额与分片参数（TP/PP/量化，M3）。
func (a *Agent) DeployInstance(ctx context.Context, id, modelName string, spec DeploySpec) (*Instance, error) {
	a.mu.Lock()
	if _, exists := a.instances[id]; exists {
		a.mu.Unlock()
		return nil, fmt.Errorf("实例 %s 已存在", id)
	}
	inst := &Instance{
		ID: id, ModelName: modelName, Engine: spec.Engine, State: InstLoading,
		TensorParallel: spec.TensorParallel, PipelineParallel: spec.PipelineParallel, Quant: spec.Quant,
		VRAMUsed: spec.VRAMQuota, StartedAt: time.Now(),
	}
	a.instances[id] = inst
	a.mu.Unlock()

	a.log.Info("部署实例", "id", id, "model", modelName, "engine", spec.Engine,
		"tp", spec.TensorParallel, "pp", spec.PipelineParallel, "quant", spec.Quant, "path", spec.ModelPath)
	eng := engine.Create(spec.Engine, engine.Options{HTTPAddr: defaultEngineAddr(spec.Engine)})
	if err := eng.Load(ctx, engine.LoadConfig{
		ModelPath:      spec.ModelPath,
		TensorParallel: spec.TensorParallel,
		Quant:          spec.Quant,
		VRAMQuotaBytes: spec.VRAMQuota,
		Extra: map[string]string{
			"pipeline_parallel": fmt.Sprintf("%d", spec.PipelineParallel),
		},
	}); err != nil {
		inst.State = InstError
		inst.LastError = err.Error()
		return inst, err
	}
	a.mu.Lock()
	a.engines[id] = eng
	inst.State = InstReady
	inst.Addr = eng.Addr()
	a.mu.Unlock()
	a.log.Info("实例就绪", "id", id, "addr", inst.Addr)
	return inst, nil
}

// StopInstance 停止实例。
func (a *Agent) StopInstance(ctx context.Context, id string) error {
	a.mu.Lock()
	inst, ok := a.instances[id]
	eng := a.engines[id]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("实例 %s 不存在", id)
	}
	delete(a.instances, id)
	delete(a.engines, id)
	a.mu.Unlock()
	if eng != nil {
		_ = eng.Unload(ctx)
	}
	inst.State = InstStopped
	a.log.Info("实例已停止", "id", id)
	return nil
}

// ListInstances 返回全部实例快照。
func (a *Agent) ListInstances() []Instance {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Instance, 0, len(a.instances))
	for _, v := range a.instances {
		cp := *v
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetEngine 获取实例对应引擎（供网关直连调用）。
func (a *Agent) GetEngine(instanceID string) (engine.Engine, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.engines[instanceID]
	return e, ok
}

// StopInstancesByModel 停止指定模型的所有实例（Web 停用/删除时调用）。
func (a *Agent) StopInstancesByModel(ctx context.Context, modelName string) error {
	for _, inst := range a.ListInstances() {
		if inst.ModelName == modelName {
			_ = a.StopInstance(ctx, inst.ID)
		}
	}
	return nil
}

// DeployByModel 按模型部署单个实例（Web 注册/启用时调用；fake 立即就绪，vllm 探测直连）。
// 返回实例；部署失败返回错误（模型元数据保留，状态置为 error 由调用方处理）。
func (a *Agent) DeployByModel(ctx context.Context, m *raftstore.Model, path string) (*Instance, error) {
	id := "inst-" + m.Name + "-web"
	if err := a.StopInstance(ctx, id); err != nil {
		// 忽略：实例不存在也继续
	}
	spec := DeploySpec{
		ModelPath:        path,
		Engine:           m.Engine,
		VRAMQuota:        m.VRAMBytes,
		TensorParallel:   m.TensorParallel,
		PipelineParallel: m.PipelineParallel,
		Quant:            m.Quant,
	}
	return a.DeployInstance(ctx, id, m.Name, spec)
}

// HealthCheck 执行本节点健康检查：实例探活 + 自愈（重启异常实例，指数退避）。
func (a *Agent) HealthCheck(ctx context.Context) {
	a.mu.Lock()
	ids := make([]string, 0, len(a.instances))
	for id := range a.instances {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.mu.Lock()
		inst, ok := a.instances[id]
		eng := a.engines[id]
		a.mu.Unlock()
		if !ok {
			continue
		}
		if eng == nil {
			// 引擎引用丢失（异常场景）：尝试重启
			a.log.Warn("实例引擎引用丢失，尝试重启", "id", id)
			_ = a.StopInstance(ctx, id)
			if _, err := a.DeployInstance(ctx, id, inst.ModelName, specOf(inst, a.cfg.ModelsDir)); err != nil {
				a.log.Error("实例重启失败", "id", id, "err", err)
			}
			continue
		}
		if err := eng.Health(ctx); err != nil {
			a.log.Warn("实例健康检查失败，尝试重启", "id", id, "err", err)
			// 自愈：先停后启（幂等）
			_ = a.StopInstance(ctx, id)
			if _, err := a.DeployInstance(ctx, id, inst.ModelName, specOf(inst, a.cfg.ModelsDir)); err != nil {
				a.log.Error("实例重启失败", "id", id, "err", err)
			}
		}
	}
}

// specOf 从实例记录构造部署规格（自愈恢复时复用原分片参数，M3）。
func specOf(inst *Instance, modelsDir string) DeploySpec {
	return DeploySpec{
		ModelPath:        modelFilePath(modelsDir, inst.ModelName),
		Engine:           inst.Engine,
		VRAMQuota:        inst.VRAMUsed,
		TensorParallel:   inst.TensorParallel,
		PipelineParallel: inst.PipelineParallel,
		Quant:            inst.Quant,
	}
}

// StartHealthLoop 周期性健康检查循环（goroutine）。
func (a *Agent) StartHealthLoop(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.stop:
				return
			case <-t.C:
				a.HealthCheck(ctx)
			}
		}
	}()
}

// Shutdown 停止健康循环。
func (a *Agent) Shutdown() { close(a.stop) }

func defaultEngineAddr(name string) string {
	switch name {
	case "vllm", "sglang":
		return "127.0.0.1:8000"
	default:
		return ""
	}
}

// modelFilePath 计算模型目录下的权重路径。
func modelFilePath(modelsDir, modelName string) string {
	return filepath.Join(modelsDir, modelName)
}

// EnsureModelDir 确保模型目录存在。
func EnsureModelDir(modelsDir string) error {
	return os.MkdirAll(modelsDir, 0o755)
}
