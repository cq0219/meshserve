package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/mdns"
	"github.com/yourorg/meshserve/internal/raftstore"
	berrors "go.etcd.io/bbolt/errors"
)

// newInitCmd 初始化集群：生成集群 ID、加入令牌、节点 ID，并持久化配置。
func newInitCmd() *cobra.Command {
	var (
		clusterName string
		output      string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化集群（第一台机器执行）",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			nodeID, err := ensureNodeID(cfg.DataDir)
			if err != nil {
				return err
			}
			clusterID := "cluster-" + randHex(8)
			token := randHex(16)

			// 打开存储并写入集群元数据
			store, err := raftstore.Open(cfg.DataDir)
			if err != nil {
				if errors.Is(err, berrors.ErrTimeout) {
					return fmt.Errorf("数据目录 %s 被运行中的节点占用：请先停止 meshserve run 再执行 init", cfg.DataDir)
				}
				return err
			}
			defer func() { _ = store.Close() }()
			// 已初始化检查：避免重复 init 覆盖集群
			if id, err := store.ClusterID(); err == nil {
				return fmt.Errorf("集群已初始化（cluster_id=%s）；如需重新初始化，请先删除数据目录 %s", id, cfg.DataDir)
			}
			if err := store.SetClusterMeta(clusterID, token); err != nil {
				return fmt.Errorf("写入集群元数据失败: %w", err)
			}
			if err := store.SetPeers([]string{nodeID}); err != nil {
				return fmt.Errorf("初始化节点列表失败: %w", err)
			}

			// 更新并持久化配置
			cfg.NodeID = nodeID
			cfg.ClusterName = clusterName
			cfg.Cluster.JoinToken = token
			cfg.Cluster.JoinAddr = ""
			if err := saveConfig(cfg); err != nil {
				return err
			}

			fmt.Printf("✅ 集群初始化完成\n")
			fmt.Printf("   集群名称 : %s\n", clusterName)
			fmt.Printf("   集群 ID  : %s\n", clusterID)
			fmt.Printf("   节点 ID  : %s\n", nodeID)
			fmt.Printf("   加入令牌 : %s\n", token)
			fmt.Printf("\n其他机器加入方式（自动发现）：\n")
			fmt.Printf("   meshserve join --token %s\n", token)
			fmt.Printf("或显式指定本机 IP：\n")
			fmt.Printf("   meshserve join --token %s <本机IP>\n", token)
			fmt.Printf("\n配置已保存至: %s\n", cfgPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&clusterName, "name", "default", "集群名称")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "输出格式: text|json")
	_ = output
	return cmd
}

// newJoinCmd 加入集群。
func newJoinCmd() *cobra.Command {
	var (
		token string
		addr  string
	)
	cmd := &cobra.Command{
		Use:   "join [addr]",
		Short: "加入集群（自动发现或显式指定地址）",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				addr = args[0]
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			nodeID, err := ensureNodeID(cfg.DataDir)
			if err != nil {
				return err
			}
			// 校验令牌（若提供）
			if token != "" {
				store, err := raftstore.Open(cfg.DataDir)
				if err != nil {
					return err
				}
				// 首次 join 时本地无集群元数据，令牌校验由引导节点执行；
				// 此处仅记录令牌到配置。
				_ = store.Close()
				cfg.Cluster.JoinToken = token
			}
			cfg.NodeID = nodeID
			cfg.Cluster.JoinAddr = addr
			if err := saveConfig(cfg); err != nil {
				return err
			}

			ctx := signalContext()
			mgr, err := startCluster(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = mgr.Shutdown() }()

			if addr == "" {
				// M2 自动发现：通过 mDNS 在局域网寻找引导节点
				log.Info("未指定加入地址，尝试 mDNS 自动发现…")
				svc, err := mdns.DiscoverFirst(ctx, 3*time.Second, log)
				if err != nil {
					return fmt.Errorf("mDNS 自动发现失败（可显式指定地址重试）: %w", err)
				}
				addr = svc.Addr()
				cfg.Cluster.JoinAddr = addr
				_ = saveConfig(cfg)
				log.Info("mDNS 发现引导节点", "node", svc.NodeID, "addr", addr, "role", svc.Role)
			}
			n, err := mgr.Join(addr)
			if err != nil {
				return fmt.Errorf("加入集群失败: %w", err)
			}
			time.Sleep(500 * time.Millisecond) // 等待 gossip 收敛
			fmt.Printf("✅ 已加入集群，当前节点数: %d\n", n+1)
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "加入令牌（由 init 生成）")
	return cmd
}

// saveConfig 持久化配置到文件（YAML）。
func saveConfig(cfg *config.Config) error {
	if cfgPath == "" {
		cfgPath = config.ConfigPath(cfg.DataDir)
	}
	data, err := config.MarshalYAML(cfg)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	if err := os.MkdirAll(dirOf(cfgPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return fmt.Errorf("写入配置 %s 失败: %w", cfgPath, err)
	}
	return nil
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
