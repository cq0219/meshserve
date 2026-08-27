# MeshServe Ubuntu 一键安装指南

目标：**一条命令装完所有依赖并可用**——显卡驱动、Go、Python、vLLM、MeshServe 二进制，覆盖从裸机到 `meshserve run` 的全过程。

## 使用

```bash
# 拷贝脚本到目标机后
sudo bash install-ubuntu.sh                 # 全量安装（含驱动，约 15-30 分钟）
sudo bash install-ubuntu.sh --skip-driver   # 跳过驱动（容器/已有驱动/无 GPU 场景）
sudo bash install-ubuntu.sh --no-meshserve  # 只装依赖（想手动装 MeshServe 时）
```

## 安装矩阵

| 组件 | 版本要求 | 安装方式 | 说明 |
|---|---|---|---|
| NVIDIA 驱动 | ≥ 550（CUDA 12.4） | apt `nvidia-driver-550` | 系统级，已装且满足则跳过 |
| **CUDA Toolkit** | **无需安装** | — | PyTorch/vLLM 的 pip wheel 自带 CUDA runtime，只要求驱动版本达标 |
| Python | 3.10–3.12 | apt + venv | 依赖装入 `/opt/meshserve/venv`，不污染系统 |
| vLLM | 最新 | `pip install vllm`（venv 内） | 自动拉取 pytorch-cuda 12.x wheel（约 5-8GB） |
| Go | ≥ 1.25（匹配 go.mod） | 官方 tarball → `/usr/local/go` | 仅构建 MeshServe 需要；用预编译二进制则可不装 |
| MeshServe | 最新 | 源码构建或 GitHub Release 二进制 | `CGO_ENABLED=0`，无运行时依赖 |

## 设计要点（为什么"一键"可行）

### 1. 核心简化：CUDA Toolkit 不装
vLLM 依赖 PyTorch，而 PyTorch 的 pip wheel **内置 CUDA runtime**（`pytorch-cuda`）。只需系统 NVIDIA 驱动满足版本门槛（≥550 支持 CUDA 12.4），无需安装/配置 CUDA Toolkit、无需设 `CUDA_HOME`、无版本矩阵噩梦。这是整个"一键"成立的关键。

### 2. 依赖隔离：Python venv
所有 Python 依赖装进 `/opt/meshserve/venv`，系统 Python 保持干净；升级/卸载互不影响。使用 vllm 命令前先 `source /opt/meshserve/venv/bin/activate`。

### 3. 版本校验即跳过（幂等）
脚本每个阶段先检测已装组件版本（`nvidia-smi` / `go version` / `vllm --version`），满足要求直接跳过——**可安全重跑**，适合失败后修复重来。

### 4. 无 GPU 也能用
驱动阶段检测到无 NVIDIA GPU 时跳过，vLLM 不可用但 MeshServe 仍可安装，用 `fake` 引擎完整体验控制台/管理/对话功能。

## 脚本执行流程

```
0 预检       root 检查 / 架构 / 磁盘 ≥15GB / GPU 检测
1 系统包     apt: curl wget git build-essential python3-venv pciutils …
2 驱动       已装 ≥550 跳过；否则 apt 装 nvidia-driver-550；加载 nvidia 模块
3 Go 1.25    已装 ≥1.22 跳过；否则官方 tarball → /usr/local/go + PATH
4 vLLM       venv + pip install vllm（自动带 CUDA 12 wheel）
5 MeshServe  源码构建（git clone + go build）或 Release 二进制
6 验证       nvidia-smi / go / vllm / meshserve + 使用指引
```

## 版本兼容矩阵（关键参考）

| 驱动 | CUDA | PyTorch | vLLM | 说明 |
|---|---|---|---|---|
| ≥ 550 | 12.4（内置） | ≥ 2.4 | ≥ 0.6 | 当前主流组合 |
| ≥ 535 | 12.2（内置） | 2.3 | 0.5.x | 旧组合，如需可 `pip install "vllm<0.6"` |
| ≥ 525 | 12.0（内置） | 2.1 | 0.4.x | 最旧可用 |

> 驱动升级后建议重启；容器环境（Docker + `--gpus all`）用 `--skip-driver`。

## 常见问题

| 现象 | 处理 |
|---|---|
| `nvidia-smi` 不存在但 GPU 有 | 驱动未装或未加载：`modprobe nvidia` 或重启 |
| `pip install vllm` 报 CUDA 版本错误 | 先确认驱动 ≥550；升级驱动后重跑脚本（幂等） |
| `vllm: command not found` | 需 `source /opt/meshserve/venv/bin/activate` |
| `meshserve: command not found` | 脚本第 5 步失败，查看报错；或手动 `go install github.com/cq0219/meshserve/cmd/meshserve@latest` |
| 国内网络慢 | `GO_MIRROR=https://mirrors.aliyun.com/golang`；pip 用 `-i https://pypi.tuna.tsinghua.edu.cn/simple` |
| 模型权重下载慢 | 用 `hf-mirror.com` 镜像：`export HF_ENDPOINT=https://hf-mirror.com` |

## 后续扩展方向（本期不做）

- systemd 服务化：`meshserve run` 托管为开机自启服务
- 模型权重自动下载：注册时按 HF 名自动拉取（当前需提前准备权重目录）
- Windows/macOS 版安装脚本（`install-ubuntu.sh` 仅覆盖 Ubuntu/Debian 系）
