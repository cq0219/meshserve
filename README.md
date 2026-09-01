# MeshServe

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

本地多机大语言模型推理集群系统 —— 自动发现组网、免 Kubernetes、一键部署。

对应文档：《MeshServe 模型服务系统架构设计方案》《MeshServe 落地实现方案》。

## 功能特性

- 🔗 自动发现组网：memberlist (SWIM/Gossip) 成员管理，新节点自动加入
- 🚀 免 K8s：自研轻量控制面（bbolt 持久化 + 确定性 Leader 选举）
- 🤖 引擎插件化：vLLM / SGLang / llama.cpp / fake（开发调试）
- 📡 OpenAI 兼容 API：`/v1/chat/completions`（含 SSE 流式）、`/v1/models`
- 🛡️ 统一错误处理：错误分类 + 结构化日志 + 优雅退出
- ✅ 开箱即用：零 GPU 环境可用 `--engine fake` 完整演示

## 安装要求（Ubuntu）

- **NVIDIA 驱动**：RTX 50 系列必须安装**带 `-open` 后缀的驱动版本**（如 `nvidia-driver-580-open`），驱动版本需 **≥ 580**；其他显卡建议 ≥580。CUDA Toolkit 无需单独安装（vLLM 的 pip wheel 自带 CUDA runtime）
- **Python**：默认 **3.10** 版本（脚本参数 `PY_VERSION` 可覆盖为 3.11/3.12）；vLLM 直接装入系统 Python（无 venv 隔离）
- **Go**：≥ 1.25（仅源码构建 MeshServe 需要；用预编译二进制可跳过）
- **一键安装**：[`docs/install-ubuntu.sh`](docs/install-ubuntu.sh) 自动完成驱动（50 系自动选 `-open`）/Python 3.10/Go/vLLM/MeshServe 安装，支持 `--skip-driver`、`--no-meshserve`、`--gpu-gen 50` 等参数，详见 [安装指南](docs/install-ubuntu.md)

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

**方式一：免 token 自动加入（M6，推荐）**——后续机器装好直接 `run`，mDNS 自动发现并加入：

```bash
# 机器 A（第一台，需先 init 一次）
meshserve init --name prod
meshserve run --engine vllm

# 机器 B/C/...（无需 init、无需 join、无需 token，直接 run）
meshserve run --engine vllm
```

**方式二：手动 join**（关闭 auto_join 或跨网段时）：

```bash
# 机器 B
meshserve join --token <TOKEN> <A的IP>
meshserve run --engine vllm
```

> 安全说明：自动加入基于局域网 mDNS 广播 + 内网信任模型（免 token）。生产环境建议设置 `cluster.auto_join: false` 并配合防火墙/网络隔离，使用手动 join 或 `join_addr` 显式指定。

## 模型部署

- [单节点部署 Qwen3-8B（vLLM 引擎）](docs/deploy-qwen-vllm.md) —— 完整部署指南（vLLM 直连模式、参数说明、FAQ、多型号变体）

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
| **CI 结果** | 6/6 jobs 全绿（Lint / Unit / Cross-Compile ×3 / E2E） |
| **备注** | 修复 M3 遗留：agent.Instance.StartedAt 恢复 json tag |

### Bugfix — status 超时 + GPU 资源显示（2026-08-26）

| 类别 | 内容 |
|------|------|
| **Bug 修复** | ① `meshserve status` 超时：根因是 `raftstore.Open` 的 bbolt 独占文件锁与运行中 `run` 进程冲突（等待 2s 后 timeout）。修复：status 优先走本机控制台 HTTP API（在线全量状态），回退只读打开本地库（`OpenReadOnly`，不创建 db、run 未运行时不冲突）。② 集群实例无 GPU 资源：节点启动时采集 GPU（型号/数量/总显存）写入 gossip 标签；控制台节点表新增「GPU 资源」列、实例表新增「显存占用」列 |
| **测试数据** | 新增 raftstore OpenReadOnly 用例（不存在报错 / 可读元数据）；raftstore/console/agent/cluster 回归全绿 |
| **本地 E2E 验证** | status 1.5s 返回在线状态（此前 2s 超时报错）；节点 API 返回 gpu_model/gpu_count/gpu_vram 标签；实例 API 返回 vram_used |

### M4-2 — GPU 实时监控（2026-08-26）

| 类别 | 内容 |
|------|------|
| **新增功能** | console 新增 `/api/gpu` 实时采集端点（每张卡型号/总显存/已用显存/利用率，nvidia-smi 不可用返回空数组）；前端新增「GPU 监控」面板（利用率进度条 + 已用/可用/总显存，5 秒轮询）；节点表 GPU 列改为「N 卡 + 型号」简洁形式 |
| **测试数据** | 新增 console 2 用例：无 GPU 空数组 / fake 注入数据字段校验（util_pct/vram_total/vram_used）；console/agent/raftstore 回归全绿 |
| **本地 E2E 验证** | `/api/gpu` 返回 `[]`（无 GPU 正确降级）；首页含 GPU 监控面板与轮询逻辑 |

### M5 — 模型注册与交互界面（2026-08-26）

| 类别 | 内容 |
|------|------|
| **新增功能** | **模型管理**：Web 注册表单（名称/引擎/路径或外部端点/量化/参数量/显存/TP/PP/副本）、编辑、停用/启用、删除（二次确认）、搜索 + 引擎/状态筛选、五态状态推导（online/deploying/offline/error/disabled）、部署失败原因展示。**模型对话**：聊天工作台（在线模型选择/搜索、temperature/max_tokens/top_p 参数面板、SSE 流式逐字渲染、耗时与 token 统计、错误提示条、localStorage 会话历史、新建对话）。**网关**：CORS 中间件（控制台跨端口调用）；LocalRouter agent 回退路由（Web 注册/启用模型免手动注册即可对话） |
| **API 新增** | `POST /api/models`、`PATCH /api/models/{name}`、`POST /api/models/{name}/toggle`、`DELETE /api/models/{name}`、`GET /api/models?q=&engine=&status=`（筛选） |
| **测试数据** | 新增 console 6 用例（注册/端点模式/校验错误/停用启用/删除/筛选）；console/gateway/agent/raftstore/cluster/modelrepo/health 7 包回归全绿 |
| **本地 E2E 验证** | Web 注册 fake 模型 → online；网关 SSE 流式对话返回内容；/v1/models 含 Web 注册模型；停用→disabled、删除→204 |

### M6 — 免 token 自动加入集群（2026-08-26）

| 类别 | 内容 |
|------|------|
| **新增功能** | 新节点装好直接 `run` 自动入网：未初始化节点启动时 mDNS 自动发现引导节点（跳过自己）并加入，免 token 免地址；新增配置 `cluster.auto_join`（默认 true，生产可关）；已初始化（bootstrap）或已配 join_addr 时自动跳过 |
| **测试数据** | config/console/raftstore 回归全绿 |
| **本地 E2E 验证** | A init+run；B 直接 run（无 join_addr）→ 日志"自动发现引导节点"→ A 控制台 node_count=2/online=2 |

### M9 — 跨节点 PP 编排（2026-09-01，commit b8bb319）

| 类别 | 内容 |
|------|------|
| **新增功能** | **跨节点 PP 编排**：vLLM 引擎进程拉起模式注入 `--pipeline-parallel-size` / `--distributed-executor-backend`（worker 无 HTTP API 启动即就绪）；agent 管理 HTTP API（9100：`/api/deploy`、`/api/stop`、`/api/instances`）供控制面分发远端部署；PPCoordinator 按 rank 并发部署到多节点、worker 端口顺延（rank0+i）、任一失败全量回滚；RemoteEngine 网关远端转发（rank0 暴露 OpenAI API、worker 仅参与计算）；console 注册 PP>1 走编排器、停用/删除跨节点清理；Web 表单新增「vLLM 服务端口」 |
| **测试数据** | 新增 engine fakevllm 进程模拟（Spawn 参数注入 / PpWorker 即就绪 / Probe 探测）、agent API 往返、scheduler 双节点并发部署/失败回滚测试；`go build` + `go vet` + `go test ./...` 全绿 |
| **本地 E2E 验证** | 双节点（A init+run、B join+run）注册 PP=2 模型 → rank0=A:8000、rank1=B:8001（端口顺延正确）→ 网关 `/v1/models` 可见 → Chat 经 RemoteEngine 转发 rank0 成功 → 停用后双节点实例清空 |

## 测试

```bash
make test          # 单元测试 + 竞态检测 + 覆盖率
make test-integration  # 集成测试（fake engine 双节点）
make lint          # golangci-lint
make vet           # go vet
```

## 许可证

[MIT License](LICENSE) © 2026 cq0219 — 允许自由使用、修改、分发（含商用），需保留版权声明。
