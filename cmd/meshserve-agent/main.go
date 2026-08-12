// Command meshserve-agent 是独立节点代理进程入口。
// 与 meshserve run 的内嵌 Agent 等价；分离部署时使用。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/meshserve/internal/agent"
	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/health"
	"github.com/yourorg/meshserve/internal/observ"
)

func main() {
	log := observ.NewLogger("info", false)
	cfg, err := config.Load("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载配置失败:", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "配置校验失败:", err)
		os.Exit(1)
	}
	log = observ.NewLogger(cfg.Log.Level, cfg.Log.JSON)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-ch; cancel() }()

	ag := agent.New(cfg, log)
	ag.StartHealthLoop(ctx, 15*time.Second)
	defer ag.Shutdown()

	// 健康探针服务
	hs := health.New()
	hs.SetStartup(true)
	server := &http.Server{Addr: cfg.Agent.RPCAddr, Handler: hs.Handler()}
	go func() {
		log.Info("Agent 探针服务启动", "addr", cfg.Agent.RPCAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("探针服务退出", "err", err)
		}
	}()

	log.Info("MeshServe Agent 已启动", "node", cfg.NodeID, "data_dir", cfg.DataDir)
	<-ctx.Done()
	shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = server.Shutdown(shutdownCtx)
	log.Info("Agent 已退出")
}
