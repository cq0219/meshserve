// Package cluster 实现基于 memberlist (SWIM/Gossip) 的成员管理：
// 节点注册、状态扩散、故障检测，以及供调度器/网关消费的事件流。
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

// NodeState 节点状态。
type NodeState string

const (
	// StateAlive 在线
	StateAlive NodeState = "alive"
	// StateSuspect 疑似故障（SWIM 间接探测中）
	StateSuspect NodeState = "suspect"
	// StateFailed 已判定离线
	StateFailed NodeState = "failed"
	// StateLeft 正常离开
	StateLeft NodeState = "left"
)

// EventType 成员事件类型。
type EventType string

const (
	// EventJoined 新节点加入
	EventJoined EventType = "joined"
	// EventLeft 节点离开
	EventLeft EventType = "left"
	// EventFailed 节点故障
	EventFailed EventType = "failed"
	// EventUpdated 节点信息更新（资源标签变化等）
	EventUpdated EventType = "updated"
)

// Member 集群成员信息。
type Member struct {
	// ID 节点唯一 ID（= 公钥指纹/生成的 node ID）
	ID string `json:"id"`
	// Addr 成员通信地址（gossip 用）
	Addr string `json:"addr"`
	// Port 成员通信端口
	Port int `json:"port"`
	// Role 角色：bootstrap|member
	Role string `json:"role"`
	// State 节点状态
	State NodeState `json:"state"`
	// Tags 资源标签（GPU 型号、显存、分区等，供调度器使用）
	Tags map[string]string `json:"tags,omitempty"`
}

// Event 成员事件。
type Event struct {
	Type   EventType
	Member Member
}

// Meta 是放入 memberlist 的节点元数据（有限大小，约几百字节）。
type Meta struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Tags string `json:"tags"` // 扁平化的 key=value,key=value
}

// Manager 成员管理器。
type Manager struct {
	list   *memberlist.Memberlist
	self   Member
	mu     sync.RWMutex
	events chan Event
	subs   []chan Event
	log    *slog.Logger
	stop   chan struct{}
}

// Options 成员管理器配置。
type Options struct {
	// NodeID 节点 ID
	NodeID string
	// Role 角色
	Role string
	// BindAddr 监听地址
	BindAddr string
	// BindPort 监听端口（memberlist）
	BindPort int
	// JoinAddr 初始加入节点地址（host:port）
	JoinAddr string
	// EnableTLS 启用加密 gossip（生产建议开启）
	EnableTLS bool
	// Tags 本节点资源/服务标签（随 NodeMeta gossip 扩散，如 console_port/gateway_port）
	Tags map[string]string
	// Logger 日志器
	Logger *slog.Logger
}

// New 创建并启动成员管理器。
func New(ctx context.Context, opts Options) (*Manager, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		self:   Member{ID: opts.NodeID, Role: opts.Role, Addr: opts.BindAddr, Port: opts.BindPort, State: StateAlive, Tags: opts.Tags},
		events: make(chan Event, 64),
		subs:   make([]chan Event, 0),
		log:    log,
		stop:   make(chan struct{}),
	}
	cfg := memberlist.DefaultLANConfig()
	cfg.Name = opts.NodeID
	cfg.BindAddr = opts.BindAddr
	cfg.BindPort = opts.BindPort
	cfg.AdvertisePort = opts.BindPort
	cfg.LogOutput = nil // 交给 slog
	cfg.Delegate = &delegate{m: m}
	cfg.Events = &eventHandler{m: m}
	cfg.EnableCompression = true
	if opts.EnableTLS {
		// 简单加密 key：基于节点 ID 派生（生产应使用 init 生成的共享 key）
		key := make([]byte, 32)
		copy(key, "meshserve-v1-encryption-key-change-me")
		cfg.SecretKey = key
	}
	list, err := memberlist.Create(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建 memberlist 失败: %w", err)
	}
	m.list = list
	if opts.JoinAddr != "" {
		// 尝试加入已有集群（非阻塞：失败仅记录，mDNS/手动重试由上层负责）
		go func() {
			if _, err := list.Join([]string{opts.JoinAddr}); err != nil {
				log.Warn("加入集群失败（可稍后重试）", "err", err, "addr", opts.JoinAddr)
			}
		}()
	}
	return m, nil
}

// Self 返回本节点信息。
func (m *Manager) Self() Member {
	return m.self
}

// Members 返回当前成员快照（去重、含状态）。
func (m *Manager) Members() []Member {
	members := m.list.Members()
	out := make([]Member, 0, len(members))
	for _, mm := range members {
		meta := decodeMeta(mm.Meta)
		if meta == nil {
			continue
		}
		out = append(out, Member{
			ID:    meta.ID,
			Addr:  mm.Addr.String(),
			Port:  int(mm.Port),
			Role:  meta.Role,
			State: stateOf(mm.State),
			Tags:  parseTags(meta.Tags),
		})
	}
	return out
}

// Subscribe 订阅成员事件流（返回的 channel 需消费，避免阻塞扩散）。
func (m *Manager) Subscribe() <-chan Event {
	ch := make(chan Event, 64)
	m.mu.Lock()
	m.subs = append(m.subs, ch)
	m.mu.Unlock()
	return ch
}

// Join 显式加入指定地址的集群。
func (m *Manager) Join(addr string) (int, error) {
	return m.list.Join([]string{addr})
}

// Shutdown 优雅退出成员管理。
func (m *Manager) Shutdown() error {
	close(m.stop)
	if err := m.list.Leave(time.Second); err != nil {
		m.log.Warn("离开集群失败", "err", err)
	}
	return m.list.Shutdown()
}

// broadcast 将事件扇出到所有订阅者（非阻塞）。
func (m *Manager) broadcast(ev Event) {
	m.mu.RLock()
	subs := make([]chan Event, len(m.subs))
	copy(subs, m.subs)
	m.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // 订阅者消费慢则丢弃，避免阻塞
		}
	}
}

func stateOf(s memberlist.NodeStateType) NodeState {
	switch s {
	case memberlist.StateSuspect:
		return StateSuspect
	case memberlist.StateDead:
		return StateFailed
	case memberlist.StateLeft:
		return StateLeft
	default:
		return StateAlive
	}
}

func decodeMeta(raw []byte) *Meta {
	if len(raw) == 0 {
		return nil
	}
	meta := &Meta{}
	if err := jsonUnmarshal(raw, meta); err != nil {
		return nil
	}
	return meta
}

func flattenTags(tags map[string]string) string {
	out := ""
	for k, v := range tags {
		if out != "" {
			out += ","
		}
		out += k + "=" + v
	}
	return out
}

// parseTags 将扁平化的 key=value,key=value 还原为 map。
func parseTags(flat string) map[string]string {
	if flat == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(flat, ",") {
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
