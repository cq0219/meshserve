// Package modelrepo 实现模型仓库：元数据管理、本地导入、权重校验。
// 对应方案 4.5 模型仓库模块。
package modelrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/yourorg/meshserve/internal/raftstore"
)

// Repo 模型仓库。
type Repo struct {
	store *raftstore.Store
	dir   string
	log   *slog.Logger
}

// New 创建模型仓库（dir 为模型存储根目录）。
func New(store *raftstore.Store, dir string, log *slog.Logger) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建模型目录失败: %w", err)
	}
	return &Repo{store: store, dir: dir, log: log}, nil
}

// RegisterLocal 注册本地模型（path 为模型权重目录）。
func (r *Repo) RegisterLocal(ctx context.Context, name, path, engine, quant string, vramBytes uint64) (*raftstore.Model, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("模型路径无效: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("模型路径不存在: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("模型路径必须是目录: %s", abs)
	}
	m := &raftstore.Model{
		Name:           name,
		Version:        "v1",
		Path:           abs,
		Engine:         engine,
		Quant:          quant,
		VRAMBytes:      vramBytes,
		TensorParallel: 1,
		Replicas:       1,
		CreatedAt:      time.Now().Format(time.RFC3339),
		Source:         "local",
		SHA256:         dirChecksum(abs),
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := r.store.PutModel(name, mustEncode(m)); err != nil {
		return nil, fmt.Errorf("保存模型元数据失败: %w", err)
	}
	r.log.Info("本地模型已注册", "name", name, "path", abs)
	return m, nil
}

// List 列出全部模型。
func (r *Repo) List(ctx context.Context) ([]*raftstore.Model, error) {
	names, err := r.store.ListModels()
	if err != nil {
		return nil, err
	}
	out := make([]*raftstore.Model, 0, len(names))
	for _, n := range names {
		data, err := r.store.GetModel(n)
		if err != nil {
			continue
		}
		m, err := raftstore.DecodeModel(data)
		if err != nil {
			r.log.Warn("模型元数据损坏", "model", n, "err", err)
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Get 获取单个模型。
func (r *Repo) Get(ctx context.Context, name string) (*raftstore.Model, error) {
	data, err := r.store.GetModel(name)
	if err != nil {
		return nil, err
	}
	return raftstore.DecodeModel(data)
}

// Delete 删除模型（元数据 + 目录可选）。
func (r *Repo) Delete(ctx context.Context, name string, removeFiles bool) error {
	if err := r.store.DeleteModel(name); err != nil {
		return err
	}
	if removeFiles {
		_ = os.RemoveAll(filepath.Join(r.dir, name))
	}
	r.log.Info("模型已删除", "name", name, "remove_files", removeFiles)
	return nil
}

// ModelDir 返回某模型的权重目录。
func (r *Repo) ModelDir(name string) string {
	return filepath.Join(r.dir, name)
}

// mustEncode 序列化模型元数据（RegisterLocal 内部使用，失败视为编程错误）。
func mustEncode(m *raftstore.Model) []byte {
	b, err := m.Encode()
	if err != nil {
		panic(err)
	}
	return b
}

// dirChecksum 计算目录下所有文件的 SHA256（组合哈希，用于校验完整性）。
func dirChecksum(dir string) string {
	h := sha256.New()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		_, _ = io.Copy(h, f)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}
