# MeshServe Ubuntu 一键安装指南

目标：**一条命令装完所有依赖并可用**——显卡驱动（50 系 -open 580+）、Python 3.10、Go、vLLM、MeshServe 二进制，覆盖从裸机到 `meshserve run` 的全过程。

## 安装要求（重要）

| 组件 | 要求 | 说明 |
|---|---|---|
| NVIDIA 驱动 | **RTX 50 系列：必须安装带 `-open` 后缀的驱动**，版本 **≥ 580**（如 `nvidia-driver-580-open`）；其他显卡建议 ≥580 | 50 系（Blackwell）的 `-open` 是内核级开源驱动，apt 包名为 `nvidia-driver-580-open` |
| Python | **默认 3.10**（`PY_VERSION` 可覆盖） | vLLM 官方支持 3.10-3.12，脚本默认 3.10 |
| CUDA | **无需安装** | PyTorch/vLLM 的 pip wheel 自带 CUDA runtime，只要求驱动版本达标 |
| Go | ≥ 1.25（匹配 go.mod） | 仅构建 MeshServe 需要 |
| vLLM | 最新 | 直接装入系统 Python（**无 venv 隔离**，`--break-system-packages` 兜底 PEP 668） |

## 使用

```bash
# 拷贝脚本到目标机后
sudo bash install-ubuntu.sh                          # 全量安装（自动检测显卡代数）
sudo bash install-ubuntu.sh --skip-driver            # 跳过驱动（容器/已有驱动/无 GPU）
sudo bash install-ubuntu.sh --no-meshserve           # 只装依赖
sudo bash install-ubuntu.sh --gpu-gen 50             # 强制按 RTX 50 系处理（-open 驱动）
sudo PY_VERSION=3.10 DRIVER_PKG=nvidia-driver-580-open bash install-ubuntu.sh  # 参数覆盖
```

## 可选参数

| 参数（环境变量） | 默认 | 说明 |
|---|---|---|
| `--gpu-gen auto\|50\|other` | auto | 50=强制 `-open` 驱动；auto 自动检测（识别 RTX 50xx / GB20x） |
| `--skip-driver` | — | 跳过驱动安装 |
| `--no-meshserve` | — | 只装依赖，不装 MeshServe |
| `PY_VERSION` | 3.10 | Python 版本（3.10/3.11/3.12） |
| `DRIVER_PKG` | 自动 | 驱动包名，如 `nvidia-driver-580-open`；自动逻辑：50 系 → `<min>-open`，其他 → `<min>` |
| `DRIVER_MIN` | 580 | 驱动最低版本 |
| `GO_VERSION` | 1.25.0 | Go 版本 |
| `MESH_BIN_VERSION` | 空（源码构建） | GitHub release tag，填了则下载预编译二进制 |
| `GO_MIRROR` | go.dev | 国内 `https://mirrors.aliyun.com/golang` |
| `PIP_FLAGS` | `--break-system-packages` | pip 直装系统 Python 的 PEP 668 兜底 |

## 脚本执行流程

```
0 预检        root / 架构 / 磁盘 ≥15GB / GPU 代数检测（50 系 → -open）
1 系统包      apt: curl wget git build-essential pciutils software-properties-common …
2 Python 3.10 已装跳过；否则 apt（22.04 自带 / 24.04 走 deadsnakes PPA）
3 驱动        50 系自动装 nvidia-driver-580-open；已装 ≥580 跳过
4 Go 1.25     官方 tarball → /usr/local/go + PATH
5 vLLM        python3.10 -m pip install vllm（系统级，无 venv）
6 MeshServe   源码构建或 Release 二进制
7 验证        nvidia-smi / python / vllm / meshserve + 使用指引
```

## 版本兼容矩阵

| 显卡 | 驱动 | CUDA | 说明 |
|---|---|---|---|
| RTX 50 系（Blackwell） | **580-open（必须 -open）** | 12.4（wheel 内置） | 脚本自动识别并选择 |
| RTX 40/30 系及更早 | 580（或 550+） | 12.4（wheel 内置） | 580 需较新内核（24.04 HWE/25.04） |
| 无 GPU | 跳过 | — | 用 fake 引擎体验全部功能 |

> 旧系统（22.04）没有 580 包时：`DRIVER_PKG=nvidia-driver-570`（或 570-open）。驱动升级后建议重启。

## 常见问题

| 现象 | 处理 |
|---|---|
| 50 系装了非 -open 驱动黑屏/性能差 | 换装 `nvidia-driver-580-open`：`apt install nvidia-driver-580-open` + 重启 |
| `nvidia-smi` 不存在但 GPU 有 | 驱动未加载：`modprobe nvidia` 或重启 |
| pip 报 externally-managed-environment | 脚本已带 `--break-system-packages`；如被拦截手动加该参数 |
| `vllm: command not found` | 系统 Python 的 bin 不在 PATH：`hash -r` 或 `$(python3.10 -c "import sys;print(sys.executable)")/../bin/vllm` |
| 国内网络慢 | `GO_MIRROR=https://mirrors.aliyun.com/golang`；pip 加 `-i https://pypi.tuna.tsinghua.edu.cn/simple`（PIP_FLAGS 追加） |
| 模型权重下载慢 | `export HF_ENDPOINT=https://hf-mirror.com` |

## 后续扩展方向（本期不做）

- systemd 服务化：`meshserve run` 托管为开机自启服务
- 模型权重自动下载：注册时按 HF 名自动拉取
- Windows/macOS 版安装脚本
