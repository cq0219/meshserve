package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLiveness 存活探针恒 200。
func TestLiveness(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("liveness 应 200，实际 %d", w.Code)
	}
}

// TestReadiness 未就绪返回 503。
func TestReadiness(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("未就绪应 503，实际 %d", w.Code)
	}
	// 就绪后 200
	s.SetReady(true)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("就绪后应 200，实际 %d", w.Code)
	}
}

// TestStartup 启动探针。
func TestStartup(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/startupz", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("未完成启动应 503，实际 %d", w.Code)
	}
	s.SetStartup(true)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("启动完成后应 200，实际 %d", w.Code)
	}
}

// TestWaitReady 等待就绪。
func TestWaitReady(t *testing.T) {
	s := New()
	s.SetReady(true)
	if !s.WaitReady(t.Context(), 0) {
		t.Error("已就绪时 WaitReady 应返回 true")
	}
	s2 := New()
	if s2.WaitReady(t.Context(), time.Millisecond*50) {
		t.Error("未就绪时 WaitReady 应超时返回 false")
	}
}
