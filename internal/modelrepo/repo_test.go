package modelrepo

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/meshserve/internal/raftstore"
)

func newTestRepo(t *testing.T) (*Repo, func()) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := t.TempDir()
	store, err := raftstore.Open(dir)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	repo, err := New(store, filepath.Join(dir, "models"), log)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return repo, func() { _ = store.Close() }
}

// TestRegisterLocal 注册本地模型目录。
func TestRegisterLocal(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()

	modelDir := filepath.Join(t.TempDir(), "qwen")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "model.safetensors"), []byte("fake-weights"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := repo.RegisterLocal(context.Background(), "qwen", modelDir, "vllm", "fp16", 1<<30)
	if err != nil {
		t.Fatalf("RegisterLocal 失败: %v", err)
	}
	if m.Name != "qwen" || m.Source != "local" {
		t.Errorf("模型字段错误: %+v", m)
	}
	if m.SHA256 == "" {
		t.Error("应生成权重校验和")
	}
	// 应可通过 Get 读取
	got, err := repo.Get(context.Background(), "qwen")
	if err != nil || got.Name != "qwen" {
		t.Errorf("Get 失败: %v, %+v", err, got)
	}
}

// TestRegisterLocal_NotFound 路径不存在应报错。
func TestRegisterLocal_NotFound(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	if _, err := repo.RegisterLocal(context.Background(), "m", "/nonexistent", "vllm", "fp16", 1); err == nil {
		t.Fatal("不存在的路径应报错")
	}
}

// TestRegisterLocal_NotDir 文件路径非目录应报错。
func TestRegisterLocal_NotDir(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterLocal(context.Background(), "m", f, "vllm", "fp16", 1); err == nil {
		t.Fatal("文件路径应报错（需目录）")
	}
}

// TestListDelete 列出与删除。
func TestListDelete(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	d := filepath.Join(t.TempDir(), "m")
	os.MkdirAll(d, 0o755)
	_, _ = repo.RegisterLocal(context.Background(), "m1", d, "fake", "fp16", 1)
	_, _ = repo.RegisterLocal(context.Background(), "m2", d, "fake", "fp16", 1)

	list, err := repo.List(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("List 错误: %v, len=%d", err, len(list))
	}
	if err := repo.Delete(context.Background(), "m1", false); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	list, _ = repo.List(context.Background())
	if len(list) != 1 || list[0].Name != "m2" {
		t.Errorf("删除后列表错误: %+v", list)
	}
}
