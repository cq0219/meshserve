// Command meshserve 是 MeshServe 的 CLI 主入口。
// 子命令：init（初始化集群）/ join（加入集群）/ run（启动节点）/ model（模型管理）/ status（查看状态）/ version。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/observ"
	"github.com/yourorg/meshserve/internal/version"
)

var (
	cfgPath string
	log     *slog.Logger
)

func main() {
	root := &cobra.Command{
		Use:     "meshserve",
		Short:   "MeshServe 本地 LLM 推理集群",
		Version: version.String(),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// 默认日志；run 子命令会按配置重建
			log = observ.NewLogger("info", false)
		},
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", "", "配置文件路径（默认 ~/.meshserve/meshserve.yaml）")

	root.AddCommand(
		newInitCmd(),
		newJoinCmd(),
		newRunCmd(),
		newModelCmd(),
		newStatusCmd(),
		newVersionCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(exitCodeOf(err))
	}
}

// exitCodeOf 依据错误类型返回退出码（0/1/2/3/4 约定见方案 5.3）。
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	return 1
}

// loadConfig 加载配置（默认路径或 --config）。
func loadConfig() (*config.Config, error) {
	if cfgPath == "" {
		def := config.Default()
		cfgPath = config.ConfigPath(def.DataDir)
	}
	return config.Load(cfgPath)
}

// nodeIDPath 节点 ID 持久化文件。
func nodeIDPath(dataDir string) string { return filepath.Join(dataDir, "node_id") }

// ensureNodeID 读取或生成节点 ID。
func ensureNodeID(dataDir string) (string, error) {
	p := nodeIDPath(dataDir)
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		return string(b), nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成节点 ID 失败: %w", err)
	}
	id := "node-" + hex.EncodeToString(buf)[:12]
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// newVersionCmd 版本命令。
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "显示版本信息",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("meshserve", version.String())
		},
	}
}

// signalContext 返回可取消的上下文（监听 SIGINT/SIGTERM）。
func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx
}

// startCluster 启动成员管理器（含 join 逻辑）。
func startCluster(ctx context.Context, cfg *config.Config) (*cluster.Manager, error) {
	nodeID, err := ensureNodeID(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	mgr, err := cluster.New(ctx, cluster.Options{
		NodeID:    nodeID,
		Role:      "member",
		BindAddr:  cfg.Cluster.BindAddr,
		BindPort:  cfg.Cluster.BindPort,
		JoinAddr:  cfg.Cluster.JoinAddr,
		EnableTLS: cfg.Cluster.EnableTLS,
		Logger:    log,
	})
	if err != nil {
		return nil, err
	}
	log.Info("成员管理已启动", "node", nodeID, "port", cfg.Cluster.BindPort)
	return mgr, nil
}
