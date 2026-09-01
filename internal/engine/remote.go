package engine

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// NewRemote 创建远端引擎代理（网关转发到模型实际所在节点，如 PP rank0 的 vLLM）。
// addr 为远端 OpenAI 兼容 API 地址，形如 "10.0.0.5:8000"；modelName 为注册模型名。
func NewRemote(addr, modelName string) Engine {
	host := addr
	// 容错：调用方可能传了 http:// 前缀
	if len(host) >= 7 && host[:7] == "http://" {
		host = host[7:]
	}
	e := &VLLMEngine{
		addr:   host,
		client: &http.Client{Timeout: 60 * time.Second},
		model:  modelName,
		spawn:  false,
	}
	return e
}

// RemoteProbe 探测远端引擎是否就绪（供部署编排器在注册路由前校验）。
func RemoteProbe(ctx context.Context, addr string, timeout time.Duration) error {
	host := addr
	if len(host) >= 7 && host[:7] == "http://" {
		host = host[7:]
	}
	if host == "" {
		return fmt.Errorf("远端引擎地址为空")
	}
	probe := &VLLMEngine{addr: host, client: &http.Client{Timeout: 30 * time.Second}, spawn: false}
	return probe.waitReady(ctx, timeout)
}
