# 单节点部署 Qwen3-8B（vLLM 引擎）

在 MeshServe 集群的单节点上，以 vLLM 引擎启用 Qwen3-8B 模型——**MeshServe 自动拉起 vLLM 进程**并管理其生命周期（启动/就绪轮询/健康自愈/停止）。

> 其他 Qwen3 型号（4B/14B/32B/30B-A3B）仅需替换模型路径/名称与显存参数，其余步骤不变。

## 架构说明

MeshServe 对 vLLM 采用**进程拉起模式（M7）**：

1. **自动拉起**：注册模型后，MeshServe 在本机动态分配端口，执行 `vllm serve <模型路径> --host 127.0.0.1 --port <端口> --served-model-name <模型名>` 拉起进程，轮询 `/v1/models` 直至就绪（默认 300s）；
2. **生命周期管理**：停用/删除模型时自动终止进程；进程崩溃由健康探针发现并自动重启（自愈）；
3. **复用兼容**：若目标端口已有就绪的 vLLM 服务（手动启动），直接复用不重复拉起。

```
┌────────────────────────────────────────────┐
│ 同一节点                                    │
│                                            │
│  MeshServe run ──拉起──▶ vLLM 子进程 :<动态端口>│
│   (agent)               (qwen3-8b)         │
│    ├── 就绪轮询/健康探针 ◀── /v1/models     │
│    ├── 网关 :8080 (OpenAI 兼容)            │
│    └── 控制台 :8443                        │
└────────────────────────────────────────────┘
```

**安装前提：本机已安装 vLLM（`pip install vllm`）。**

## 前置条件

| 项 | 要求 | 说明 |
|---|---|---|
| GPU | 单卡 ≥24 GB | Qwen3-8B fp16 权重约 16 GB + KV cache |
| vLLM | ≥ 0.6.0 | `pip install vllm` 或 Docker：`vllm/vllm-openai`（宿主机运行） |
| 模型 | 本地权重目录或 HF 模型名 | 权重约 16 GB |
| MeshServe | 已编译 | `make build`（生成 `./bin/meshserve`） |

## 详细步骤

### 第 1 步：安装 vLLM

```bash
pip install vllm
vllm --version   # 确认可用
```

### 第 2 步：初始化集群（首次部署执行）

```bash
meshserve init --name prod
```

### 第 3 步：注册模型到 MeshServe

```bash
meshserve model register qwen3-8b \
  --path /path/to/Qwen3-8B \    # 本地权重目录（或 HF 模型名）
  --engine vllm \
  --quant fp16 \
  --params 8
```

### 第 4 步：启动 MeshServe（自动拉起 vLLM）

```bash
meshserve run
```

启动日志关键行（vLLM 被自动拉起并就绪）：

```
部署实例 id=inst-qwen3-8b-restore model=qwen3-8b engine=vllm ...
vLLM 进程已启动 bin=vllm args="serve /path/to/Qwen3-8B --host 127.0.0.1 --port 54321 --served-model-name qwen3-8b" pid=12345
vLLM 进程已就绪 addr=127.0.0.1:54321 model=qwen3-8b
模型已恢复 model=qwen3-8b instance=inst-qwen3-8b-restore
```

若出现 `未找到 vllm 可执行文件`：确认 vLLM 已安装（`pip install vllm`），或通过配置指定路径（见下）。

### 第 5 步：验证推理（走 MeshServe 网关）

```bash
# 集群状态
meshserve status

# 对话推理（网关自动路由到拉起进程）
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3-8b","messages":[{"role":"user","content":"用一句话介绍你自己"}]}'
```

Web 控制台：`http://<节点IP>:8443` —— 模型状态 `online`；GPU 监控实时显示占用率与显存。

## 可选配置（agent 段）

| 配置项 | 默认 | 说明 |
|---|---|---|
| `agent.vllm_bin` | `vllm` | vLLM 可执行文件（支持"命令 + 前缀参数"，如 `python /opt/fake_vllm.py`） |
| `agent.vllm_timeout_seconds` | `300` | 启动就绪等待秒数（大模型加载慢可调大） |
| `agent.vllm_extra_args` | 空 | 附加启动参数，空格分隔，如 `--max-model-len 32768 --gpu-memory-utilization 0.9` |

示例（`~/.meshserve/config.yaml`）：

```yaml
agent:
  vllm_bin: vllm
  vllm_timeout_seconds: 600
  vllm_extra_args: "--max-model-len 32768 --gpu-memory-utilization 0.9"
```

## 常见问题（FAQ）

| 现象 | 原因 | 解决 |
|---|---|---|
| 注册后模型状态 `error`，`last_error` 提示"未找到 vllm 可执行文件" | vLLM 未安装或不在 PATH | `pip install vllm`；或配置 `agent.vllm_bin` 指定绝对路径 |
| 状态 `error`，提示"等待 vLLM 就绪超时" | 模型加载慢 / 参数不当 / 显存不足 | 调大 `vllm_timeout_seconds`；检查 `vllm_extra_args`（减 max-model-len、降 gpu-memory-utilization）；看 run 日志中 vLLM 输出 |
| 显存不足（OOM） | KV cache 或权重超显存 | `vllm_extra_args: "--max-model-len 16384 --gpu-memory-utilization 0.80"` |
| 请求返回模型不存在 | 模型名不匹配 | 使用注册名（`qwen3-8b`）请求，或 `curl /v1/models` 查看实际名称 |
| 控制台 GPU 显示"无" | 节点无 NVIDIA GPU 或 nvidia-smi 不可用 | 真实 GPU 环境自动采集；无 GPU 时属正常降级 |

## 变体：其他 Qwen3 型号

| 模型 | 显存需求（fp16） | 推荐配置 |
|---|---|---|
| Qwen3-4B | ≈ 8 GB | 单卡 12 GB；`--max-model-len 16384` |
| Qwen3-14B | ≈ 28 GB | 单卡 32 GB；或 2 卡 TP=2 |
| Qwen3-30B-A3B（MoE） | ≈ 18 GB（激活 3B） | 单卡 24 GB；`--max-model-len 32768` |
| Qwen3-32B | ≈ 64 GB | 2× 卡 TP=2 或 4 卡 TP=4（int8） |

多卡 TP：注册时 `--tp 2`，MeshServe 自动追加 `--tensor-parallel-size 2` 拉起。

## 停止与清理

```bash
# 停止 MeshServe（Ctrl+C 优雅退出，同时终止拉起的 vLLM 进程）
# 删除模型（终止 vLLM 进程 + 删除元数据）
meshserve model remove qwen3-8b

# 彻底清理集群（删除本地数据目录 ~/.meshserve）
rm -rf ~/.meshserve
```
