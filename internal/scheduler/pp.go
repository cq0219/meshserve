// PP 编排器：将 PP>1 模型跨节点部署——各节点 agent 并发拉起 vLLM，
// rank0 暴露 OpenAI API，worker 仅参与计算；任一节点失败则全部回滚。
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/raftstore"
)

// DefaultAgentPort 节点 agent API 默认端口（未注入 agent_port 标签时）。
const DefaultAgentPort = "9100"

// PPCoordinator 跨节点流水线并行编排器。
type PPCoordinator struct {
	// agentPortOf 解析成员标签中的 agent API 端口（可注入，默认读 Tags["agent_port"]）
	agentPortOf func(m *cluster.Member) string
	log         *slog.Logger
}

// NewPPCoordinator 创建编排器。
func NewPPCoordinator(log *slog.Logger) *PPCoordinator {
	if log == nil {
		log = slog.Default()
	}
	return &PPCoordinator{agentPortOf: agentPortOf, log: log}
}

func agentPortOf(m *cluster.Member) string {
	if m.Tags != nil {
		if p := m.Tags["agent_port"]; p != "" {
			return p
		}
	}
	return DefaultAgentPort
}

// agentAddrOf 构造成员 agent API 地址：Member.Addr 为 gossip 地址（host:port），
// 需提取主机部分再拼接 agent 端口。
func (c *PPCoordinator) agentAddrOf(m *cluster.Member) string {
	return net.JoinHostPort(hostOf(m.Addr), c.agentPortOf(m))
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// Result PP 部署结果。
type Result struct {
	// Rank0Addr rank0 引擎地址（形如 10.0.0.5:8000），供网关注册路由
	Rank0Addr string
	// InstanceIDs 各 rank 的实例 ID（回滚/查询用）
	InstanceIDs []string
	// Nodes 参与部署的节点（按 rank 序）
	Nodes []cluster.Member
}

// Deploy 将模型按 PP 并发部署到 nodes（按序分配 rank 0..PP-1，长度需 ≥ PP）。
// rank0 使用 model.Port（0=默认 8000）；worker 端口顺延隔离。
func (c *PPCoordinator) Deploy(ctx context.Context, m *raftstore.Model, nodes []cluster.Member, path string, args []string, backend string) (*Result, error) {
	pp := m.PipelineParallel
	if pp <= 1 {
		return nil, fmt.Errorf("PP=%d 无需跨节点编排", pp)
	}
	if len(nodes) < pp {
		return nil, fmt.Errorf("可用节点 %d < PP %d", len(nodes), pp)
	}
	rank0Port := m.Port
	if rank0Port == 0 {
		rank0Port = 8000
	}

	// InstanceIDs 预分配并按下标赋值：InstanceIDs[i] 严格对应 Nodes[i]（rank i），
	// 避免并发 append 导致回滚时实例与节点错配。
	res := &Result{Nodes: nodes[:pp], InstanceIDs: make([]string, pp)}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for i := 0; i < pp; i++ {
		n := nodes[i]
		instID := fmt.Sprintf("inst-%s-pp%d", m.Name, i)
		spec := agent.DeploySpec{
			ModelPath:          path,
			Engine:             m.Engine,
			VRAMQuota:          m.VRAMBytes,
			TensorParallel:     m.TensorParallel,
			PipelineParallel:   pp,
			Quant:              m.Quant,
			Port:               rank0Port + i, // worker 端口顺延，避免同机冲突
			PPRank:             i,
			DistributedBackend: backend,
			Args:               args,
		}
		wg.Add(1)
		go func(i int, n cluster.Member, instID string) {
			defer wg.Done()
			cli := agent.NewClient(c.agentAddrOf(&n))
			c.log.Info("PP 部署", "model", m.Name, "node", n.Addr, "rank", i, "pp", pp)
			if err := cli.Deploy(ctx, agent.DeployRequest{InstanceID: instID, ModelName: m.Name, Spec: spec}); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("rank%d @ %s: %w", i, n.Addr, err))
				mu.Unlock()
				return
			}
			mu.Lock()
			res.InstanceIDs[i] = instID // 按下标写入，保持与 Nodes/rank 对应
			mu.Unlock()
		}(i, n, instID)
	}
	wg.Wait()

	if len(errs) > 0 {
		// 回滚已成功部署的 rank（跳过未部署的空下标）
		for i, id := range res.InstanceIDs {
			if id == "" {
				continue
			}
			n := res.Nodes[i]
			cli := agent.NewClient(c.agentAddrOf(&n))
			_ = cli.Stop(ctx, id)
		}
		return nil, fmt.Errorf("PP 部署失败，已回滚: %v", errs[0])
	}

	res.Rank0Addr = net.JoinHostPort(hostOf(res.Nodes[0].Addr), fmt.Sprintf("%d", rank0Port))
	c.log.Info("PP 部署完成", "model", m.Name, "pp", pp, "rank0", res.Rank0Addr)
	return res, nil
}
