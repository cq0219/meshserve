// Package config 定义并加载 MeshServe 配置。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config 是全局配置根节点。所有字段均有合理默认值，遵循「零配置默认」原则。
type Config struct {
	// NodeID 节点 ID（init 时生成，持久化）
	NodeID string `yaml:"node_id" json:"node_id"`
	// ClusterName 集群名
	ClusterName string `yaml:"cluster_name" json:"cluster_name"`
	// DataDir 数据目录（raft 日志、模型元数据、证书等）
	DataDir string `yaml:"data_dir" json:"data_dir"`
	// Log 日志配置
	Log LogConfig `yaml:"log" json:"log"`
	// Cluster 成员管理配置
	Cluster ClusterConfig `yaml:"cluster" json:"cluster"`
	// Gateway 网关配置
	Gateway GatewayConfig `yaml:"gateway" json:"gateway"`
	// Console Web 控制台配置
	Console ConsoleConfig `yaml:"console" json:"console"`
	// Agent 节点代理配置
	Agent AgentConfig `yaml:"agent" json:"agent"`
	// ModelsDir 模型存储根目录
	ModelsDir string `yaml:"models_dir" json:"models_dir"`
}

// ConsoleConfig Web 控制台配置。
type ConsoleConfig struct {
	// HTTPAddr 控制台 HTTP 监听地址
	HTTPAddr string `yaml:"http_addr" json:"http_addr"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Level string `yaml:"level" json:"level"` // debug|info|warn|error
	JSON  bool   `yaml:"json" json:"json"`   // true=JSON 输出
}

// ClusterConfig 成员管理配置。
type ClusterConfig struct {
	// BindAddr 成员通信监听地址
	BindAddr string `yaml:"bind_addr" json:"bind_addr"`
	// BindPort 成员通信端口（memberlist gossip）
	BindPort int `yaml:"bind_port" json:"bind_port"`
	// JoinAddr 初始加入地址（可为空，mDNS 自动发现兜底）
	JoinAddr string `yaml:"join_addr" json:"join_addr"`
	// JoinToken 加入令牌（init 时生成）
	JoinToken string `yaml:"join_token" json:"join_token"`
	// EnableTLS 是否启用节点间 mTLS
	EnableTLS bool `yaml:"enable_tls" json:"enable_tls"`
}

// GatewayConfig 推理网关配置。
type GatewayConfig struct {
	// HTTPAddr 网关 HTTP 监听地址（OpenAI 兼容 API）
	HTTPAddr string `yaml:"http_addr" json:"http_addr"`
	// RateLimit 每秒请求上限（0=不限流）
	RateLimit int `yaml:"rate_limit" json:"rate_limit"`
}

// AgentConfig 节点代理配置。
type AgentConfig struct {
	// RPCAddr 控制面 RPC 监听地址
	RPCAddr string `yaml:"rpc_addr" json:"rpc_addr"`
	// Engine 默认推理引擎：vllm|sglang|llamacpp|fake
	Engine string `yaml:"engine" json:"engine"`
}

// Default 返回内置默认配置。
func Default() *Config {
	return &Config{
		ClusterName: "default",
		DataDir:     filepath.Join(mustHome(), ".meshserve"),
		ModelsDir:   filepath.Join(mustHome(), ".meshserve", "models"),
		Log:         LogConfig{Level: "info", JSON: false},
		Cluster: ClusterConfig{
			BindAddr:  "0.0.0.0",
			BindPort:  7946,
			EnableTLS: true,
		},
		Gateway: GatewayConfig{
			HTTPAddr: "0.0.0.0:8080",
		},
		Console: ConsoleConfig{
			HTTPAddr: "0.0.0.0:8443",
		},
		Agent: AgentConfig{
			RPCAddr: "0.0.0.0:9100",
			Engine:  "vllm",
		},
	}
}

// Validate 校验配置合法性，返回错误信息。
func (c *Config) Validate() error {
	if c.NodeID == "" {
		return fmt.Errorf("node_id 不能为空（请先执行 meshserve init）")
	}
	if c.Cluster.BindPort <= 0 || c.Cluster.BindPort > 65535 {
		return fmt.Errorf("cluster.bind_port 无效: %d", c.Cluster.BindPort)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level 无效: %s（可选 debug/info/warn/error）", c.Log.Level)
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("无法创建数据目录 %s: %w", c.DataDir, err)
	}
	if err := os.MkdirAll(c.ModelsDir, 0o755); err != nil {
		return fmt.Errorf("无法创建模型目录 %s: %w", c.ModelsDir, err)
	}
	return nil
}

// WaitTimeout 用于 RPC 操作的默认超时。
const WaitTimeout = 5 * time.Second

func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}
