// Package health 提供 Liveness / Readiness / Startup 三类探针。
// 对应方案 5.4 健康检查设计：语义与 K8s 对齐但自研实现。
package health

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// Status 探针状态。
type Status struct {
	// Ready 是否接受流量（模型已加载）
	Ready atomic.Bool
	// Startup 是否完成启动（大模型加载期豁免杀进程）
	Startup atomic.Bool
}

// New 创建探针状态（默认未就绪）。
func New() *Status {
	s := &Status{}
	s.Ready.Store(false)
	s.Startup.Store(false)
	return s
}

// SetReady 设置就绪状态。
func (s *Status) SetReady(v bool) { s.Ready.Store(v) }

// SetStartup 设置启动完成状态。
func (s *Status) SetStartup(v bool) { s.Startup.Store(v) }

// Handler 返回探针 HTTP 处理器。
// GET /healthz      → liveness（进程存活，恒 200）
// GET /readyz       → readiness（模型就绪才 200）
// GET /startupz     → startup（启动完成才 200）
func (s *Status) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.Ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("GET /startupz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.Startup.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("started"))
	})
	return mux
}

// WaitReady 阻塞等待就绪（带超时），供测试与启动编排使用。
// timeout<=0 时只做一次即时检查。
func (s *Status) WaitReady(ctx context.Context, timeout time.Duration) bool {
	// 先即时检查一次，避免 timeout=0 时直接返回 false
	if s.Ready.Load() {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if s.Ready.Load() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}
