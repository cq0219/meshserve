// PP 编排器跨节点部署测试：双 agent server（fake 引擎）验证并发部署、端口透传与失败回滚。
package scheduler

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/raftstore"
)

// ppTestNode 测试节点：真实 agent HTTP server + 对应 cluster.Member。
type ppTestNode struct {
	Member cluster.Member
	Addr   string // agent API 地址（host:port）
}

// newPPTestNode 起一个 agent server（fake 引擎），返回节点信息。
// gossipAddr 模拟成员通信地址（host:port 带端口，验证 host 提取）。
func newPPTestNode(t *testing.T, id, gossipAddr string) *ppTestNode {
	t.Helper()
	cfg := defaultAgentCfg(t)
	ag := agent.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(agent.Handler(ag, nil))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return &ppTestNode{
		Member: cluster.Member{
			ID:    id,
			Addr:  gossipAddr,
			State: cluster.StateAlive,
			Tags:  map[string]string{"agent_port": port},
		},
		Addr: strings.TrimPrefix(srv.URL, "http://"),
	}
}

func defaultAgentCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ModelsDir = dir
	_ = agent.EnsureModelDir(cfg.ModelsDir)
	return cfg
}

// instancesOf 查询节点 agent 的实例列表。
func instancesOf(t *testing.T, node *ppTestNode) []agent.Instance {
	t.Helper()
	cli := agent.NewClient(node.Addr)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	insts, err := cli.Instances(ctx)
	if err != nil {
		t.Fatalf("查询 %s 实例失败: %v", node.Addr, err)
	}
	return insts
}

// TestPPCoordinator_DeploySuccess 双节点 PP=2 并发部署：rank 分配、端口顺延、Result 完整。
func TestPPCoordinator_DeploySuccess(t *testing.T) {
	n1 := newPPTestNode(t, "node-a", "127.0.0.1:7946")
	n2 := newPPTestNode(t, "node-b", "127.0.0.1:7947")

	pc := NewPPCoordinator(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := &raftstore.Model{
		Name:             "deepseek",
		Engine:           "fake",
		Path:             "/models/deepseek",
		VRAMBytes:        30 << 30,
		PipelineParallel: 2,
		TensorParallel:   1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := pc.Deploy(ctx, m, []cluster.Member{n1.Member, n2.Member}, m.Path, []string{"--max-model-len", "32768"}, "ray")
	if err != nil {
		t.Fatalf("PP 部署失败: %v", err)
	}

	// Result 校验
	if len(res.InstanceIDs) != 2 || res.InstanceIDs[0] != "inst-deepseek-pp0" || res.InstanceIDs[1] != "inst-deepseek-pp1" {
		t.Fatalf("InstanceIDs 异常: %v", res.InstanceIDs)
	}
	if res.Rank0Addr != "127.0.0.1:8000" {
		t.Fatalf("Rank0Addr = %s, want 127.0.0.1:8000", res.Rank0Addr)
	}
	if len(res.Nodes) != 2 || res.Nodes[0].ID != "node-a" || res.Nodes[1].ID != "node-b" {
		t.Fatalf("Nodes 异常: %+v", res.Nodes)
	}

	// 两节点各部署了对应 rank（rank0 port=8000，worker 顺延 8001）
	i0 := instancesOf(t, n1)
	if len(i0) != 1 || i0[0].PPRank != 0 || i0[0].Port != 8000 || i0[0].PipelineParallel != 2 {
		t.Fatalf("node-a 实例异常: %+v", i0)
	}
	i1 := instancesOf(t, n2)
	if len(i1) != 1 || i1[0].PPRank != 1 || i1[0].Port != 8001 || i1[0].PipelineParallel != 2 {
		t.Fatalf("node-b 实例异常: %+v", i1)
	}
	// 自定义参数透传
	if len(i1[0].Args) != 2 || i1[0].Args[0] != "--max-model-len" {
		t.Fatalf("args 未透传: %+v", i1[0].Args)
	}
}

// TestPPCoordinator_DeployRollback 单节点部署失败（返回 500）→ 已部署节点被回滚（实例清空）。
func TestPPCoordinator_DeployRollback(t *testing.T) {
	n1 := newPPTestNode(t, "node-a", "127.0.0.1:7946")
	// 部署必失败的假 agent：/api/deploy 恒返回 500
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"模拟部署失败"}`)
	}))
	t.Cleanup(failSrv.Close)
	_, failPort, _ := net.SplitHostPort(strings.TrimPrefix(failSrv.URL, "http://"))

	pc := NewPPCoordinator(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := &raftstore.Model{
		Name:             "deepseek",
		Engine:           "fake",
		Path:             "/models/deepseek",
		PipelineParallel: 2,
	}
	bad := cluster.Member{
		ID:    "node-b",
		Addr:  "127.0.0.1:7947",
		State: cluster.StateAlive,
		Tags:  map[string]string{"agent_port": failPort},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pc.Deploy(ctx, m, []cluster.Member{n1.Member, bad}, m.Path, nil, "ray")
	if err == nil {
		t.Fatal("部署应失败")
	}
	if !strings.Contains(err.Error(), "已回滚") {
		t.Fatalf("错误应包含回滚提示: %v", err)
	}

	// node-a 成功部署的 rank0 应已被回滚（实例清空）
	insts := instancesOf(t, n1)
	if len(insts) != 0 {
		t.Fatalf("回滚后 node-a 实例应为空: %+v", insts)
	}
}

// TestPPCoordinator_DeployValidation 参数校验：PP<=1 与节点不足。
func TestPPCoordinator_DeployValidation(t *testing.T) {
	pc := NewPPCoordinator(nil)

	// PP=1 无需编排
	m1 := &raftstore.Model{Name: "m", Engine: "fake", PipelineParallel: 1}
	if _, err := pc.Deploy(context.Background(), m1, nil, "/m", nil, "ray"); err == nil || !strings.Contains(err.Error(), "无需跨节点") {
		t.Fatalf("PP=1 应报错: %v", err)
	}

	// PP=3 但仅 2 节点
	n1 := newPPTestNode(t, "node-a", "127.0.0.1:7946")
	n2 := newPPTestNode(t, "node-b", "127.0.0.1:7947")
	m3 := &raftstore.Model{Name: "big", Engine: "fake", PipelineParallel: 3}
	_, err := pc.Deploy(context.Background(), m3, []cluster.Member{n1.Member, n2.Member}, "/m", nil, "ray")
	if err == nil || !strings.Contains(err.Error(), "可用节点") {
		t.Fatalf("节点不足应报错: %v", err)
	}
}

// TestPPCoordinator_AgentAddrOf 成员地址解析：gossip 地址带端口时提取 host，标签端口兜底 9100。
func TestPPCoordinator_AgentAddrOf(t *testing.T) {
	pc := NewPPCoordinator(nil)

	// 标签指定端口
	m := cluster.Member{Addr: "10.0.0.5:7946", Tags: map[string]string{"agent_port": "9200"}}
	if got := pc.agentAddrOf(&m); got != "10.0.0.5:9200" {
		t.Fatalf("带标签 = %s, want 10.0.0.5:9200", got)
	}

	// 无标签 → 默认 9100
	m2 := cluster.Member{Addr: "10.0.0.6:7946"}
	if got := pc.agentAddrOf(&m2); got != "10.0.0.6:9100" {
		t.Fatalf("无标签 = %s, want 10.0.0.6:9100", got)
	}

	// 纯 IP 地址（无端口）
	m3 := cluster.Member{Addr: "10.0.0.7", Tags: map[string]string{"agent_port": "9101"}}
	if got := pc.agentAddrOf(&m3); got != "10.0.0.7:9101" {
		t.Fatalf("纯 IP = %s, want 10.0.0.7:9101", got)
	}
}
