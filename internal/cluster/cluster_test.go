package cluster

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// TestNewAndSelf 创建成员管理器并读取本节点信息。
func TestNewAndSelf(t *testing.T) {
	m, err := New(context.Background(), Options{
		NodeID: "test-node", Role: "member", BindAddr: "127.0.0.1", BindPort: 0,
		EnableTLS: false, Logger: testLogger(),
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer m.Shutdown()
	if m.Self().ID != "test-node" {
		t.Errorf("Self ID 错误: %s", m.Self().ID)
	}
}

// TestTwoNodesJoin 双节点组网：B join A 后成员表收敛。
func TestTwoNodesJoin(t *testing.T) {
	a, err := New(context.Background(), Options{
		NodeID: "node-a", Role: "bootstrap", BindAddr: "127.0.0.1", BindPort: 0,
		EnableTLS: false, Logger: testLogger(),
	})
	if err != nil {
		t.Fatalf("A 创建失败: %v", err)
	}
	defer a.Shutdown()

	b, err := New(context.Background(), Options{
		NodeID:    "node-b",
		Role:      "member",
		BindAddr:  "127.0.0.1",
		BindPort:  0,
		JoinAddr:  "127.0.0.1:" + itoa(a.list.LocalNode().Port),
		EnableTLS: false,
		Logger:    testLogger(),
	})
	if err != nil {
		t.Fatalf("B 创建失败: %v", err)
	}
	defer b.Shutdown()

	// 等待 gossip 收敛
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.Members()) >= 2 && len(a.Members()) >= 2 {
			return // 收敛成功
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("成员表未收敛: A=%d 个成员, B=%d 个成员", len(a.Members()), len(b.Members()))
}

func itoa(n uint16) string {
	// 小工具：uint16 → string（避免引入 strconv 于测试噪音）
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestJoinEvent 订阅加入事件。
func TestJoinEvent(t *testing.T) {
	a, _ := New(context.Background(), Options{
		NodeID: "node-a", Role: "bootstrap", BindAddr: "127.0.0.1", BindPort: 0,
		EnableTLS: false, Logger: testLogger(),
	})
	defer a.Shutdown()
	events := a.Subscribe()

	_, err := New(context.Background(), Options{
		NodeID:    "node-b",
		Role:      "member",
		BindAddr:  "127.0.0.1",
		BindPort:  0,
		JoinAddr:  "127.0.0.1:" + itoa(a.list.LocalNode().Port),
		EnableTLS: false,
		Logger:    testLogger(),
	})
	if err != nil {
		t.Fatalf("B 创建失败: %v", err)
	}
	// B 创建即离开作用域？defer 未调用——这里立即 join，然后等待事件
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Type == EventJoined {
				return // 收到加入事件
			}
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Error("未收到加入事件")
}

// TestMembers_SelfIncluded 成员表包含本节点。
func TestMembers_SelfIncluded(t *testing.T) {
	m, _ := New(context.Background(), Options{
		NodeID: "solo", Role: "bootstrap", BindAddr: "127.0.0.1", BindPort: 0,
		EnableTLS: false, Logger: testLogger(),
	})
	defer m.Shutdown()
	time.Sleep(300 * time.Millisecond) // 等待本地节点注册
	found := false
	for _, mm := range m.Members() {
		if mm.ID == "solo" {
			found = true
		}
	}
	if !found {
		t.Error("成员表应包含本节点")
	}
}
