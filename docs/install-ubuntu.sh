#!/usr/bin/env bash
# =============================================================================
# MeshServe Ubuntu 一键安装脚本
# 覆盖：NVIDIA 驱动（50 系列需 -open）/ Go 1.25 / Python 3.10 / vLLM / MeshServe
#
# 用法:
#   sudo bash install-ubuntu.sh                          # 全量（自动检测显卡代数）
#   sudo bash install-ubuntu.sh --skip-driver            # 跳过显卡驱动（已装/容器/无 GPU）
#   sudo bash install-ubuntu.sh --no-meshserve           # 只装依赖，不装 MeshServe
#   sudo bash install-ubuntu.sh --gpu-gen 50             # 强制按 RTX 50 系安装（-open 驱动）
#   sudo PY_VERSION=3.10 DRIVER_PKG=nvidia-driver-580-open bash install-ubuntu.sh  # 参数覆盖
#
# 要求:
#   - RTX 50 系列（Blackwell）必须安装带 -open 后缀的驱动（如 nvidia-driver-580-open）
#   - 驱动版本需 ≥ 580
#   - Python 默认 3.10（可用 PY_VERSION 覆盖）
#   - vLLM 直接装入系统 Python（无 venv 隔离）
#   - CUDA Toolkit 无需单独安装：PyTorch/vLLM 的 pip wheel 自带 CUDA runtime
#   - 本脚本在 Ubuntu 20.04/22.04/24.04 设计；580 系驱动需较新内核（24.04 HWE / 25.04），
#     旧系统可 DRIVER_PKG=nvidia-driver-570 覆盖
# =============================================================================
set -euo pipefail

# ---------- 参数（可环境变量覆盖） ----------
GO_VERSION="${GO_VERSION:-1.25.0}"
PY_VERSION="${PY_VERSION:-3.10}"                    # Python 版本（默认 3.10）
DRIVER_MIN="${DRIVER_MIN:-580}"                     # 驱动最低版本（50 系必须 580+）
DRIVER_PKG="${DRIVER_PKG:-}"                        # 驱动包名，留空自动选择
GPU_GEN="${GPU_GEN:-auto}"                          # auto|50|other：显卡代数
GO_MIRROR="${GO_MIRROR:-https://go.dev/dl}"         # 国内 https://mirrors.aliyun.com/golang
MESH_DIR="/opt/meshserve"
MESH_BIN_VERSION="${MESH_BIN_VERSION:-}"            # 留空 = 源码构建；可填 GitHub release tag
PIP_FLAGS="${PIP_FLAGS:---break-system-packages}"   # 无 venv 直装系统的 PEP668 兜底

SKIP_DRIVER=0; SKIP_MESH=0
for a in "$@"; do
  case "$a" in
    --skip-driver) SKIP_DRIVER=1 ;;
    --no-meshserve) SKIP_MESH=1 ;;
    --gpu-gen) GPU_GEN="${2:?--gpu-gen 需要参数 auto|50|other}"; shift ;;
    --gpu-gen=*) GPU_GEN="${a#*=}" ;;
  esac
done

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
fail() { printf '\033[1;31m[错误]\033[0m %s\n' "$*"; exit 1; }

# ---------- 0. 预检 ----------
log "预检环境…"
[ "$(id -u)" -eq 0 ] || fail "请用 sudo 运行（需要安装系统包）"
ARCH="$(uname -m)"
[ "$ARCH" = "x86_64" ] || [ "$ARCH" = "aarch64" ] || fail "不支持的架构: $ARCH"
FREE_GB=$(df /opt --output=avail -B1G 2>/dev/null | tail -1 | tr -d ' ')
[ "${FREE_GB:-0}" -ge 15 ] || fail "磁盘剩余不足 15GB（当前约 ${FREE_GB}GB）"

# 检测 GPU 代数（50 系 = Blackwell）
HAS_GPU=0; IS_50=0
if lspci 2>/dev/null | grep -qi "nvidia"; then
  HAS_GPU=1
  if [ "$GPU_GEN" = "50" ] || lspci | grep -iE "nvidia.*(RTX 5[0-9]{2,3}|GB20[0-9])" >/dev/null 2>&1; then
    IS_50=1
  fi
fi
[ "$GPU_GEN" = "other" ] && IS_50=0

# 驱动包自动选择：50 系必须 -open
if [ -z "$DRIVER_PKG" ]; then
  if [ "$IS_50" -eq 1 ]; then
    DRIVER_PKG="nvidia-driver-$DRIVER_MIN-open"     # 如 nvidia-driver-580-open
  else
    DRIVER_PKG="nvidia-driver-$DRIVER_MIN"          # 如 nvidia-driver-580
  fi
fi
log "检测结果: GPU=$([ $HAS_GPU -eq 1 ] && echo "有($([ $IS_50 -eq 1 ] && echo 'RTX 50系→需 -open 驱动') || echo '其他')" || echo 无) | 驱动包=$DRIVER_PKG"

# ---------- 1. 系统基础包 ----------
log "安装系统基础包…"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl wget git build-essential pciutils ca-certificates software-properties-common

# ---------- 2. Python ${PY_VERSION}（无 venv，直接系统级） ----------
PY=python${PY_VERSION}
if command -v "$PY" >/dev/null 2>&1; then
  log "已装 Python $PY_VERSION，跳过"
else
  log "安装 Python $PY_VERSION…"
  # Ubuntu 22.04 自带 python3.10；24.04 需 deadsnakes PPA
  if ! apt-get install -y -qq "$PY" "$PY-pip" "$PY-venv" 2>/dev/null; then
    add-apt-repository -y ppa:deadsnakes/ppa >/dev/null 2>&1 || true
    apt-get update -qq
    apt-get install -y -qq "$PY" "$PY-pip" "$PY-venv"
  fi
fi
log "Python: $("$PY" --version)"

# ---------- 3. NVIDIA 驱动 ----------
if [ "$SKIP_DRIVER" -eq 1 ]; then
  log "跳过显卡驱动安装（--skip-driver）"
elif [ "$HAS_GPU" -eq 0 ]; then
  log "未检测到 NVIDIA GPU，跳过驱动（无 GPU 时 vLLM 不可用，可先用 fake 引擎体验）"
else
  if command -v nvidia-smi >/dev/null 2>&1; then
    DRV=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -1 | cut -d. -f1)
    if [ "${DRV:-0}" -ge "$DRIVER_MIN" ]; then
      log "已装驱动 $DRV（≥ $DRIVER_MIN），跳过"
    else
      log "驱动 $DRV 过旧（< $DRIVER_MIN），安装 $DRIVER_PKG…"
      apt-get install -y -qq "$DRIVER_PKG"
    fi
  else
    log "安装 NVIDIA 驱动 $DRIVER_PKG（约 5-10 分钟）…"
    apt-get install -y -qq "$DRIVER_PKG"
  fi
  if ! nvidia-smi >/dev/null 2>&1; then
    log "驱动已安装但未加载，尝试 modprobe（若失败请重启后重跑）…"
    modprobe nvidia 2>/dev/null || true
  fi
fi

# ---------- 4. Go 1.25 ----------
if command -v go >/dev/null 2>&1 && [ "$(go version | grep -oE 'go[0-9.]+' | tr -d go | cut -d. -f1)" -ge 1 ] \
   && [ "$(go version | grep -oE 'go[0-9.]+' | tr -d go | cut -d. -f2)" -ge 22 ]; then
  log "已装 $(go version)，跳过 Go 安装"
else
  log "安装 Go $GO_VERSION…"
  case "$ARCH" in
    x86_64)  GOTGZ="go$GO_VERSION.linux-amd64.tar.gz" ;;
    aarch64) GOTGZ="go$GO_VERSION.linux-arm64.tar.gz" ;;
  esac
  curl -fsSL -o /tmp/$GOTGZ "$GO_MIRROR/$GOTGZ"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/$GOTGZ
  rm -f /tmp/$GOTGZ
  echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
  export PATH=$PATH:/usr/local/go/bin
fi

# ---------- 5. vLLM（系统 Python 直装，无 venv） ----------
log "安装 vLLM 到系统 Python $PY_VERSION（约 5-8GB，5-15 分钟）…"
"$PY" -m pip install -q -U pip
"$PY" -m pip install -q $PIP_FLAGS vllm
VLLM_BIN="$(command -v vllm || echo "$("$PY" -c 'import os,sys;print(os.path.dirname(sys.executable))')/vllm")"
log "vLLM: $("$VLLM_BIN" --version)"

# ---------- 6. MeshServe ----------
if [ "$SKIP_MESH" -eq 1 ]; then
  log "跳过 MeshServe 安装（--no-meshserve）"
elif [ -n "$MESH_BIN_VERSION" ]; then
  log "下载 MeshServe release $MESH_BIN_VERSION…"
  curl -fsSL -o /usr/local/bin/meshserve \
    "https://github.com/cq0219/meshserve/releases/download/$MESH_BIN_VERSION/meshserve-linux-$ARCH"
  chmod +x /usr/local/bin/meshserve
else
  log "源码构建 MeshServe（需 Go；约 1-2 分钟）…"
  SRC="$MESH_DIR/src"
  [ -d "$SRC" ] || git clone --depth 1 https://github.com/cq0219/meshserve.git "$SRC"
  cd "$SRC"
  CGO_ENABLED=0 go build -o /usr/local/bin/meshserve ./cmd/meshserve
fi

# ---------- 7. 验证 ----------
log "=== 安装完成，验证 ==="
echo "驱动:  $(nvidia-smi --query-gpu=name,driver_version --format=csv,noheader 2>/dev/null | head -1 || echo '无 GPU/驱动未加载')"
echo "Python: $("$PY" --version)"
echo "vLLM:  $("$VLLM_BIN" --version 2>/dev/null || echo 未找到)"
echo "Mesh:  $(meshserve version 2>/dev/null | head -1 || echo 'meshserve 未安装')"

cat << 'DONE'

接下来（使用 vLLM 推理）:
  meshserve init --name prod                    # 第一台机器初始化
  meshserve model register qwen3-8b --path /data/models/Qwen3-8B --engine vllm --params 8
  meshserve run                                 # 自动拉起 vLLM 进程并挂载
  控制台: http://<本机IP>:8443
DONE
