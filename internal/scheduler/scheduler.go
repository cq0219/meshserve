// Package scheduler 实现资源感知调度：模型放置决策、副本管理、故障重建触发。
// 对应方案 4.4 调度器模块。
package scheduler

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/raftstore"
)

// NodeResources 节点资源画像（来自 agent 上报）。
type NodeResources struct {
	NodeID   string        `json:"node_id"`
	GPUs     []GPUCapacity `json:"gpus"`
	MemAvail uint64        `json:"mem_avail_bytes"`
	Updated  time.Time     `json:"updated"`
}

// GPUCapacity GPU 容量信息。
type GPUCapacity struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	VRAMTotal uint64 `json:"vram_total"`
	VRAMFree  uint64 `json:"vram_free"`
}

// Placement 一次放置决策。
type Placement struct {
	NodeID     string `json:"node_id"`
	InstanceID string `json:"instance_id"`
	Engine     string `json:"engine"`
}

// Scheduler 调度器。
type Scheduler struct {
	mu        sync.RWMutex
	resources map[string]*NodeResources // nodeID → 资源
	store     *raftstore.Store
	members   *cluster.Manager
	log       *slog.Logger
	// onDeploy 部署回调（注入 agent 执行，避免循环依赖）
	onDeploy func(ctx context.Context, nodeID, instanceID, modelName, modelPath, engine string, vram uint64) error
}

// New 创建调度器。
func New(store *raftstore.Store, members *cluster.Manager, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		resources: make(map[string]*NodeResources),
		store:     store,
		members:   members,
		log:       log,
	}
}

// SetDeployHandler 注入部署执行回调（由上层 wiring 设置）。
func (s *Scheduler) SetDeployHandler(fn func(ctx context.Context, nodeID, instanceID, modelName, modelPath, engine string, vram uint64) error) {
	s.onDeploy = fn
}

// UpdateResources 更新某节点资源画像（agent 周期性上报）。
func (s *Scheduler) UpdateResources(r *NodeResources) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil || r.NodeID == "" {
		return
	}
	r.Updated = time.Now()
	s.resources[r.NodeID] = r
}

// RemoveNode 节点离线时移除其资源。
func (s *Scheduler) RemoveNode(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resources, nodeID)
	s.log.Info("节点资源已移除", "node", nodeID)
}

// Place 为模型选择放置节点。约束：显存满足；偏好：空闲资源多者优先。
// 返回 Placement 或错误（无可用节点）。
func (s *Scheduler) Place(ctx context.Context, m *raftstore.Model) (*Placement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type cand struct {
		node  string
		score int
	}
	var cands []cand
	for id, res := range s.resources {
		avail := s.nodeFreeVRAM(res)
		if avail < m.VRAMBytes {
			continue // 硬约束：显存不足
		}
		score := s.score(res, m)
		cands = append(cands, cand{node: id, score: score})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("无可放置节点：所有节点显存不足（模型需要 %d 字节）", m.VRAMBytes)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	best := cands[0].node
	instID := instanceID(m.Name)
	return &Placement{NodeID: best, InstanceID: instID, Engine: m.Engine}, nil
}

// PlaceN 为模型放置 n 个副本（多副本支持，M2）。
// 优先分散到不同节点（故障域分散），不足时允许同节点多副本兜底。
func (s *Scheduler) PlaceN(ctx context.Context, m *raftstore.Model, n int) ([]*Placement, error) {
	if n <= 0 {
		n = 1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 收集所有显存合格的候选节点（按评分排序）
	type cand struct {
		node  string
		score int
	}
	var cands []cand
	for id, res := range s.resources {
		if s.nodeFreeVRAM(res) >= m.VRAMBytes {
			cands = append(cands, cand{node: id, score: s.score(res, m)})
		}
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("无可放置节点：所有节点显存不足（模型需要 %d 字节）", m.VRAMBytes)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	// 分配：循环分散到候选节点（节点数 ≥ n 时完全分散；否则同节点多副本兜底）
	placements := make([]*Placement, 0, n)
	for i := 0; i < n; i++ {
		idx := i % len(cands)
		placements = append(placements, &Placement{
			NodeID:     cands[idx].node,
			InstanceID: instanceID(m.Name),
			Engine:     m.Engine,
		})
	}
	return placements, nil
}

// nodeFreeVRAM 计算节点可分配显存（取所有 GPU 空闲之和，简单模型）。
func (s *Scheduler) nodeFreeVRAM(r *NodeResources) uint64 {
	var free uint64
	for _, g := range r.GPUs {
		free += g.VRAMFree
	}
	return free
}

// score 评分：空闲显存占比 + 已承载实例数（负载均衡）。
func (s *Scheduler) score(r *NodeResources, m *raftstore.Model) int {
	var total, free uint64
	for _, g := range r.GPUs {
		total += g.VRAMTotal
		free += g.VRAMFree
	}
	if total == 0 {
		return 0
	}
	// 空闲率权重 70，实例数权重 30
	freeRatio := int(free * 100 / total)
	instancePenalty := s.instanceCount(r) * 10
	return freeRatio*7 - instancePenalty + 100
}

func (s *Scheduler) instanceCount(r *NodeResources) int {
	// 简化：通过已部署模型数近似（真实实现由实例状态表提供）
	return 0
}

// HandleNodeFailed 节点故障处理：触发该节点上模型副本的重新调度（重建到其他节点）。
func (s *Scheduler) HandleNodeFailed(ctx context.Context, nodeID string) {
	s.RemoveNode(nodeID)
	models, err := s.store.ListModels()
	if err != nil {
		s.log.Error("读取模型清单失败", "err", err)
		return
	}
	for _, name := range models {
		data, err := s.store.GetModel(name)
		if err != nil {
			continue
		}
		m, err := raftstore.DecodeModel(data)
		if err != nil {
			continue
		}
		// 触发重建（异步执行，避免阻塞事件处理）
		go s.redeploy(ctx, m)
	}
	s.log.Info("节点故障处理完成", "node", nodeID, "模型数", len(models))
}

func (s *Scheduler) redeploy(ctx context.Context, m *raftstore.Model) {
	if s.onDeploy == nil {
		s.log.Warn("未注入部署回调，跳过重建", "model", m.Name)
		return
	}
	p, err := s.Place(ctx, m)
	if err != nil {
		s.log.Error("重建放置失败", "model", m.Name, "err", err)
		return
	}
	s.log.Info("重建模型副本", "model", m.Name, "node", p.NodeID)
	if err := s.onDeploy(ctx, p.NodeID, p.InstanceID, m.Name, m.Path, p.Engine, m.VRAMBytes); err != nil {
		s.log.Error("重建部署失败", "model", m.Name, "err", err)
	}
}

func instanceID(modelName string) string {
	ts := time.Now().UnixNano() / 1e6
	// 追加 4 位随机后缀，避免同一毫秒内重复
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("inst-%s-%d-%x", sanitize(modelName), ts, suffix)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
