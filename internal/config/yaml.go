package config

import "gopkg.in/yaml.v3"

// unmarshalYAML 解析 YAML 配置。
func unmarshalYAML(data []byte, v *Config) error {
	return yaml.Unmarshal(data, v)
}

// MarshalYAML 序列化配置为 YAML（供 init 持久化）。
func MarshalYAML(v *Config) ([]byte, error) {
	return yaml.Marshal(v)
}
