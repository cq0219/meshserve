# MeshServe Makefile
# 对应落地实现方案 8.2 Makefile 目标

BINARY      := meshserve
BIN_DIR     := bin
GO          := go
GOPROXY     := https://goproxy.cn,direct
LDFLAGS     := -s -w \
  -X github.com/yourorg/meshserve/internal/version.Version=$(VERSION) \
  -X github.com/yourorg/meshserve/internal/version.GitCommit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) \
  -X github.com/yourorg/meshserve/internal/version.BuildTime=$(shell date +%Y-%m-%dT%H:%M:%S)
VERSION     ?= 0.1.0

.PHONY: all build build-all test lint vet fmt proto dev-up e2e install release docs clean

all: build

# 编译当前平台二进制
build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/meshserve
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-agent ./cmd/meshserve-agent
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-gateway ./cmd/meshserve-gateway
	@echo "✅ 构建完成: $(BIN_DIR)/"

# 交叉编译（linux-amd64/arm64 + windows）
build-all:
	@mkdir -p $(BIN_DIR)/release
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/release/$(BINARY)-linux-amd64 ./cmd/meshserve
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/release/$(BINARY)-linux-arm64 ./cmd/meshserve
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/release/$(BINARY)-windows-amd64.exe ./cmd/meshserve
	@echo "✅ 交叉编译完成: $(BIN_DIR)/release/"

# 单元测试（含竞态检测与覆盖率）
test:
	$(GO) test -race -count=1 -cover ./...

# 集成测试（fake engine 双节点模拟）
test-integration:
	$(GO) test -tags=integration -count=1 ./test/...

# 静态检查
lint:
	@command -v golangci-lint >/dev/null 2>&1 || (echo "⚠️ 请先安装 golangci-lint"; exit 1)
	golangci-lint run ./...

vet:
	$(GO) vet ./...

# 格式化
fmt:
	$(GO) fmt ./...
	goimports -w .

# 开发环境（双节点 docker-compose）
dev-up:
	docker compose -f deploy/docker-compose.yml up -d --build

dev-down:
	docker compose -f deploy/docker-compose.yml down

# 端到端测试
e2e:
	$(GO) test -tags=e2e -count=1 ./test/e2e/...

# 本地安装
install: build
	install -m 0755 $(BIN_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

# 清理
clean:
	rm -rf $(BIN_DIR) dist
