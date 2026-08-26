# 单节点部署 Qwen3-8B（vLLM 引擎）

在 MeshServe 集群的单节点上，以 vLLM 引擎启用 Qwen3-8B 模型的完整操作指南。

> 其他 Qwen3 型号（4B/14B/32B/30B-A3B）仅需替换第 1 步的模型名与显存参数，其余步骤不变。

## 架构说明

MeshServe 对 vLLM 采用**直连模式**：它不负责拉起 vLLM 进程，而是探测本机 `127.0.0.1:8000` 上**已运行**的 vLLM OpenAI 兼容服务，就绪后挂载为推理实例。

```
┌────────────────────────────────────────────┐
│ 同一节点                                    │
│                                            │
│  vLLM 进程 ──:8000──┐                      │
│  (qwen3-8b)         │  HTTP 直连          │
│                     ▼                      │
│  meshserve run ──  agent(探测:8000)        │
│                    ├─ 网关 :8080 (OpenAI)  │
│                    └─ 控制台 :8443         │
└────────────────────────────────────────────┘
```

**启动顺序：先起 vLLM，再起 MeshServe。**

## 前置条件

| 项 | 要求 | 说明 |
|---|---|---|
| GPU | 单卡 ≥24 GB | Qwen3-8B fp16 权重约 16 GB + KV cache |
| vLLM | ≥ 0.6.0 | `pip install vllm` 或 Docker：`vllm/vllm-openai` |
| MeshServe | 已编译 | `make build`（生成 `./bin/meshserve`） |
| 网络 | 可访问 HuggingFace 或已本地下载模型 | 权重约 16 GB |

## 详细步骤

### 第 1 步：启动 vLLM 服务

**方式 A：pip 安装（推荐）**

```bash
vllm serve Qwen/Qwen3-8B \
  --served-model-name qwen3-8b \
  --host 127.0.0.1 \
  --port 8000 \
  --gpu-memory-utilization 0.90 \
  --max-model-len 32768
```

**方式 B：Docker**

```bash
docker run --gpus all --shm-size 16g -p 8000:8000 \
  vllm/vllm-openai:latest \
  --model Qwen/Qwen3-8B \
  --served-model-name qwen3-8b \
  --gpu-memory-utilization 0.90 \
  --max-model-len 32768
```

**验证就绪**（返回模型列表即成功）：

```bash
curl http://127.0.0.1:8000/v1/models
```

### 第 2 步：初始化集群（首次部署执行）

```bash
meshserve init --name prod
```

### 第 3 步：注册模型到 MeshServe

```bash
meshserve model register qwen3-8b \
  --path Qwen/Qwen3-8B \
  --engine vllm \
  --quant fp16 \
  --params 8
```

> `--params 8` 自动估算显存 ≈ 16.6 GB；显存紧张可改用 `--quant int8`（估算减半，需 vLLM 侧加载 int8 权重）。

查看注册结果：`meshserve model list`

### 第 4 步：启动 MeshServe 节点

```bash
meshserve run
```

启动日志关键行（出现即成功）：

```
部署实例 id=inst-qwen3-8b-restore model=qwen3-8b engine=vllm ...
实例就绪 id=inst-qwen3-8b-restore addr=127.0.0.1:8000
模型已恢复 model=qwen3-8b instance=inst-qwen3-8b-restore
```

若出现 `vLLM 服务未就绪`：确认第 1 步服务正常、端口为 8000 且与本节点同机。

### 第 5 步：验证推理（走 MeshServe 网关）

```bash
# 集群状态
meshserve status

# 模型列表
curl http://localhost:8080/v1/models

# 对话推理
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3-8b","messages":[{"role":"user","content":"用一句话介绍你自己"}]}'
```

Web 控制台：`http://<节点IP>:8443` —— 集群实例表应显示 `qwen3-8b / vllm / ready` 及显存占用；节点表显示 GPU 资源（型号/数量/总显存）。

## 参数说明

### vLLM 侧

| 参数 | 作用 | 建议值 |
|---|---|---|
| `--served-model-name` | vLLM 对外模型名（建议与 MeshServe 注册名一致） | `qwen3-8b` |
| `--gpu-memory-utilization` | 显存占用上限，为 KV cache 预留 | 0.85–0.92 |
| `--max-model-len` | 上下文长度（越大 KV cache 越吃显存） | 24 GB 单卡 ≤ 32768 |
| `--tensor-parallel-size` | 多卡张量并行（TP） | 24 GB 单卡 = 1；多卡按卡数 |

### MeshServe 侧

| 参数 | 作用 | 说明 |
|---|---|---|
| `--path` | 模型权重路径/HF 模型名（元数据记录用） | 与 vLLM 加载模型对应 |
| `--engine` | 推理引擎 | 必须 `vllm` |
| `--quant` | 量化档位 | `fp16`/`bf16`/`int8`/`int4`，参与显存估算 |
| `--params` | 参数量（十亿） | 用于显存估算 |
| `--vram` | 显式覆盖显存需求（字节） | 优先级高于 `--params` |

## 常见问题（FAQ）

| 现象 | 原因 | 解决 |
|---|---|---|
| `vLLM 服务未就绪` | vLLM 未启动 / 端口不是 8000 / 不在同机 | 先起 vLLM；MeshServe 当前固定探测 `127.0.0.1:8000` |
| 显存不足（OOM） | KV cache 或权重超显存 | 三选一：MeshServe 注册 `--quant int8`；vLLM `--max-model-len 16384`；`--gpu-memory-utilization 0.80` |
| 请求返回模型不存在 | 模型名不匹配 | `curl /v1/models` 确认实际模型名，与注册名保持一致 |
| 控制台 GPU 显示"无" | 节点无 NVIDIA GPU 或 nvidia-smi 不可用 | 真实 GPU 环境自动采集；无 GPU 时属正常降级 |

## 变体：其他 Qwen3 型号

| 模型 | 显存需求（fp16） | 推荐配置 |
|---|---|---|
| Qwen3-4B | ≈ 8 GB | 单卡 12 GB；`--max-model-len 16384` |
| Qwen3-14B | ≈ 28 GB | 单卡 32 GB；或 2 卡 TP=2 |
| Qwen3-30B-A3B（MoE） | ≈ 18 GB（激活 3B） | 单卡 24 GB；`--max-model-len 32768` |
| Qwen3-32B | ≈ 64 GB | 2× 卡 TP=2 或 4 卡 TP=4（int8） |

MoE 模型示例（vLLM 侧）：

```bash
vllm serve Qwen/Qwen3-30B-A3B \
  --served-model-name qwen3-30b \
  --gpu-memory-utilization 0.90 \
  --max-model-len 32768
```

## 停止与清理

```bash
# 停止 MeshServe（Ctrl+C 优雅退出）
# 停止 vLLM
pkill -f "vllm serve"        # 或 docker stop <容器>

# 删除模型元数据（保留权重）
meshserve model remove qwen3-8b

# 彻底清理集群（删除本地数据目录 ~/.meshserve）
rm -rf ~/.meshserve
```
