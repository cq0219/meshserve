// Package raftstore 提供集群配置与状态的持久化 KV 存储。
//
// 落地说明（ADR-02 落地偏差）：V1 使用 bbolt 单机持久化 + 确定性 Leader 选举
// （节点 ID 最小者为 Leader，避免引入 etcd-raft 的运维复杂度与编译成本）。
// 接口与 etcd-raft 对齐（Get/Put/Delete/Watch/Leader），M2 可无缝替换为正式 Raft 实现。
package raftstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Store 配置与状态存储。
type Store struct {
	db     *bolt.DB
	mu     sync.Mutex
	peers  []string // 当前节点列表（用于 Leader 判定）
	wat    []*watcher
	leader string
}

type watcher struct {
	ch     chan string // 变更的 key
	prefix string
}

var (
	bucketMeta   = []byte("meta")
	bucketConfig = []byte("config")
	bucketModels = []byte("models")
	keyPeers     = []byte("peers")
	keyJoinToken = []byte("join_token")
	keyClusterID = []byte("cluster_id")
)

// Open 打开（或创建）数据目录下的 raftstore 数据库。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	db, err := bolt.Open(filepath.Join(dataDir, "meshserve.db"), 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketMeta, bucketConfig, bucketModels} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	s := &Store{db: db, wat: make([]*watcher, 0)}
	s.reloadLeader()
	return s, nil
}

// Close 关闭存储。
func (s *Store) Close() error { return s.db.Close() }

// ============ KV 基础 ============

// Put 写入配置 KV（写后触发 Watch 通知）。
func (s *Store) Put(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketConfig).Put([]byte(key), value)
	})
	if err == nil {
		s.notify(key)
	}
	return err
}

// Get 读取配置 KV。
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketConfig).Get([]byte(key))
		if v == nil {
			return fmt.Errorf("key %q 不存在", key)
		}
		out = make([]byte, len(v))
		copy(out, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete 删除配置 KV。
func (s *Store) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketConfig).Delete([]byte(key))
	})
	if err == nil {
		s.notify(key)
	}
	return err
}

// Watch 订阅 key 前缀的变更（prefix 为空表示全部）。
func (s *Store) Watch(ctx context.Context, prefix string) <-chan string {
	ch := make(chan string, 16)
	s.mu.Lock()
	s.wat = append(s.wat, &watcher{ch: ch, prefix: prefix})
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		for i, w := range s.wat {
			if w.ch == ch {
				s.wat = append(s.wat[:i], s.wat[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()
	return ch
}

func (s *Store) notify(key string) {
	for _, w := range s.wat {
		if w.prefix == "" || strings.HasPrefix(key, w.prefix) {
			select {
			case w.ch <- key:
			default:
			}
		}
	}
}

// ============ 集群元数据 ============

// SetClusterMeta 写入集群初始化元数据（cluster_id / join_token）。
func (s *Store) SetClusterMeta(clusterID, joinToken string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if err := b.Put(keyClusterID, []byte(clusterID)); err != nil {
			return err
		}
		return b.Put(keyJoinToken, []byte(joinToken))
	})
}

// ClusterID 返回集群 ID。
func (s *Store) ClusterID() (string, error) {
	var out string
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(keyClusterID)
		if v == nil {
			return fmt.Errorf("集群尚未初始化")
		}
		out = string(v)
		return nil
	})
	return out, err
}

// JoinToken 返回加入令牌。
func (s *Store) JoinToken() (string, error) {
	var out string
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(keyJoinToken)
		if v == nil {
			return fmt.Errorf("加入令牌不存在（请先 init）")
		}
		out = string(v)
		return nil
	})
	return out, err
}

// SetPeers 记录集群节点列表（用于 Leader 判定）。
func (s *Store) SetPeers(peers []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers = append([]string(nil), peers...)
	raw, _ := json.Marshal(s.peers)
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMeta).Put(keyPeers, raw)
	})
	if err == nil {
		s.recomputeLeader()
	}
	return err
}

// Peers 返回节点列表。
func (s *Store) Peers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.peers...)
}

// Leader 返回当前 Leader 节点 ID。
func (s *Store) Leader() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leader
}

// IsLeader 判断本节点是否为 Leader。
func (s *Store) IsLeader(nodeID string) bool { return s.Leader() == nodeID }

// recomputeLeader 确定性选举：节点 ID 字典序最小者为 Leader。
func (s *Store) recomputeLeader() {
	if len(s.peers) == 0 {
		s.leader = ""
		return
	}
	sorted := append([]string(nil), s.peers...)
	sort.Strings(sorted)
	s.leader = sorted[0]
}

func (s *Store) reloadLeader() {
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(keyPeers)
		if v != nil {
			_ = json.Unmarshal(v, &s.peers)
		}
		return nil
	})
	s.recomputeLeader()
}

// ============ 模型元数据 ============

// PutModel 保存模型元数据（JSON）。
func (s *Store) PutModel(name string, meta []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketModels).Put([]byte(name), meta)
	})
}

// GetModel 读取模型元数据。
func (s *Store) GetModel(name string) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketModels).Get([]byte(name))
		if v == nil {
			return fmt.Errorf("模型 %q 不存在", name)
		}
		out = make([]byte, len(v))
		copy(out, v)
		return nil
	})
	return out, err
}

// ListModels 列出全部模型名。
func (s *Store) ListModels() ([]string, error) {
	var out []string
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketModels).ForEach(func(k, _ []byte) error {
			out = append(out, string(k))
			return nil
		})
	})
	return out, err
}

// DeleteModel 删除模型元数据。
func (s *Store) DeleteModel(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketModels).Delete([]byte(name))
	})
}
