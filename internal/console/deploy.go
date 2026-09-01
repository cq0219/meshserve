// 模型部署辅助：PP>1 走 PPCoordinator 跨节点编排，普通模型本机部署；
// 停止/删除时清理本机与全部在线节点的实例。
package console

import (
	"context"
	"net"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/raftstore"
	"github.com/yourorg/meshserve/internal/scheduler"
)

// deployModel 部署模型到集群：
//   - endpoint 外部模型：无需部署
//   - PP>1：PPCoordinator 跨节点编排（rank0 暴露 API，路由注册远端）
//   - 其他：本机部署
func deployModel(ctx context.Context, m *raftstore.Model, members *cluster.Manager, ag *agent.Agent,
	ppc *scheduler.PPCoordinator, registerRemote func(modelName, addr string), backend string) error {
	if m.Endpoint != "" {
		return nil
	}
	if m.PipelineParallel > 1 {
		nodes := aliveMembers(members, m.PipelineParallel)
		res, err := ppc.Deploy(ctx, m, nodes, m.Path, nil, backend)
		if err != nil {
			return err
		}
		registerRemote(m.Name, res.Rank0Addr)
		return nil
	}
	_, err := ag.DeployByModel(ctx, m, m.Path)
	return err
}

// stopModelEverywhere 停止模型实例：本机 + 所有在线节点（PP 实例分散在各节点）。
func stopModelEverywhere(ctx context.Context, modelName string, members *cluster.Manager, ag *agent.Agent) error {
	// 本机
	_ = ag.StopInstancesByModel(ctx, modelName)
	// 远端在线节点：查询并停止同名实例（失败跳过，不阻塞）
	for _, node := range members.Members() {
		if node.ID == members.Self().ID || node.State != cluster.StateAlive {
			continue
		}
		cli := agent.NewClient(agentAddrOf(node))
		insts, err := cli.Instances(ctx)
		if err != nil {
			continue
		}
		for _, inst := range insts {
			if inst.ModelName == modelName {
				_ = cli.Stop(ctx, inst.ID)
			}
		}
	}
	return nil
}

// aliveMembers 返回在线成员，最多 n 个（不足时返回全部在线）。
func aliveMembers(members *cluster.Manager, n int) []cluster.Member {
	var out []cluster.Member
	for _, m := range members.Members() {
		if m.State == cluster.StateAlive {
			out = append(out, m)
			if n > 0 && len(out) >= n {
				break
			}
		}
	}
	return out
}

// agentAddrOf 解析成员标签中的 agent API 地址（host:port）。
func agentAddrOf(m cluster.Member) string {
	port := scheduler.DefaultAgentPort
	if m.Tags != nil {
		if p := m.Tags["agent_port"]; p != "" {
			port = p
		}
	}
	return net.JoinHostPort(hostOf(m.Addr), port)
}
