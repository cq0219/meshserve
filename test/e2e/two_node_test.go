//go:build integration

// Package e2e 双节点集成测试：真实启动两个 meshserve 进程，验证组网与推理链路。
// 运行：make build && make test-integration
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/meshserve/internal/config"
)

// binPath 动态定位编译产物（从测试工作目录向上定位到仓库根）。
func binPath() string {
	wd, _ := os.Getwd()
	name := "meshserve"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	return filepath.Join(wd, "..", "..", "bin", name)
}

// waitHTTP 轮询直到 URL 返回 200。
func waitHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("等待 %s 超时", url)
}

// writeConfig 用 config 包序列化配置到文件（避免字符串替换脆弱性）。
func writeConfig(t *testing.T, path, nodeID, dataDir, gossipAddr string, gossipPort int, gatewayAddr, probeAddr, joinAddr string) {
	t.Helper()
	cfg := config.Default()
	cfg.NodeID = nodeID
	cfg.DataDir = dataDir
	cfg.ModelsDir = filepath.Join(dataDir, "models")
	cfg.Cluster.BindAddr = gossipAddr
	cfg.Cluster.BindPort = gossipPort
	cfg.Cluster.JoinAddr = joinAddr
	cfg.Cluster.JoinToken = "it-token"
	cfg.Cluster.EnableTLS = false
	cfg.Gateway.HTTPAddr = gatewayAddr
	cfg.Agent.RPCAddr = probeAddr
	cfg.Agent.Engine = "fake"
	data, err := config.MarshalYAML(cfg)
	if err != nil {
		t.Fatalf("序列化配置失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
}

// TestTwoNodeCluster 双节点端到端：
// A run（引导）→ B join A → B run → 通过 B 网关推理（fake 引擎）。
func TestTwoNodeCluster(t *testing.T) {
	if _, err := os.Stat(binPath()); err != nil {
		t.Skipf("未编译二进制 %s，跳过（请先 make build）", binPath())
	}
	base := t.TempDir()
	dataA := filepath.Join(base, "node-a")
	dataB := filepath.Join(base, "node-b")
	cfgA := filepath.Join(base, "a.yaml")
	cfgB := filepath.Join(base, "b.yaml")

	// 端口规划（避免冲突）
	writeConfig(t, cfgA, "it-node-a", dataA, "127.0.0.1", 17946, "127.0.0.1:18080", "127.0.0.1:19100", "")
	writeConfig(t, cfgB, "it-node-b", dataB, "127.0.0.1", 17947, "127.0.0.1:18081", "127.0.0.1:19101", "127.0.0.1:17946")

	// ---- 先在 B 注册模型（此时无 run 进程占用 db 锁）----
	reg := exec.Command(binPath(), "model", "register", "it-model",
		"--path", filepath.Join(base, "model"), "--engine", "fake", "--params", "0.5", "--config", cfgB)
	if out, err := reg.CombinedOutput(); err != nil {
		t.Fatalf("模型注册失败: %v\n%s", err, out)
	}

	// ---- 启动 A（引导节点）----
	runA := exec.Command(binPath(), "run", "--config", cfgA)
	if err := runA.Start(); err != nil {
		t.Fatalf("A run 启动失败: %v", err)
	}
	defer runA.Process.Kill()
	if err := waitHTTP("http://127.0.0.1:18080/healthz", 15*time.Second); err != nil {
		t.Fatalf("A 网关未就绪: %v", err)
	}

	// ---- 启动 B（join A；run 启动时自动恢复已注册模型）----
	runB := exec.Command(binPath(), "run", "--config", cfgB)
	if err := runB.Start(); err != nil {
		t.Fatalf("B run 启动失败: %v", err)
	}
	defer runB.Process.Kill()
	if err := waitHTTP("http://127.0.0.1:18081/healthz", 15*time.Second); err != nil {
		t.Fatalf("B 网关未就绪: %v", err)
	}
	// 等待 B 自动部署恢复
	time.Sleep(2 * time.Second)

	// ---- 通过 B 网关推理 ----
	resp, err := http.Post("http://127.0.0.1:18081/v1/chat/completions",
		"application/json",
		bytes.NewBufferString(`{"model":"it-model","messages":[{"role":"user","content":"集成测试"}]}`))
	if err != nil {
		t.Fatalf("推理请求失败: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("推理返回 %d: %s", resp.StatusCode, out)
	}
	if len(out.Choices) == 0 || !strings.Contains(out.Choices[0].Message.Content, "集成测试") {
		t.Fatalf("推理结果异常: %+v", out)
	}
	t.Logf("✅ 双节点推理成功: %s", out.Choices[0].Message.Content)
}

// TestSingleNodeAPI 单节点 API 冒烟（不依赖 GPU）。
func TestSingleNodeAPI(t *testing.T) {
	if _, err := os.Stat(binPath()); err != nil {
		t.Skipf("未编译二进制 %s，跳过", binPath())
	}
	base := t.TempDir()
	cfg := filepath.Join(base, "s.yaml")
	writeConfig(t, cfg, "it-solo", filepath.Join(base, "data"), "127.0.0.1", 17950, "127.0.0.1:18085", "127.0.0.1:19105", "")

	// 先注册模型（此时无 run 进程占用 db 锁）
	reg := exec.Command(binPath(), "model", "register", "solo-model",
		"--path", filepath.Join(base, "m"), "--engine", "fake", "--params", "0.5", "--config", cfg)
	if out, err := reg.CombinedOutput(); err != nil {
		t.Fatalf("注册失败: %v\n%s", err, out)
	}

	run := exec.Command(binPath(), "run", "--config", cfg)
	if err := run.Start(); err != nil {
		t.Fatalf("run 启动失败: %v", err)
	}
	defer run.Process.Kill()
	if err := waitHTTP("http://127.0.0.1:18085/healthz", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	// run 启动时自动恢复已注册模型，等待部署完成
	time.Sleep(2 * time.Second)

	// 模型列表应包含
	resp, err := http.Get("http://127.0.0.1:18085/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if !strings.Contains(buf.String(), "solo-model") {
		t.Errorf("模型列表缺少 solo-model: %s", buf.String())
	}

	// 流式对话
	resp2, err := http.Post("http://127.0.0.1:18085/v1/chat/completions",
		"application/json",
		bytes.NewBufferString(`{"model":"solo-model","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	buf2 := new(bytes.Buffer)
	buf2.ReadFrom(resp2.Body)
	if !strings.Contains(buf2.String(), "data:") || !strings.Contains(buf2.String(), "[DONE]") {
		t.Errorf("流式响应异常: %s", buf2.String())
	}
	t.Log("✅ 单节点 API 冒烟通过")
}
