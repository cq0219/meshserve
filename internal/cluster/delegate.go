package cluster

import (
	"encoding/json"

	"github.com/hashicorp/memberlist"
)

// delegate 实现 memberlist.Delegate：控制节点元数据在 gossip 中的交换。
type delegate struct {
	m *Manager
}

// NodeMeta 返回节点元数据（限制 512 字节内）。
func (d *delegate) NodeMeta(limit int) []byte {
	meta := &Meta{ID: d.m.self.ID, Role: d.m.self.Role, Tags: flattenTags(d.m.self.Tags)}
	raw, err := json.Marshal(meta)
	if err != nil || len(raw) > limit {
		return []byte{}
	}
	return raw
}

// NotifyMsg 接收 gossip 消息（本实现无需应用消息体，保留接口）。
func (d *delegate) NotifyMsg(b []byte) {}

// GetBroadcasts 返回待广播的队列消息。
func (d *delegate) GetBroadcasts(overhead, limit int) [][]byte { return nil }

// LocalState 返回本地完整状态（用于节点加入时的全量同步）。
func (d *delegate) LocalState(join bool) []byte {
	members := d.m.Members()
	raw, _ := json.Marshal(members)
	return raw
}

// MergeRemoteState 处理远端节点状态合并（本实现仅记录，状态收敛靠 gossip）。
func (d *delegate) MergeRemoteState(buf []byte, join bool) {}

// eventHandler 实现 memberlist.EventDelegate：节点加入/离开/故障事件 → 内部 Event 流。
type eventHandler struct {
	m *Manager
}

func (h *eventHandler) NotifyJoin(n *memberlist.Node) {
	meta := decodeMeta(n.Meta)
	if meta == nil {
		return
	}
	h.m.broadcast(Event{Type: EventJoined, Member: Member{
		ID: meta.ID, Addr: n.Addr.String(), Port: int(n.Port), Role: meta.Role, State: StateAlive,
	}})
}

func (h *eventHandler) NotifyLeave(n *memberlist.Node) {
	meta := decodeMeta(n.Meta)
	if meta == nil {
		return
	}
	h.m.broadcast(Event{Type: EventLeft, Member: Member{
		ID: meta.ID, Addr: n.Addr.String(), Port: int(n.Port), Role: meta.Role, State: StateLeft,
	}})
}

func (h *eventHandler) NotifyUpdate(n *memberlist.Node) {
	meta := decodeMeta(n.Meta)
	if meta == nil {
		return
	}
	h.m.broadcast(Event{Type: EventUpdated, Member: Member{
		ID: meta.ID, Addr: n.Addr.String(), Port: int(n.Port), Role: meta.Role, State: StateAlive,
	}})
}

// jsonUnmarshal 便于测试时替换。
var jsonUnmarshal = json.Unmarshal
