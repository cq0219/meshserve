#!/usr/bin/env bash
# MeshServe 一键安装脚本
# 用法: curl -sfL https://meshserve.io/install.sh | sh
#      或 ./install.sh --offline  (离线包模式)
set -euo pipefail

# ============ 基础配置 ============
VERSION="${MESHSERVE_VERSION:-0.1.0}"
BASE_URL="${MESHSERVE_BASE_URL:-https://github.com/yourorg/meshserve/releases/download/v${VERSION}}"
INSTALL_DIR="${MESHSERVE_INSTALL_DIR:-/usr/local/bin}"
DATA_DIR="${MESHSERVE_DATA_DIR:-$HOME/.meshserve}"
OFFLINE=0
SKIP_GPU_CHECK=0

# 解析参数
for arg in "$@"; do
  case "$arg" in
    --offline) OFFLINE=1 ;;
    --skip-gpu-check) SKIP_GPU_CHECK=1 ;;
    --help)
      echo "用法: install.sh [--offline] [--skip-gpu-check]"
      exit 0 ;;
    *) echo "未知参数: $arg" >&2; exit 2 ;;
  esac
done

echo "=============================================="
echo "  MeshServe v${VERSION} 安装程序"
echo "=============================================="

# ============ 1. 环境预检 ============
echo "[1/4] 环境预检…"
detect_os() {
  case "$(uname -s)" in
    Linux)  echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)      echo "unsupported" ;;
  esac
}
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "unsupported" ;;
  esac
}
OS=$(detect_os); ARCH=$(detect_arch)
if [ "$OS" = "unsupported" ] || [ "$ARCH" = "unsupported" ]; then
  echo "✗ 不支持的系统/架构: $OS/$ARCH" >&2; exit 2
fi
echo "  系统: $OS/$ARCH ✓"

# GPU 检测（可跳过）
if [ "$SKIP_GPU_CHECK" = "0" ]; then
  if command -v nvidia-smi >/dev/null 2>&1; then
    echo "  GPU: $(nvidia-smi --query-gpu=name --format=csv,noheader | head -1) ✓"
  else
    echo "  ⚠ 未检测到 NVIDIA GPU（可用 --engine fake 演示模式，或后续安装驱动）"
  fi
fi

# ============ 2. 下载 / 解包 ============
echo "[2/4] 获取二进制…"
BIN_NAME="meshserve-${OS}-${ARCH}"
if [ "$OFFLINE" = "1" ]; then
  # 离线包模式：当前目录应包含二进制
  SRC="./${BIN_NAME}"
  [ -f "$SRC" ] || { echo "✗ 离线包缺少 ${BIN_NAME}" >&2; exit 1; }
else
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  URL="${BASE_URL}/${BIN_NAME}"
  echo "  下载: ${URL}"
  if command -v curl >/dev/null 2>&1; then
    curl -sfL "$URL" -o "$TMP/${BIN_NAME}" || { echo "✗ 下载失败" >&2; exit 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$URL" -O "$TMP/${BIN_NAME}" || { echo "✗ 下载失败" >&2; exit 1; }
  else
    echo "✗ 需要 curl 或 wget" >&2; exit 1
  fi
  SRC="$TMP/${BIN_NAME}"
fi
chmod +x "$SRC"

# ============ 3. 安装 ============
echo "[3/4] 安装到 ${INSTALL_DIR}…"
mkdir -p "$INSTALL_DIR"
install -m 0755 "$SRC" "$INSTALL_DIR/meshserve"
mkdir -p "$DATA_DIR"

# ============ 4. 服务注册 ============
echo "[4/4] 配置 systemd 服务（如可用）…"
if command -v systemctl >/dev/null 2>&1; then
  cat > /tmp/meshserve.service <<EOF
[Unit]
Description=MeshServe LLM Inference Node
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/meshserve run
Restart=on-failure
RestartSec=5
User=${USER}

[Install]
WantedBy=multi-user.target
EOF
  sudo cp /tmp/meshserve.service /etc/systemd/system/meshserve.service
  sudo systemctl daemon-reload
  echo "  systemd 服务已注册（启动: sudo systemctl start meshserve）"
fi

echo ""
echo "=============================================="
echo "  ✅ MeshServe 安装完成！"
echo "  下一步："
echo "    meshserve init                 # 第一台机器初始化集群"
echo "    meshserve join --token <TOKEN> # 其他机器加入"
echo "    meshserve run                  # 启动节点"
echo "=============================================="
