// Package version 提供构建版本信息。
package version

// 版本信息（可通过 ldflags 注入：-X github.com/yourorg/meshserve/internal/version.Version=v1.0.0）
var (
	// Version 语义化版本号
	Version = "0.1.0"
	// GitCommit git commit hash（构建时注入）
	GitCommit = "unknown"
	// BuildTime 构建时间（构建时注入）
	BuildTime = "unknown"
)

// String 返回完整的版本字符串。
func String() string {
	return Version + " (commit " + GitCommit + ", built " + BuildTime + ")"
}
