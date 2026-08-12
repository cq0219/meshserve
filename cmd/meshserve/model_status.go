package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/raftstore"
)

// newModelCmd 模型管理子命令组。
func newModelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "模型管理（register/list/deploy/remove）",
	}
	cmd.AddCommand(
		newModelRegisterCmd(),
		newModelListCmd(),
		newModelRemoveCmd(),
	)
	return cmd
}

// newModelRegisterCmd 注册本地模型。
func newModelRegisterCmd() *cobra.Command {
	var (
		path   string
		engine string
		quant  string
		paramB string
		vram   string
	)
	cmd := &cobra.Command{
		Use:   "register <name>",
		Short: "注册本地模型（不部署）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := raftstore.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer store.Close()

			// 显存估算：优先显式 vram，其次参数量
			var vramBytes uint64
			if vram != "" {
				v, err := strconv.ParseUint(vram, 10, 64)
				if err != nil {
					return fmt.Errorf("vram 参数无效（期望字节数）: %w", err)
				}
				vramBytes = v
			} else if pb, err := strconv.ParseFloat(paramB, 64); err == nil && pb > 0 {
				vramBytes = raftstore.EstimateVRAM(pb, quant)
			}

			m := &raftstore.Model{
				Name:      name,
				Version:   "v1",
				Path:      path,
				Engine:    engine,
				Quant:     quant,
				VRAMBytes: vramBytes,
				Replicas:  1,
				Source:    "local",
			}
			if err := m.Validate(); err != nil {
				return err
			}
			if err := store.PutModel(name, mustEncodeModel(m)); err != nil {
				return fmt.Errorf("保存模型失败: %w", err)
			}
			fmt.Printf("✅ 模型已注册: %s (engine=%s, quant=%s, vram≈%d bytes)\n", name, engine, quant, vramBytes)
			fmt.Printf("部署方式: meshserve model deploy %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "模型权重目录路径")
	cmd.Flags().StringVar(&engine, "engine", "vllm", "推理引擎: vllm|sglang|llamacpp|fake")
	cmd.Flags().StringVar(&quant, "quant", "fp16", "量化: fp16|bf16|int8|int4")
	cmd.Flags().StringVar(&paramB, "params", "", "参数量（十亿），用于显存估算")
	cmd.Flags().StringVar(&vram, "vram", "", "显存需求（字节），优先级高于 params")
	return cmd
}

// newModelListCmd 列出模型。
func newModelListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出已注册模型",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := raftstore.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer store.Close()
			names, err := store.ListModels()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Println("（暂无已注册模型）")
				return nil
			}
			fmt.Printf("%-24s %-10s %-8s %-14s\n", "名称", "引擎", "量化", "显存需求")
			for _, n := range names {
				data, err := store.GetModel(n)
				if err != nil {
					continue
				}
				m, err := raftstore.DecodeModel(data)
				if err != nil {
					continue
				}
				fmt.Printf("%-24s %-10s %-8s %-14d\n", m.Name, m.Engine, m.Quant, m.VRAMBytes)
			}
			return nil
		},
	}
}

// newModelRemoveCmd 删除模型。
func newModelRemoveCmd() *cobra.Command {
	var removeFiles bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "删除模型",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := raftstore.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.DeleteModel(name); err != nil {
				return err
			}
			if removeFiles {
				_ = removePath(cfg, name)
			}
			fmt.Printf("✅ 模型已删除: %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&removeFiles, "remove-files", false, "同时删除模型权重文件")
	return cmd
}

// newStatusCmd 查看节点与集群状态。
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "查看集群状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := raftstore.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer store.Close()
			fmt.Println("=== MeshServe 集群状态 ===")
			if id, err := store.ClusterID(); err == nil {
				fmt.Printf("集群 ID:   %s\n", id)
			}
			fmt.Printf("节点 ID:   %s\n", cfg.NodeID)
			fmt.Printf("Leader:    %s\n", store.Leader())
			fmt.Printf("数据目录:  %s\n", cfg.DataDir)
			fmt.Printf("网关地址:  %s\n", cfg.Gateway.HTTPAddr)
			fmt.Printf("推理引擎:  %s\n", cfg.Agent.Engine)
			// 本机实例状态
			ag := agent.New(cfg, log)
			insts := ag.ListInstances()
			if len(insts) > 0 {
				fmt.Println("--- 本机实例 ---")
				for _, i := range insts {
					fmt.Printf("  %-28s %s state=%s\n", i.ID, i.ModelName, i.State)
				}
			}
			return nil
		},
	}
}

// mustEncodeModel 序列化模型（内部使用）。
func mustEncodeModel(m *raftstore.Model) []byte {
	b, err := m.Encode()
	if err != nil {
		panic(err)
	}
	return b
}

func removePath(cfg *config.Config, name string) error {
	return os.RemoveAll(filepath.Join(cfg.ModelsDir, name))
}
