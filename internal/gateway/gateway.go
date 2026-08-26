// Package gateway 实现推理网关：OpenAI 兼容 API、模型路由、SSE 流式转发、限流。
// 对应方案 4.7 推理网关模块。
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/yourorg/meshserve/internal/engine"
)

// Router 将模型名路由到实际推理引擎（由上层 wiring 注入：单机直连 / 集群转发）。
type Router interface {
	// Resolve 返回可处理 model 的引擎列表。
	Resolve(model string) ([]engine.Engine, error)
	// Models 返回可用模型列表。
	Models() ([]string, error)
}

// Limiter 限流器接口（可替换实现）。
type Limiter interface {
	Allow() bool
}

// Gateway 推理网关。
type Gateway struct {
	router  Router
	limiter Limiter
	log     *slog.Logger
	mu      sync.Mutex
	stats   *Stats
}

// Stats 网关运行时统计（暴露给 /metrics）。
type Stats struct {
	Requests      int64 `json:"requests"`
	Errors        int64 `json:"errors"`
	Streams       int64 `json:"streams"`
	LastLatencyMS int64 `json:"last_latency_ms"`
}

// New 创建网关。
func New(router Router, limiter Limiter, log *slog.Logger) *Gateway {
	return &Gateway{router: router, limiter: limiter, log: log, stats: &Stats{}}
}

// Handler 返回完整 HTTP 处理器（含 OpenAI 兼容端点）。
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.handleHealth)
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", g.handleChat)
	mux.HandleFunc("POST /v1/completions", g.handleCompletions)
	mux.HandleFunc("GET /metrics", g.handleMetrics)
	return g.corsMiddleware(g.recoverMiddleware(mux))
}

// corsMiddleware 允许浏览器跨端口调用（Web 控制台 8443 → 网关 8080 的对话请求）。
func (g *Gateway) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverMiddleware 统一 panic 恢复（防止单请求崩溃整个网关）。
func (g *Gateway) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				g.log.Error("请求处理 panic", "err", rec, "path", r.URL.Path)
				http.Error(w, `{"error":{"message":"internal server error"}}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := g.router.Models()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	items := make([]map[string]any, 0, len(models))
	for _, m := range models {
		items = append(items, map[string]any{"id": m, "object": "model", "created": time.Now().Unix(), "owned_by": "meshserve"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}

// handleChat 处理 /v1/chat/completions（支持流式）。
func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	if g.limiter != nil && !g.limiter.Allow() {
		writeErr(w, http.StatusTooManyRequests, errors.New("请求频率超限（429），请稍后重试"))
		return
	}
	var req engine.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("请求体无效: %w", err))
		return
	}
	if err := validateChatReq(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	engines, err := g.router.Resolve(req.Model)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if len(engines) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("模型 %q 当前无可用副本", req.Model))
		return
	}
	start := time.Now()
	g.mu.Lock()
	g.stats.Requests++
	g.mu.Unlock()

	eng := engines[0] // 多副本负载均衡：router.Resolve 已按负载升序返回（低负载优先）
	// 负载感知（M3）：若 router 支持并发计数，请求进出时维护活跃数
	if lb, ok := g.router.(interface {
		Acquire(engine.Engine)
		Release(engine.Engine)
	}); ok {
		lb.Acquire(eng)
		defer lb.Release(eng)
	}
	if req.Stream {
		g.streamChat(w, r.Context(), eng, &req)
	} else {
		g.nonStreamChat(w, r.Context(), eng, &req)
	}
	g.mu.Lock()
	g.stats.LastLatencyMS = time.Since(start).Milliseconds()
	g.mu.Unlock()
}

// streamChat 流式响应：SSE 逐 chunk 输出。
func (g *Gateway) streamChat(w http.ResponseWriter, ctx context.Context, eng engine.Engine, req *engine.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("客户端不支持流式"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	g.mu.Lock()
	g.stats.Streams++
	g.mu.Unlock()

	chunkFn := func(c engine.ChatChunk) error {
		payload, _ := json.Marshal(map[string]any{
			"id": c.ID, "object": "chat.completion.chunk", "model": c.Model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": c.Content}, "finish_reason": nil}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return nil
	}
	_, err := eng.Chat(ctx, *req, chunkFn)
	if err != nil {
		g.log.Warn("流式推理失败", "err", err)
		g.mu.Lock()
		g.stats.Errors++
		g.mu.Unlock()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// nonStreamChat 非流式响应。
func (g *Gateway) nonStreamChat(w http.ResponseWriter, ctx context.Context, eng engine.Engine, req *engine.ChatRequest) {
	chunk, err := eng.Chat(ctx, *req, nil)
	if err != nil {
		g.mu.Lock()
		g.stats.Errors++
		g.mu.Unlock()
		writeErr(w, http.StatusBadGateway, fmt.Errorf("推理失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": chunk.ID, "object": "chat.completion", "model": chunk.Model, "created": time.Now().Unix(),
		"choices": []map[string]any{{
			"index": 0, "message": map[string]string{"role": "assistant", "content": chunk.Content}, "finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
}

// handleCompletions 补全端点（V1 仅透传，供兼容）。
func (g *Gateway) handleCompletions(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, errors.New("/v1/completions 将在后续版本支持，请使用 /v1/chat/completions"))
}

func (g *Gateway) handleMetrics(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Prometheus 文本格式（M2 可观测性）
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "# HELP meshserve_requests_total 网关累计请求数\n")
	_, _ = fmt.Fprintf(w, "# TYPE meshserve_requests_total counter\n")
	_, _ = fmt.Fprintf(w, "meshserve_requests_total %d\n", g.stats.Requests)
	_, _ = fmt.Fprintf(w, "# HELP meshserve_errors_total 网关累计错误数\n")
	_, _ = fmt.Fprintf(w, "# TYPE meshserve_errors_total counter\n")
	_, _ = fmt.Fprintf(w, "meshserve_errors_total %d\n", g.stats.Errors)
	_, _ = fmt.Fprintf(w, "# HELP meshserve_streams_total 流式请求数\n")
	_, _ = fmt.Fprintf(w, "# TYPE meshserve_streams_total counter\n")
	_, _ = fmt.Fprintf(w, "meshserve_streams_total %d\n", g.stats.Streams)
	_, _ = fmt.Fprintf(w, "# HELP meshserve_last_latency_ms 最近一次请求延迟(ms)\n")
	_, _ = fmt.Fprintf(w, "# TYPE meshserve_last_latency_ms gauge\n")
	_, _ = fmt.Fprintf(w, "meshserve_last_latency_ms %d\n", g.stats.LastLatencyMS)
}

// validateChatReq 请求校验：model 与 messages 必填。
func validateChatReq(req *engine.ChatRequest) error {
	if req.Model == "" {
		return errors.New("model 不能为空")
	}
	if len(req.Messages) == 0 {
		return errors.New("messages 不能为空")
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("无效消息角色 %q（可选 system/user/assistant）", m.Role)
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"message": err.Error(), "type": "meshserve_error"},
	})
}
