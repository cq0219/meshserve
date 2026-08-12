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

## 迭代记录（Change Log）

> 每次迭代完成后更新：新增功能 + 测试数据 + CI 状态，按里程碑归档。

### 里程碑总览

| 里程碑 | 目标 | 状态 |
|--------|------|------|
| M1 | 双机闭环（V0.x） | ✅ 已交付（v0.1.0 发布） |
| M2 | 生产可用（V1.0） | ✅ 已交付 |
| M3 | 分片 + 量化 + 负载均衡（V1.5） | ✅ 已交付（多租户待后续） |
| M4 | 多集群联邦 + GPU 虚拟化（V2.0） | 🔄 进行中（M4-1 集群级实例同步） |

### M1 — 双机闭环（2026-08-12，commit f890117）

| 类别 | 内容 |
|------|------|
| **新增功能** | CLI（init/join/run/model/status/version）；8 大核心模块（cluster/raftstore/scheduler/modelrepo/gateway/engine/agent/health）；OpenAI 兼容 API（非流式 + SSE）；统一错误处理 + 结构化日志；CI 流水线（GitHub Actions） |
| **测试数据** | 9 包单元测试全绿；核心包覆盖率：gateway 78.8% / raftstore 88.7% / health 93.9% / cluster 73%；双节点集成测试 PASS |
| **CI 结果** | 6/6 jobs 全绿（Lint / Unit / Cross-Compile ×3 / E2E） |
| **发布** | v0.1.0 Release：三平台二进制 + SHA256SUMS |

### M2 — 生产可用（2026-08-12，commit b6b492ae）

| 类别 | 内容 |
|------|------|
| **新增功能** | mDNS 自动发现（join 免地址）；Web 控制台（REST API + Go embed 内嵌前端，端口 8443）；Prometheus 指标（/metrics 文本格式）；调度器 PlaceN 多副本部署；CLI 配置新增 console.http_addr |
| **测试数据** | 11 包全绿；console 88.6% / mdns 68.2% / scheduler 72.2%；本地 E2E：控制台/API/指标/推理端点全部 200 |
| **CI 结果** | 6/6 jobs 全绿 |

### M3 — 分片 + 量化 + 负载均衡（2026-08-12，commit 0c326cd4）

| 类别 | 内容 |
|------|------|
| **新增功能** | 模型分片元数据（PipelineParallel + CLI --tp/--pp）；分片感知调度（TP 需单节点 GPU 数满足 / PP 跨节点 stage）；量化自动选择 PickQuant（fp16→bf16→int8→int4）；agent.DeploySpec 分片参数传递（fake 引擎 Shard() 展示）；网关负载均衡（LocalRouter 活跃计数 + 负载升序路由） |
| **测试数据** | 12 包 + 新增 13 用例全绿；平均覆盖率 57.3%（>50% 门禁）；CLI 分片注册链路验证通过 |
| **CI 结果** | 6/6 jobs 全绿 |

### M4-1 — 集群级实例状态同步（2026-08-12，commit e169f55）

| 类别 | 内容 |
|------|------|
| **新增功能** | cluster 节点标签随 gossip 扩散（console_port/gateway_port）；run 启动广播服务端口；console `/api/instances` 跨节点聚合（本机直读 + 远端 HTTP 拉取，返回带 node_id 的实例视图）；前端实例表新增节点列（本机标识） |
| **测试数据** | 新增 3 用例：cluster parseTags / TagsPropagate（真实双节点标签扩散）、console 多节点聚合（真实组网 + httptest 模拟远端实例）；受影响 3 包回归全绿 |
| **CI 结果** | 待验证（推送后更新） |
| **备注** | 修复 M3 遗留：agent.Instance.StartedAt 恢复 json tag |

## 测试

```bash
make test          # 单元测试 + 竞态检测 + 覆盖率
make test-integration  # 集成测试（fake engine 双节点）
make lint          # golangci-lint
make vet           # go vet
```

## 许可证

内部项目，未发布。
