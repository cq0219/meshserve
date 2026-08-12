package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefault 默认配置包含合理默认值。
func TestDefault(t *testing.T) {
	c := Default()
	if c.Cluster.BindPort != 7946 {
		t.Errorf("默认 bind_port 应为 7946，实际 %d", c.Cluster.BindPort)
	}
	if c.Gateway.HTTPAddr != "0.0.0.0:8080" {
		t.Errorf("默认网关地址错误: %s", c.Gateway.HTTPAddr)
	}
	if c.Log.Level != "info" {
		t.Errorf("默认日志级别错误: %s", c.Log.Level)
	}
}

// TestValidate 校验规则。
func TestValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Error("未初始化（node_id 为空）应校验失败")
	}
	c.NodeID = "node-1"
	if err := c.Validate(); err != nil {
		t.Errorf("合法配置校验失败: %v", err)
	}
	c.Cluster.BindPort = 99999
	if err := c.Validate(); err == nil {
		t.Error("非法端口应校验失败")
	}
	c.Cluster.BindPort = 7946
	c.Log.Level = "bogus"
	if err := c.Validate(); err == nil {
		t.Error("非法日志级别应校验失败")
	}
}

// TestLoad_MissingFile 配置文件不存在时返回默认值。
func TestLoad_MissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if c == nil {
		t.Fatal("应返回默认配置")
	}
}

// TestLoad_InvalidFile 配置文件格式错误时报错。
func TestLoad_InvalidFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.yaml")
	// YAML 非法：tab 缩进 + 重复 key 冲突
	os.WriteFile(p, []byte("node_id: [unclosed\n  bad_indent: \tvalue\n"), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("非法 YAML 应报错")
	}
}

// TestLoad_ValidFile 正常加载 YAML 配置。
func TestLoad_ValidFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ok.yaml")
	content := "node_id: node-x\ncluster_name: test\nlog:\n  level: debug\n  json: true\ngateway:\n  http_addr: 0.0.0.0:9999\n"
	os.WriteFile(p, []byte(content), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if c.NodeID != "node-x" || c.ClusterName != "test" {
		t.Errorf("配置加载错误: %+v", c)
	}
	if c.Log.Level != "debug" || !c.Log.JSON {
		t.Errorf("日志配置加载错误: %+v", c.Log)
	}
	if c.Gateway.HTTPAddr != "0.0.0.0:9999" {
		t.Errorf("网关地址覆盖失败: %s", c.Gateway.HTTPAddr)
	}
}

// TestMarshalRoundTrip 序列化回环。
func TestMarshalRoundTrip(t *testing.T) {
	c := Default()
	c.NodeID = "node-rt"
	data, err := MarshalYAML(c)
	if err != nil {
		t.Fatalf("MarshalYAML 失败: %v", err)
	}
	got, err := Load("")
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	_ = got
	// 解析序列化产物
	p := filepath.Join(t.TempDir(), "rt.yaml")
	os.WriteFile(p, data, 0o644)
	loaded, err := Load(p)
	if err != nil {
		t.Fatalf("回环加载失败: %v", err)
	}
	if loaded.NodeID != "node-rt" {
		t.Errorf("回环后 node_id 丢失: %q", loaded.NodeID)
	}
}
