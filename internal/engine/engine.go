// Package engine 定义统一推理引擎接口与各引擎实现（vLLM / fake）。
// 对应方案 ADR-04：引擎插件化，上层（agent/gateway）不感知具体引擎差异。
package engine

import (
	"context"
)

// ChatMessage 对话消息（OpenAI 兼容）。
type ChatMessage struct {
	Role    string `json:"role"` // system|user|assistant
	Content string `json:"content"`
}

// ChatRequest 对话请求。
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// ChatChunk 流式响应片段（OpenAI SSE chunk 结构）。
type ChatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content string `json:"content"` // delta.content
	Done    bool   `json:"done"`
}

// LoadConfig 引擎加载参数（由调度器决策传入）。
type LoadConfig struct {
	// ModelPath 模型权重路径
	ModelPath string
	// TensorParallel 张量并行大小
	TensorParallel int
	// Quant 量化档位
	Quant string
	// VRAMQuotaBytes 显存配额（字节）
	VRAMQuotaBytes uint64
	// Port 引擎 HTTP 服务端口（0=默认 8000，多实例需显式分配）
	Port int
	// PPRank 流水线并行 rank（0=rank0 暴露 API；>0=worker 仅参与计算）
	PPRank int
	// PPTotal 流水线并行总大小（<=1 表示无 PP）
	PPTotal int
	// DistributedBackend 跨节点并行后端：ray|mp
	DistributedBackend string
	// Extra 引擎附加参数（透传）
	Extra map[string]string
}

// Engine 统一推理引擎接口。
type Engine interface {
	// Name 返回引擎标识（vllm/sglang/llamacpp/fake）。
	Name() string
	// Load 加载模型（阻塞至就绪或超时）。
	Load(ctx context.Context, cfg LoadConfig) error
	// Unload 卸载模型，释放显存。
	Unload(ctx context.Context) error
	// Chat 执行一次对话（stream=true 时通过 chunkFn 回调流式片段）。
	Chat(ctx context.Context, req ChatRequest, chunkFn func(ChatChunk) error) (*ChatChunk, error)
	// Health 返回引擎健康状态。
	Health(ctx context.Context) error
	// Addr 返回引擎对外服务地址（供网关直连）。
	Addr() string
}

// Registry 引擎注册表：name → 工厂函数。
var registry = map[string]func(opts Options) Engine{}

// Options 引擎通用配置。
type Options struct {
	// HTTPAddr 引擎 HTTP 服务地址（vLLM 场景）
	HTTPAddr string
	// Command 引擎可执行命令（llamacpp 场景）
	Command string
	// Args 引擎启动参数
	Args []string
}

// Register 注册引擎实现（init 时调用）。
func Register(name string, factory func(Options) Engine) {
	registry[name] = factory
}

// Create 按名称创建引擎实例。
func Create(name string, opts Options) Engine {
	if f, ok := registry[name]; ok {
		return f(opts)
	}
	// 未知引擎回退 fake（保证系统可用，日志告警由上层记录）
	return &FakeEngine{}
}

// Registered 返回已注册引擎列表。
func Registered() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}
