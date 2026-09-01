// fakevllm 模拟 vllm serve 进程：解析 --port 参数、监听 HTTP、响应 OpenAI 兼容端点。
// 供引擎/编排测试验证进程拉起、就绪探测与参数注入（非真实推理）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	port := 8000
	for i := 0; i < len(os.Args); i++ {
		if os.Args[i] == "--port" && i+1 < len(os.Args) {
			if p, err := strconv.Atoi(os.Args[i+1]); err == nil {
				port = p
			}
		}
	}
	// 写入启动参数到临时文件（测试可断言参数注入）：<tmp>/fakevllm-args-<port>.log
	_ = os.WriteFile(
		filepath.Join(os.TempDir(), fmt.Sprintf("fakevllm-args-%d.log", port)),
		[]byte(strings.Join(os.Args[1:], " ")), 0o644)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": "fake-model", "object": "model"}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "fake-1", "object": "chat.completion", "model": "fake-model",
			"choices": []map[string]any{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "fake reply"},
			}},
		})
	})

	addr := "127.0.0.1:" + strconv.Itoa(port)
	fmt.Println("fakevllm listening on", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "fakevllm exit:", err)
		os.Exit(1)
	}
}
