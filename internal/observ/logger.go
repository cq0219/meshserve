// Package observ 提供统一的结构化日志与指标初始化。
package observ

import (
	"log/slog"
	"os"
)

// NewLogger 创建结构化日志器。
// level 支持 debug/info/warn/error；json 为 true 时输出 JSON 格式（生产推荐）。
func NewLogger(level string, json bool) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if json {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// 指标默认命名空间
const (
	// MetricsNamespace Prometheus 指标命名空间
	MetricsNamespace = "meshserve"
)
