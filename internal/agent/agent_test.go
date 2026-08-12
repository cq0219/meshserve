package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yourorg/meshserve/internal/config"
)

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ModelsDir = filepath.Join(dir, "models")
	_ = os.MkdirAll(cfg.ModelsDir, 0o755)
	return New(cfg, log)
}

// TestDeployAndStop fake 引擎部署/停止全流程。
func TestDeployAndStop(t *testing.T) {
	a := newTestAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	inst, err := a.DeployInstance(ctx, "inst-1", "m1", filepath.Join(a.cfg.ModelsDir, "m1"), "fake", 0)
	if err != nil {
		t.Fatalf("DeployInstance 失败: %v", err)
	}
	if inst.State != InstReady {
		t.Errorf("期望 ready，实际 %s", inst.State)
	}
	// 重复部署应报错
	if _, err := a.DeployInstance(ctx, "inst-1", "m1", "/m", "fake", 0); err == nil {
		t.Error("重复部署应报错")
	}
	// 停止
	if err := a.StopInstance(ctx, "inst-1"); err != nil {
		t.Fatalf("StopInstance 失败: %v", err)
	}
	// 引擎应已移除
	if _, ok := a.GetEngine("inst-1"); ok {
		t.Error("停止后引擎应被移除")
	}
	// 停止不存在的实例应报错
	if err := a.StopInstance(ctx, "nope"); err == nil {
		t.Error("停止不存在的实例应报错")
	}
}

// TestListInstances 实例列表。
func TestListInstances(t *testing.T) {
	a := newTestAgent(t)
	ctx := context.Background()
	_, _ = a.DeployInstance(ctx, "inst-a", "ma", "/m/ma", "fake", 0)
	_, _ = a.DeployInstance(ctx, "inst-b", "mb", "/m/mb", "fake", 0)

	list := a.ListInstances()
	if len(list) != 2 {
		t.Errorf("期望 2 个实例，实际 %d", len(list))
	}
}

// TestHealthCheckSelfHeal 健康检查：健康实例不受影响。
func TestHealthCheckSelfHeal(t *testing.T) {
	a := newTestAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = a.DeployInstance(ctx, "inst-ok", "m", "/m/m", "fake", 0)
	a.HealthCheck(ctx)
	list := a.ListInstances()
	if len(list) != 1 || list[0].State != InstReady {
		t.Errorf("健康实例不应被重启: %+v", list)
	}
}

// TestHealthCheckFailedInstanceRestart 不健康实例触发重启。
func TestHealthCheckFailedInstanceRestart(t *testing.T) {
	a := newTestAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 部署 fake 后手动移除引擎引用模拟故障
	_, _ = a.DeployInstance(ctx, "inst-bad", "m", "/m/m", "fake", 0)
	a.mu.Lock()
	delete(a.engines, "inst-bad") // 模拟引擎丢失
	a.mu.Unlock()
	// 健康检查应尝试重启（fake 可恢复）
	a.HealthCheck(ctx)
	list := a.ListInstances()
	if len(list) == 0 {
		t.Error("实例不应被删除")
	}
}
