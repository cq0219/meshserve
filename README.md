# MeshServe

本地多机大语言模型推理集群系统 —— 自动发现组网、免 Kubernetes、一键部署。

对应文档：《MeshServe 模型服务系统架构设计方案》《MeshServe 落地实现方案》。

## 功能特性

- 🔗 自动发现组网：memberlist (SWIM/Gossip) 成员管理，新节点自动加入
- 🚀 免 K8s：自研轻量控制面（bbolt 持久化 + 确定性 Leader 选举）
- 🤖 引擎插件化：vLLM / SGLang / llama.cpp / fake（开发调试）
- 📡 OpenAI 兼容 API：`/v1/chat/completions`（含 SSE 流式）、`/v1/models`
- 🛡️ 统一错误处理：错误分类 + 结构化日志 + 优雅退出
- ✅ 开箱即用：零 GPU 环境可用 `--engine fake` 完整演示

## 快速开始（单机演示，无需 GPU）

```bash
make build                      # 编译
./bin/meshserve init --name demo # 初始化集群
./bin/meshserve model register demo-model --path /path/to/model --engine fake --params 0.5
./bin/meshserve run --engine fake &   # 启动节点（Agent + 网关）
sleep 2
curl http://localhost:8080/v1/models
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"demo-model","messages":[{"role":"user","content":"你好"}]}'
```

> 说明：`register` 仅注册元数据；`run` 启动时会自动把已注册的本地模型部署为实例（`fake` 引擎无需真实权重路径，`--path` 传任意目录即可）。

## 双机组网

```bash
# 机器 A
meshserve init --name prod
meshserve run --engine vllm

# 机器 B
meshserve join --token <TOKEN> <A的IP>
meshserve run --engine vllm
```

## 项目结构

```
cmd/meshserve            CLI 入口（init/join/run/model/status/version）
cmd/meshserve-agent      独立节点代理（可选）
cmd/meshserve-gateway    独立推理网关（可选）
internal/cluster         成员管理（memberlist 封装、事件流）
internal/raftstore       持久化 KV + Leader 选举 + 模型元数据
internal/scheduler       资源感知调度（显存约束 + 评分放置）
internal/modelrepo       模型仓库（注册/列表/校验和）
internal/gateway         OpenAI 兼容网关（路由/SSE/限流）
internal/engine          引擎适配层（vLLM/llama.cpp/fake）
internal/agent           节点代理（资源采集/实例管理/自愈）
internal/health          健康探针（Liveness/Readiness/Startup）
internal/observ          结构化日志
```

## 常用命令

| 命令 | 说明 |
|------|------|
| `meshserve init` | 初始化集群（生成集群 ID / 加入令牌 / 节点 ID） |
| `meshserve join [addr]` | 加入集群 |
| `meshserve run` | 启动节点（Agent + 网关） |
| `meshserve model register <name>` | 注册模型 |
| `meshserve model list` | 列出模型 |
| `meshserve model remove <name>` | 删除模型 |
| `meshserve status` | 查看集群状态 |
| `meshserve version` | 版本信息 |

## 配置

默认配置目录 `~/.meshserve/`，可用 `-c` 指定配置文件。环境变量 `MESHSERVE_LOG_LEVEL`、`MESHSERVE_GATEWAY_ADDR` 可覆盖。

## 测试

```bash
make test          # 单元测试 + 竞态检测 + 覆盖率
make test-integration  # 集成测试（fake engine 双节点）
make lint          # golangci-lint
make vet           # go vet
```

## 许可证

内部项目，未发布。
