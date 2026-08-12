// Package mdns 实现 mDNS 自动发现：节点在局域网广播自身服务，
// 新节点无需人工指定地址即可发现引导节点（对应架构方案 3.4 种子发现）。
package mdns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceType 是 MeshServe 节点的 mDNS 服务类型。
const ServiceType = "_meshserve._tcp"

// ServiceInfo 发现的 MeshServe 节点信息。
type ServiceInfo struct {
	// Instance 节点 ID
	Instance string
	// Host 主机名
	Host string
	// IP 节点 IP
	IP string
	// Port 成员通信端口（gossip）
	Port int
	// NodeID 节点 ID（TXT 记录，与 Instance 一致）
	NodeID string
	// Role 角色（bootstrap/member）
	Role string
}

// Addr 返回可用于 join 的 host:port。
func (s ServiceInfo) Addr() string {
	return net.JoinHostPort(s.IP, strconv.Itoa(s.Port))
}

// Register 在局域网注册本节点 mDNS 服务（阻塞直到 ctx 取消或出错）。
// 返回后可通过 stop() 手动停止广播。
func Register(ctx context.Context, instance, nodeID, role string, port int, log *slog.Logger) (stop func(), err error) {
	// 自动探测本机局域网 IP（避免只广播 loopback）
	ip, err := localIP()
	if err != nil {
		return nil, fmt.Errorf("探测本机 IP 失败: %w", err)
	}
	svc, err := zeroconf.Register(instance, ServiceType, "local.", port,
		[]string{"node_id=" + nodeID, "role=" + role}, nil)
	if err != nil {
		return nil, fmt.Errorf("注册 mDNS 服务失败: %w", err)
	}
	log.Info("mDNS 服务已注册", "instance", instance, "ip", ip, "port", port, "role", role)
	return func() { svc.Shutdown() }, nil
}

// Discover 在局域网发现 MeshServe 节点，返回找到的服务列表。
// timeout 为发现窗口（通常 2~3 秒）。返回后解析器自动关闭。
func Discover(ctx context.Context, timeout time.Duration, log *slog.Logger) []ServiceInfo {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Warn("创建 mDNS 解析器失败", "err", err)
		return nil
	}

	var (
		mu  sync.Mutex
		out []ServiceInfo
	)
	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		for e := range entries {
			si := toServiceInfo(e)
			if si == nil {
				continue
			}
			mu.Lock()
			out = append(out, *si)
			mu.Unlock()
		}
	}()

	if err := resolver.Browse(ctx, ServiceType, "local.", entries); err != nil {
		log.Warn("mDNS 浏览失败", "err", err)
		return nil
	}
	<-ctx.Done()
	mu.Lock()
	defer mu.Unlock()
	return out
}

// DiscoverFirst 发现并返回第一个 MeshServe 节点（用于 join 自动发现）。
func DiscoverFirst(ctx context.Context, timeout time.Duration, log *slog.Logger) (*ServiceInfo, error) {
	svcs := Discover(ctx, timeout, log)
	if len(svcs) == 0 {
		return nil, fmt.Errorf("mDNS 未发现任何 MeshServe 节点")
	}
	// 优先引导节点（bootstrap），否则取第一个
	for i := range svcs {
		if svcs[i].Role == "bootstrap" {
			return &svcs[i], nil
		}
	}
	return &svcs[0], nil
}

// toServiceInfo 转换 zeroconf 条目为 ServiceInfo。
func toServiceInfo(e *zeroconf.ServiceEntry) *ServiceInfo {
	if e == nil {
		return nil
	}
	ip := ""
	if len(e.AddrIPv4) > 0 {
		ip = e.AddrIPv4[0].String()
	} else if len(e.AddrIPv6) > 0 {
		ip = e.AddrIPv6[0].String()
	}
	if ip == "" {
		return nil
	}
	si := &ServiceInfo{
		Instance: e.Instance,
		Host:     e.HostName,
		IP:       ip,
		Port:     e.Port,
	}
	for _, txt := range e.Text {
		kv := strings.SplitN(txt, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "node_id":
			si.NodeID = kv[1]
		case "role":
			si.Role = kv[1]
		}
	}
	return si
}

// localIP 探测本机第一个非 loopback 的局域网 IP。
func localIP() (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4, nil
			}
		}
	}
	return nil, fmt.Errorf("未找到非 loopback 的 IPv4 地址")
}
