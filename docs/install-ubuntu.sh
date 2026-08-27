#!/usr/bin/env bash
# =============================================================================
# MeshServe Ubuntu 一键安装脚本
# 覆盖：NVIDIA 驱动 / Go 1.25 / Python venv / vLLM / MeshServe 二进制
#
# 用法:
#   sudo bash install-ubuntu.sh                 # 全量安装
#   sudo bash install-ubuntu.sh --skip-driver   # 跳过显卡驱动（已装或容器场景）
#   sudo bash install-ubuntu.sh --no-meshserve  # 只装依赖，不装 MeshServe
#
# 说明:
#   - CUDA Toolkit 无需单独安装：PyTorch/vLLM 的 pip wheel 自带 CUDA runtime，
#     仅要求系统 NVIDIA 驱动版本 ≥ 550（支持 CUDA 12.4）
#   - 依赖装到 /opt/meshserve/venv，不污染系统 Python
#   - 本脚本在 Ubuntu 20.04/22.04/24.04 设计，未在真实环境逐版本验证，如有报错请按
#     输出中的步骤号定位
# =============================================================================
set -euo pipefail

# ---------- 配置 ----------
GO_VERSION="${GO_VERSION:-1.25.0}"          # 匹配 go.mod（>= 1.25）
DRIVER_MIN="${DRIVER_MIN:-550}"             # vLLM 需要驱动 >= 550（CUDA 12.4）
GO_MIRROR="${GO_MIRROR:-https://go.dev/dl}" # 国内可用 https://mirrors.aliyun.com/golang
MESH_DIR="/opt/meshserve"
VENV="$MESH_DIR/venv"
MESH_BIN_VERSION="${MESH_BIN_VERSION:-}"    # 留空 = 最新源码构建；可填 GitHub release tag

SKIP_DRIVER=0
SKIP_MESH=0
for a in "$@"; do
  case "$a" in
    --skip-driver) SKIP_DRIVER=1 ;;
    --no-meshserve) SKIP_MESH=1 ;;
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
[ "${FREE_GB:-0}" -ge 15 ] || fail "磁盘剩余不足 15GB（当前约 ${FREE_GB}GB，vLLM 安装约需 8GB + 模型权重另计）"

HAS_GPU=0
if lspci 2>/dev/null | grep -qi "nvidia"; then HAS_GPU=1; fi

# ---------- 1. 系统基础包 ----------
log "安装系统基础包…"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl wget git build-essential pciutils \
  python3 python3-venv python3-pip ca-certificates

# ---------- 2. NVIDIA 驱动 ----------
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
      log "驱动 $DRV 过旧（< $DRIVER_MIN），升级中…"
      apt-get install -y -qq "nvidia-driver-$DRIVER_MIN"
    fi
  else
    log "安装 NVIDIA 驱动（nvidia-driver-$DRIVER_MIN，约 5-10 分钟）…"
    apt-get install -y -qq "nvidia-driver-$DRIVER_MIN"
  fi
  if ! nvidia-smi >/dev/null 2>&1; then
    log "驱动已安装但未加载，尝试加载内核模块（若失败请重启后重跑）…"
    modprobe nvidia 2>/dev/null || true
  fi
fi

# ---------- 3. Go 1.25 ----------
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
log "Go: $(go version 2>/dev/null || echo 重新登录后生效)"

# ---------- 4. Python venv + vLLM ----------
log "创建 Python 虚拟环境并安装 vLLM（约 5-8GB，5-15 分钟）…"
mkdir -p "$MESH_DIR"
[ -d "$VENV" ] || python3 -m venv "$VENV"
"$VENV/bin/pip" install -q -U pip
# vllm 自动拉取匹配 CUDA 12.x 的 pytorch wheel（无需单独装 CUDA Toolkit）
"$VENV/bin/pip" install -q vllm
log "vLLM: $("$VENV/bin/vllm" --version)"

# ---------- 5. MeshServe ----------
if [ "$SKIP_MESH" -eq 1 ]; then
  log "跳过 MeshServe 安装（--no-meshserve）"
elif [ -n "$MESH_BIN_VERSION" ]; then
  log "下载 MeshServe release $MESH_BIN_VERSION…"
  # 预编译二进制（GitHub Releases 产物）；按实际 release 资产名调整
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
log "MeshServe: $(meshserve version 2>/dev/null | head -1)"

# ---------- 6. 验证 ----------
log "=== 安装完成，验证 ==="
echo "驱动:  $(nvidia-smi --query-gpu=name,driver_version --format=csv,noheader 2>/dev/null | head -1 || echo '无 GPU/驱动未加载')"
echo "Go:    $(go version 2>/dev/null || echo '重新登录后生效')"
echo "vLLM:  $("$VENV/bin/vllm" --version)"
echo "Mesh:  $(meshserve version 2>/dev/null | head -1 || echo 'meshserve 未安装')"

cat << 'DONE'

接下来（使用 vLLM 推理）:
  source /opt/meshserve/venv/bin/activate      # 激活 venv（vllm 命令在此环境）
  meshserve init --name prod                    # 第一台机器初始化
  meshserve model register qwen3-8b --path /data/models/Qwen3-8B --engine vllm --params 8
  meshserve run                                 # 自动拉起 vLLM 进程并挂载
  控制台: http://<本机IP>:8443
DONE
