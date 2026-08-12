// Command meshserve-gateway 是独立推理网关进程入口。
// 与 meshserve run 的内嵌网关等价；需要独立部署网关时使用。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/meshserve/internal/config"
	"github.com/yourorg/meshserve/internal/gateway"
	"github.com/yourorg/meshserve/internal/modelrepo"
	"github.com/yourorg/meshserve/internal/observ"
	"github.com/yourorg/meshserve/internal/raftstore"
)

func main() {
	log := observ.NewLogger("info", false)
	cfg, err := config.Load("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载配置失败:", err)
		os.Exit(1)
	}
	log = observ.NewLogger(cfg.Log.Level, cfg.Log.JSON)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-ch; cancel() }()

	store, err := raftstore.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开存储失败:", err)
		os.Exit(1)
	}
	defer store.Close()

	repo, err := modelrepo.New(store, cfg.ModelsDir, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化模型仓库失败:", err)
		os.Exit(1)
	}
	_ = repo // 独立网关模式下模型注册由 CLI/Agent 完成

	// 独立网关使用空路由（V1：需配合本地 agent 注册；真实路由在 meshserve run 中）
	router := gateway.NewLocalRouter(repo)
	gw := gateway.New(router, gateway.NewTokenBucket(cfg.Gateway.RateLimit), log)

	server := &http.Server{Addr: cfg.Gateway.HTTPAddr, Handler: gw.Handler()}
	go func() {
		log.Info("独立推理网关启动", "addr", cfg.Gateway.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("网关退出", "err", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = server.Shutdown(shutdownCtx)
	log.Info("网关已退出")
}
