package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/yourorg/meshserve/internal/raftstore"
)

// 构造测试资源视图。
func res(nodeID string, total, free uint64) *NodeResources {
	return &NodeResources{
		NodeID: nodeID,
		GPUs: []GPUCapacity{
			{ID: "0", Name: "A100", VRAMTotal: total, VRAMFree: free},
		},
		MemAvail: 64 << 30,
		Updated:  time.Now(),
	}
}

// TestPlace_Basic 基本放置：充足显存应选中唯一节点。
func TestPlace_Basic(t *testing.T) {
	s := New(nil, nil, nil)
	s.UpdateResources(res("node-a", 80<<30, 80<<30))

	m := &raftstore.Model{Name: "qwen-7b", VRAMBytes: 30 << 30}
	p, err := s.Place(context.Background(), m)
	if err != nil {
		t.Fatalf("Place 返回错误: %v", err)
	}
	if p.NodeID != "node-a" {
		t.Errorf("期望放置到 node-a，实际 %s", p.NodeID)
	}
	if p.InstanceID == "" {
		t.Error("InstanceID 不应为空")
	}
}

// TestPlace_InsufficientVRAM 显存不足应返回错误。
func TestPlace_InsufficientVRAM(t *testing.T) {
	s := New(nil, nil, nil)
	s.UpdateResources(res("node-a", 16<<30, 16<<30))

	m := &raftstore.Model{Name: "big-model", VRAMBytes: 80 << 30}
	if _, err := s.Place(context.Background(), m); err == nil {
		t.Fatal("期望返回显存不足错误，实际成功")
	}
}

// TestPlace_NoNodes 无节点应返回错误。
func TestPlace_NoNodes(t *testing.T) {
	s := New(nil, nil, nil)
	m := &raftstore.Model{Name: "m", VRAMBytes: 1 << 30}
	if _, err := s.Place(context.Background(), m); err == nil {
		t.Fatal("期望返回无节点错误，实际成功")
	}
}

// TestPlace_ChooseMostFree 多个候选节点时选择空闲更多者。
func TestPlace_ChooseMostFree(t *testing.T) {
	s := New(nil, nil, nil)
	s.UpdateResources(res("node-busy", 80<<30, 20<<30)) // 空闲 20G
	s.UpdateResources(res("node-idle", 80<<30, 70<<30)) // 空闲 70G

	m := &raftstore.Model{Name: "m", VRAMBytes: 10 << 30}
	p, err := s.Place(context.Background(), m)
	if err != nil {
		t.Fatalf("Place 返回错误: %v", err)
	}
	if p.NodeID != "node-idle" {
		t.Errorf("期望选择 node-idle（更空闲），实际 %s", p.NodeID)
	}
}

// TestRemoveNode 节点移除后不再参与调度。
func TestRemoveNode(t *testing.T) {
	s := New(nil, nil, nil)
	s.UpdateResources(res("node-a", 80<<30, 80<<30))
	s.RemoveNode("node-a")
	m := &raftstore.Model{Name: "m", VRAMBytes: 1 << 30}
	if _, err := s.Place(context.Background(), m); err == nil {
		t.Fatal("节点移除后仍可放置，应返回错误")
	}
}

// TestInstanceID 实例 ID 生成唯一且稳定格式。
func TestInstanceID(t *testing.T) {
	a := instanceID("qwen-2.5-7b")
	b := instanceID("qwen-2.5-7b")
	if a == b {
		t.Error("两次生成的实例 ID 不应相同")
	}
	if len(a) == 0 || a[:5] != "inst-" {
		t.Errorf("实例 ID 格式异常: %s", a)
	}
}
