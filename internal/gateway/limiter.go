package gateway

import (
	"sync"
	"time"
)

// TokenBucket 令牌桶限流器。
type TokenBucket struct {
	mu       sync.Mutex
	rate     float64 // 每秒补充令牌数
	burst    float64 // 桶容量
	tokens   float64
	lastFill time.Time
}

// NewTokenBucket 创建令牌桶；rate<=0 表示不限流。
func NewTokenBucket(rate int) *TokenBucket {
	if rate <= 0 {
		return &TokenBucket{rate: 0, burst: 1, tokens: 1, lastFill: time.Now()}
	}
	return &TokenBucket{rate: float64(rate), burst: float64(rate), tokens: float64(rate), lastFill: time.Now()}
}

// Allow 判断是否允许通过（消费 1 个令牌）。
func (t *TokenBucket) Allow() bool {
	if t.rate <= 0 {
		return true // 不限流
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	// 补充令牌
	elapsed := now.Sub(t.lastFill).Seconds()
	t.tokens = min(t.burst, t.tokens+elapsed*t.rate)
	t.lastFill = now
	if t.tokens >= 1 {
		t.tokens--
		return true
	}
	return false
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
