// Package agent 实现节点代理：资源采集、推理实例生命周期管理、自愈。
// 对应方案 4.3 节点代理模块。
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	// ModelPath 部署时的权重路径（自愈恢复时精确复用，避免误用约定目录）
	ModelPath string `json:"model_path,omitempty"`
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
		VRAMUsed: spec.VRAMQuota, ModelPath: spec.ModelPath, StartedAt: time.Now(),
	}
	a.instances[id] = inst
	a.mu.Unlock()

	a.log.Info("部署实例", "id", id, "model", modelName, "engine", spec.Engine,
		"tp", spec.TensorParallel, "pp", spec.PipelineParallel, "quant", spec.Quant, "path", spec.ModelPath)
	// M6：vLLM 由 MeshServe 拉起进程管理（动态分配端口；已就绪外部服务则复用）
	eng := engine.Create(spec.Engine, engine.Options{
		HTTPAddr:      defaultEngineAddr(spec.Engine),
		VLLMBin:       a.cfg.Agent.VLLMBin,
		VLLMTimeout:   time.Duration(a.cfg.Agent.VLLMTimeoutSeconds) * time.Second,
		VLLMExtraArgs: splitArgs(a.cfg.Agent.VLLMExtraArgs),
		Logger:        a.log,
	})
	if err := eng.Load(ctx, engine.LoadConfig{
		ModelPath:      spec.ModelPath,
		ModelName:      modelName,
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
	// 先停旧实例（不存在则忽略）
	_ = a.StopInstance(ctx, id)
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
		// 仅探活已就绪实例：loading 中的实例由 Load 负责就绪，探测会误判失败触发无谓重启
		if inst.State != InstReady {
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

// specOf 从实例记录构造部署规格（自愈恢复时复用原路径与分片参数，M3）。
func specOf(inst *Instance, modelsDir string) DeploySpec {
	path := inst.ModelPath
	if path == "" {
		path = modelFilePath(modelsDir, inst.ModelName)
	}
	return DeploySpec{
		ModelPath:        path,
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
		// M6：MeshServe 拉起 vLLM 进程，为每个实例动态分配空闲端口
		return "127.0.0.1:" + itoa(freePort())
	default:
		return ""
	}
}

// freePort 获取一个空闲 TCP 端口（动态分配，供拉起引擎进程使用）。
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 8000
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// itoa 整数转字符串（避免 strconv 重复导入）。
func itoa(n int) string { return strconv.Itoa(n) }

// splitArgs 将空格分隔的参数字符串拆分为切片（配置 vllm_extra_args 用）。
func splitArgs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}

// modelFilePath 计算模型目录下的权重路径。
func modelFilePath(modelsDir, modelName string) string {
	return filepath.Join(modelsDir, modelName)
}

// EnsureModelDir 确保模型目录存在。
func EnsureModelDir(modelsDir string) error {
	return os.MkdirAll(modelsDir, 0o755)
}
