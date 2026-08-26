package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/cluster"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/console"
	"github.com/yourorg/meshserve/internal/gateway"
	"github.com/yourorg/meshserve/internal/health"
	"github.com/yourorg/meshserve/internal/mdns"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/observ"
	"github.com/yourorg/meshserve/internal/raftstore"
	"github.com/yourorg/meshserve/internal/scheduler"
)

// portOf 提取 host:port 地址中的端口部分（供节点标签广播，M4）。
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// gpuTags 采集本机 GPU 摘要（型号/数量/总显存），供节点标签广播。
// 无 NVIDIA GPU 或 nvidia-smi 不可用时返回占位（不阻断启动）。
func gpuTags() map[string]string {
	gpus, err := agent.CollectGPU()
	if err != nil || len(gpus) == 0 {
		return map[string]string{"gpu_model": "无", "gpu_count": "0", "gpu_vram": "0"}
	}
	models := make([]string, 0, len(gpus))
	var total uint64
	for _, g := range gpus {
		models = append(models, g.Name)
		total += g.VRAMTotal
	}
	return map[string]string{
		"gpu_model": strings.Join(models, ", "),
		"gpu_count": fmt.Sprintf("%d", len(gpus)),
		"gpu_vram":  fmt.Sprintf("%d", total),
	}
}

// newRunCmd 启动节点（Node Agent + 网关 + 健康探针）。
func newRunCmd() *cobra.Command {
	var (
		gatewayAddr string
		agentEngine string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "启动节点（Agent + 推理网关）",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if gatewayAddr != "" {
				cfg.Gateway.HTTPAddr = gatewayAddr
			}
			if agentEngine != "" {
				cfg.Agent.Engine = agentEngine
			}
			log = newLogger(cfg)

			ctx := signalContext()

			// 1. 存储
			store, err := raftstore.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// 2. 成员管理
			nodeID, err := ensureNodeID(cfg.DataDir)
			if err != nil {
				return err
			}
			gt := gpuTags() // 采集本机 GPU 摘要
			mgr, err := cluster.New(ctx, cluster.Options{
				NodeID:    nodeID,
				Role:      "member",
				BindAddr:  cfg.Cluster.BindAddr,
				BindPort:  cfg.Cluster.BindPort,
				JoinAddr:  cfg.Cluster.JoinAddr,
				EnableTLS: cfg.Cluster.EnableTLS,
				// 节点标签：服务端口 + GPU 资源，随 NodeMeta gossip 扩散（控制台展示/调度感知）
				Tags: map[string]string{
					"console_port": portOf(cfg.Console.HTTPAddr),
					"gateway_port": portOf(cfg.Gateway.HTTPAddr),
					"gpu_model":    gt["gpu_model"],
					"gpu_count":    gt["gpu_count"],
					"gpu_vram":     gt["gpu_vram"],
				},
				Logger: log,
			})
			if err != nil {
				return fmt.Errorf("启动成员管理失败: %w", err)
			}
			defer func() { _ = mgr.Shutdown() }()
			log.Info("成员管理已启动", "node", nodeID, "bind", fmt.Sprintf("%s:%d", cfg.Cluster.BindAddr, cfg.Cluster.BindPort))

			// 2.1 mDNS 广播：局域网内新节点可自动发现本节点
			stopMDNS, err := mdns.Register(ctx, nodeID, nodeID, "bootstrap", cfg.Cluster.BindPort, log)
			if err != nil {
				log.Warn("mDNS 广播失败（不影响运行，可手动 join）", "err", err)
			} else {
				defer stopMDNS()
			}

			// 3. 模型仓库 + 节点代理
			repo, err := modelrepo.New(store, cfg.ModelsDir, log)
			if err != nil {
				return err
			}
			ag := agent.New(cfg, log)
			ag.StartHealthLoop(ctx, 15*time.Second)
			defer ag.Shutdown()

			// 4. 调度器（订阅成员事件，节点离线触发重建）
			sched := scheduler.New(store, mgr, log)
			sched.SetDeployHandler(func(ctx context.Context, req scheduler.DeployRequest) error {
				// 本节点部署（V1 单机模式；集群版由各节点 agent RPC 分发）
				_, err := ag.DeployInstance(ctx, req.InstanceID, req.ModelName, agent.DeploySpec{
					ModelPath:        req.ModelPath,
					Engine:           req.Engine,
					VRAMQuota:        req.VRAMBytes,
					TensorParallel:   req.TensorParallel,
					PipelineParallel: req.PipelineParallel,
					Quant:            req.Quant,
				})
				return err
			})
			go watchClusterEvents(ctx, mgr, sched)

			// 5. 网关（单机路由模式）
			router := gateway.NewLocalRouter(repo)
			gw := gateway.New(router, gateway.NewTokenBucket(cfg.Gateway.RateLimit), log)
			registerDeployedModels(ctx, ag, repo, router)

			// 6. 健康探针
			hs := health.New()
			hs.SetStartup(true) // 进程启动即视为完成 startup（大模型加载由实例就绪管理）
			go func() {
				// 周期探活：任一实例就绪则标记 Ready
				t := time.NewTicker(10 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						hs.SetReady(len(ag.ListInstances()) > 0)
					}
				}
			}()

			// 7. HTTP 服务：网关 + 控制台（含指标端点）+ 探针
			mux := http.NewServeMux()
			mux.Handle("/", gw.Handler())

			// 7.1 Web 控制台（M2 交付：REST API + 内嵌前端）
			consoleHandler, err := console.Handler(store, mgr, repo, ag)
			if err != nil {
				return fmt.Errorf("初始化控制台失败: %w", err)
			}
			// 控制台挂载到独立端口（避免与 OpenAI 网关路径冲突）
			cs := &http.Server{Addr: cfg.Console.HTTPAddr, Handler: consoleHandler}
			go func() {
				log.Info("Web 控制台启动", "addr", cfg.Console.HTTPAddr, "url", "http://"+cfg.Console.HTTPAddr)
				if err := cs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Error("控制台退出", "err", err)
				}
			}()

			sh := &http.Server{Addr: cfg.Gateway.HTTPAddr, Handler: mux}
			ph := &http.Server{Addr: cfg.Agent.RPCAddr, Handler: hs.Handler()}
			go func() {
				log.Info("推理网关启动", "addr", cfg.Gateway.HTTPAddr)
				if err := sh.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Error("网关退出", "err", err)
				}
			}()
			go func() {
				log.Info("健康探针启动", "addr", cfg.Agent.RPCAddr)
				if err := ph.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Error("探针服务退出", "err", err)
				}
			}()

			<-ctx.Done()
			log.Info("收到退出信号，优雅关闭…")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = sh.Shutdown(shutdownCtx)
			_ = ph.Shutdown(shutdownCtx)
			_ = cs.Shutdown(shutdownCtx)
			return nil
		},
	}
	cmd.Flags().StringVarP(&gatewayAddr, "gateway", "g", "", "网关监听地址（覆盖配置）")
	cmd.Flags().StringVar(&agentEngine, "engine", "", "推理引擎（vllm/fake，覆盖配置）")
	return cmd
}

// registerDeployedModels 启动时将已注册模型重新部署为实例（服务重启恢复）。
func registerDeployedModels(ctx context.Context, ag *agent.Agent, repo *modelrepo.Repo, router *gateway.LocalRouter) {
	models, err := repo.List(ctx)
	if err != nil {
		log.Warn("读取已注册模型失败", "err", err)
		return
	}
	for _, m := range models {
		if m.Source != "local" {
			continue
		}
		inst, err := ag.DeployInstance(ctx, "inst-"+m.Name+"-restore", m.Name, agent.DeploySpec{
			ModelPath:        m.Path,
			Engine:           m.Engine,
			VRAMQuota:        m.VRAMBytes,
			TensorParallel:   m.TensorParallel,
			PipelineParallel: m.PipelineParallel,
			Quant:            m.Quant,
		})
		if err != nil {
			log.Warn("模型自动部署失败", "model", m.Name, "err", err)
			continue
		}
		if eng, ok := ag.GetEngine(inst.ID); ok {
			router.RegisterEngine(m.Name, eng)
			log.Info("模型已恢复", "model", m.Name, "instance", inst.ID)
		}
	}
}

// watchClusterEvents 订阅成员事件：节点故障 → 调度器触发重建。
func watchClusterEvents(ctx context.Context, mgr *cluster.Manager, sched *scheduler.Scheduler) {
	events := mgr.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Type {
			case cluster.EventFailed:
				log.Warn("检测到节点故障", "node", ev.Member.ID)
				sched.HandleNodeFailed(ctx, ev.Member.ID)
			case cluster.EventLeft:
				log.Info("节点离开集群", "node", ev.Member.ID)
			case cluster.EventJoined:
				log.Info("新节点加入集群", "node", ev.Member.ID)
			}
		}
	}
}

// newLogger 依据配置创建日志器。
func newLogger(cfg *config.Config) *slog.Logger {
	return observ.NewLogger(cfg.Log.Level, cfg.Log.JSON)
}
