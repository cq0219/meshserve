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
	// Path 模型权重路径（本地目录）
	Path string `json:"path"`
	// Engine 推荐引擎：vllm|sglang|llamacpp|fake
	Engine string `json:"engine"`
	// Quant 量化档位：fp16|bf16|int8|int4
	Quant string `json:"quant"`
	// VRAMBytes 预估显存需求（含 KV cache 预留），调度器据此放置
	VRAMBytes uint64 `json:"vram_bytes"`
	// TensorParallel 张量并行大小
	TensorParallel int `json:"tensor_parallel"`
	// Replicas 目标副本数
	Replicas int `json:"replicas"`
	// CreatedAt 创建时间（RFC3339）
	CreatedAt string `json:"created_at"`
	// Source 来源：local|huggingface
	Source string `json:"source"`
	// SHA256 权重校验和（防损坏）
	SHA256 string `json:"sha256,omitempty"`
}

// Encode 序列化为 JSON。
func (m *Model) Encode() ([]byte, error) { return json.Marshal(m) }

// DecodeModel 解析模型 JSON。
func DecodeModel(data []byte) (*Model, error) {
	m := &Model{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("模型元数据格式错误: %w", err)
	}
	return m, nil
}

// Validate 校验模型元数据必填字段。
func (m *Model) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("模型 name 不能为空")
	}
	if m.Path == "" && m.Source != "huggingface" {
		return fmt.Errorf("模型 path 不能为空（或指定 source=huggingface）")
	}
	if m.VRAMBytes == 0 {
		return fmt.Errorf("模型 vram_bytes 必须大于 0（调度依赖）")
	}
	return nil
}

// EstimateVRAM 粗估算模型显存需求（未显式配置时）。
// 权重字节数 ≈ 参数量 × 每参数字节数（按量化），KV cache 按 max_seq_len × batch 预留。
func EstimateVRAM(paramBillions float64, quant string) uint64 {
	bytesPerParam := map[string]float64{"fp16": 2, "bf16": 2, "int8": 1, "int4": 0.5}
	bpp, ok := bytesPerParam[quant]
	if !ok {
		bpp = 2
	}
	weights := paramBillions * 1e9 * bpp
	// KV cache 与工作区粗估：约占权重 30%
	return uint64(weights * 1.3)
}
