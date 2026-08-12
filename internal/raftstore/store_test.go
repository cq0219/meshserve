package raftstore

import (
	"context"
	"testing"
	"time"
)

// TestKV_RoundTrip 配置 KV 读写回环。
func TestKV_RoundTrip(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := s.Put(ctx, "k1", []byte("v1")); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	v, err := s.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if string(v) != "v1" {
		t.Errorf("值不匹配: %q", v)
	}
	if err := s.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, err := s.Get(ctx, "k1"); err == nil {
		t.Error("删除后应返回错误")
	}
}

// TestKV_Watch 变更通知。
func TestKV_Watch(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := s.Watch(ctx, "model/")
	if err := s.Put(context.Background(), "model/qwen", []byte("{}")); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	select {
	case key := <-ch:
		if key != "model/qwen" {
			t.Errorf("watch key 不匹配: %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch 通知超时")
	}
}

// TestClusterMeta 集群元数据写入与读取。
func TestClusterMeta(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	if err := s.SetClusterMeta("cluster-x", "tok-123"); err != nil {
		t.Fatalf("SetClusterMeta 失败: %v", err)
	}
	id, err := s.ClusterID()
	if err != nil || id != "cluster-x" {
		t.Errorf("ClusterID 错误: %q, err=%v", id, err)
	}
	tok, err := s.JoinToken()
	if err != nil || tok != "tok-123" {
		t.Errorf("JoinToken 错误: %q, err=%v", tok, err)
	}
}

// TestLeaderElection 确定性选举：字典序最小者为 Leader。
func TestLeaderElection(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	if err := s.SetPeers([]string{"node-c", "node-a", "node-b"}); err != nil {
		t.Fatalf("SetPeers 失败: %v", err)
	}
	if got := s.Leader(); got != "node-a" {
		t.Errorf("Leader 应为 node-a，实际 %s", got)
	}
	if !s.IsLeader("node-a") {
		t.Error("node-a 应为 Leader")
	}
}

// TestModelCRUD 模型元数据 CRUD。
func TestModelCRUD(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	m := &Model{Name: "qwen", Version: "v1", Path: "/m/qwen", Engine: "fake", VRAMBytes: 1 << 30}
	if err := s.PutModel("qwen", mustMarshal(m)); err != nil {
		t.Fatalf("PutModel 失败: %v", err)
	}
	data, err := s.GetModel("qwen")
	if err != nil {
		t.Fatalf("GetModel 失败: %v", err)
	}
	got, err := DecodeModel(data)
	if err != nil {
		t.Fatalf("DecodeModel 失败: %v", err)
	}
	if got.Name != "qwen" || got.VRAMBytes != 1<<30 {
		t.Errorf("模型字段不符: %+v", got)
	}
	names, _ := s.ListModels()
	if len(names) != 1 || names[0] != "qwen" {
		t.Errorf("ListModels 错误: %v", names)
	}
	if err := s.DeleteModel("qwen"); err != nil {
		t.Fatalf("DeleteModel 失败: %v", err)
	}
}

// TestModelValidate 模型校验。
func TestModelValidate(t *testing.T) {
	if err := (&Model{}).Validate(); err == nil {
		t.Error("空模型应校验失败")
	}
	m := &Model{Name: "m", Path: "/p", VRAMBytes: 100}
	if err := m.Validate(); err != nil {
		t.Errorf("合法模型校验失败: %v", err)
	}
}

// TestEstimateVRAM 显存估算合理。
func TestEstimateVRAM(t *testing.T) {
	v := EstimateVRAM(7, "fp16")
	if v < 13<<30 || v > 20<<30 {
		t.Errorf("7B fp16 估算异常: %d（期望约 18GB）", v)
	}
	v4 := EstimateVRAM(7, "int4")
	if v4 >= v {
		t.Error("int4 估算应小于 fp16")
	}
}

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	return s, func() { _ = s.Close() }
}

func mustMarshal(m *Model) []byte {
	b, _ := m.Encode()
	return b
}
