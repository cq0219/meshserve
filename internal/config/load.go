package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Load 从 path 读取 YAML 配置；若文件不存在则返回默认配置。
// 优先级：环境变量 > 文件 > 默认值。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// 文件不存在：使用默认配置（首次运行）
				return cfg, nil
			}
			return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
		}
		if err := unmarshalYAML(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
		}
	}
	applyEnv(cfg)
	return cfg, nil
}

// ConfigPath 返回给定数据目录下的配置文件路径。
func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "meshserve.yaml")
}

// applyEnv 用环境变量覆盖配置（MESHSERVE_ 前缀）。
func applyEnv(cfg *Config) {
	// 保持轻量：仅覆盖最常用的几个字段
	if v := os.Getenv("MESHSERVE_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("MESHSERVE_GATEWAY_ADDR"); v != "" {
		cfg.Gateway.HTTPAddr = v
	}
	if v := os.Getenv("MESHSERVE_AGENT_RPC_ADDR"); v != "" {
		cfg.Agent.RPCAddr = v
	}
}
