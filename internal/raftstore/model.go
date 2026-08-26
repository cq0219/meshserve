package raftstore

import (
	"encoding/json"
	"fmt"
)

// Model 模型元数据（对应方案 modelrepo 模块的核心数据模型）。
type Model struct {
	// Name 模型名（唯一，路由与部署使用）
	Name string `json:"name"`
	// Version 模型版本
	Version string `json:"version"`
	// Path 模型权重路径（本地目录，与 Endpoint 二选一）
	Path string `json:"path,omitempty"`
	// Endpoint 外部 OpenAI 兼容 API 地址（与 Path 二选一）
	Endpoint string `json:"endpoint,omitempty"`
	// Engine 推荐引擎：vllm|sglang|llamacpp|fake
	Engine string `json:"engine"`
	// Quant 量化档位：fp16|bf16|int8|int4
	Quant string `json:"quant,omitempty"`
	// VRAMBytes 预估显存需求（含 KV cache 预留），调度器据此放置
	VRAMBytes uint64 `json:"vram_bytes,omitempty"`
	// Params 参数量（十亿，可选，用于显存估算与展示）
	Params float64 `json:"params,omitempty"`
	// TensorParallel 张量并行大小（TP>1 需单节点 GPU 数 ≥ TP，同机多卡）
	TensorParallel int `json:"tensor_parallel,omitempty"`
	// PipelineParallel 流水线并行大小（PP>1 需跨 PP 个节点，每节点一个 stage）
	PipelineParallel int `json:"pipeline_parallel,omitempty"`
	// Replicas 目标副本数
	Replicas int `json:"replicas"`
	// Description 模型描述
	Description string `json:"description,omitempty"`
	// Status 运行状态：online|deploying|offline|error|disabled
	Status string `json:"status,omitempty"`
	// LastError 最近一次部署/运行错误（展示用）
	LastError string `json:"last_error,omitempty"`
	// CreatedAt 创建时间（RFC3339）
	CreatedAt string `json:"created_at,omitempty"`
	// Source 来源：local|huggingface|endpoint
	Source string `json:"source,omitempty"`
	// SHA256 权重校验和（防损坏）
	SHA256 string `json:"sha256,omitempty"`
}

// Encode 序列化为 JSON。
func (m *Model) Encode() ([]byte, error) { return json.Marshal(m) }

// DecodeModel 解析模型 JSON（旧记录缺 Status 时默认 offline，向后兼容）。
func DecodeModel(data []byte) (*Model, error) {
	m := &Model{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("模型元数据格式错误: %w", err)
	}
	if m.Status == "" {
		m.Status = "offline"
	}
	return m, nil
}

// ModelStatus 运行状态枚举。
const (
	StatusOnline    = "online"    // 在线（≥1 实例 ready）
	StatusDeploying = "deploying" // 部署中（实例 loading）
	StatusOffline   = "offline"   // 离线（无实例）
	StatusError     = "error"     // 错误（实例 error）
	StatusDisabled  = "disabled"  // 已停用（手动停用）
)

// Validate 校验模型元数据必填字段。
func (m *Model) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("模型 name 不能为空")
	}
	if m.Path == "" && m.Endpoint == "" {
		return fmt.Errorf("模型 path 与 endpoint 至少填一个")
	}
	if m.Path == "" && m.Source == "local" {
		m.Source = "endpoint"
	}
	switch m.Engine {
	case "", "fake", "vllm", "sglang", "llamacpp":
	default:
		return fmt.Errorf("非法 engine: %q（可选 fake/vllm/sglang/llamacpp）", m.Engine)
	}
	if m.TensorParallel < 0 || m.PipelineParallel < 0 {
		return fmt.Errorf("分片参数不能为负（tp=%d pp=%d）", m.TensorParallel, m.PipelineParallel)
	}
	if m.Replicas == 0 {
		m.Replicas = 1
	}
	return nil
}

// QuantTiers 量化档位（按显存占用升序，供自动降档选择）。
var QuantTiers = []string{"int4", "int8", "bf16", "fp16"}

// bytesPerParam 每参数量化字节数。
var bytesPerParam = map[string]float64{"fp16": 2, "bf16": 2, "int8": 1, "int4": 0.5}

// PickQuant 量化自动选择（M3）：在显存预算内选择满足的量化档位。
// 优先返回 preferred；若预算不足则自动降档（fp16→bf16→int8→int4）；
// 全部不满足时返回 error。
func PickQuant(paramBillions float64, vramBudget uint64, preferred string) (string, error) {
	if _, ok := bytesPerParam[preferred]; !ok {
		preferred = "fp16"
	}
	// QuantTiers 按显存占用升序（int4 < int8 < bf16 < fp16）。
	// 从 preferred 所在档向更低档位（索引递减）降级尝试。
	idx := len(QuantTiers) - 1
	for i, q := range QuantTiers {
		if q == preferred {
			idx = i
			break
		}
	}
	for i := idx; i >= 0; i-- {
		q := QuantTiers[i]
		if EstimateVRAM(paramBillions, q) <= vramBudget {
			return q, nil
		}
	}
	return "", fmt.Errorf("显存预算 %d 字节不足：模型 %0.1fB 最低需 %s（%d 字节）",
		vramBudget, paramBillions, QuantTiers[0], EstimateVRAM(paramBillions, QuantTiers[0]))
}

// EstimateVRAM 粗估算模型显存需求（未显式配置时）。
// 权重字节数 ≈ 参数量 × 每参数字节数（按量化），KV cache 按 max_seq_len × batch 预留。
func EstimateVRAM(paramBillions float64, quant string) uint64 {
	bpp, ok := bytesPerParam[quant]
	if !ok {
		bpp = 2
	}
	weights := paramBillions * 1e9 * bpp
	// KV cache 与工作区粗估：约占权重 30%
	return uint64(weights * 1.3)
}
