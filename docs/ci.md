# CI/CD 说明

MeshServe 使用 **GitHub Actions** 作为 CI 平台（配置文件：`.github/workflows/ci.yml`）。

## 流水线结构

| Job | 触发时机 | 内容 |
|-----|---------|------|
| `lint` | push / PR | gofmt 检查、go vet、golangci-lint、govulncheck 漏洞扫描 |
| `unit` | push / PR | 单元测试（race + 覆盖率），覆盖率 < 50% 失败，产出 coverage 报告 |
| `build` | push / PR（依赖 lint+unit） | 三平台交叉编译矩阵（linux/amd64、linux/arm64、windows/amd64） |
| `e2e` | push / PR（依赖 build） | 双节点真实进程组网集成测试（fake 引擎） |
| `release` | 打 tag `v*`（依赖 e2e） | `make build-all` 构建发布资产 + SHA256 校验和 + 创建 GitHub Release |

```
push/PR ──► lint ──► unit ──► build ──► e2e
tag v*  ──► (以上全绿) ──► release
```

## 使用方式

### 推送到 GitHub

```bash
git remote add origin git@github.com:<org>/meshserve.git
git push -u origin main
```

### 手动触发

仓库 Actions 页面 → 选择 **CI** → Run workflow。

### 发布新版本

```bash
git tag v0.1.0
git push origin v0.1.0
```

流水线自动构建 `bin/release/meshserve-{os}-{arch}` 并发布为 GitHub Release 资产（含 SHA256SUMS 校验和，供 `deploy/install.sh` 下载校验）。

## 本地等价验证（提交前）

```bash
make fmt        # gofmt 格式化
make vet        # go vet
make lint       # golangci-lint（需本地安装）
make test       # 单测 + race + 覆盖率
make build-all  # 交叉编译
make test-integration  # 双节点集成测试
```

## 说明

- 依赖走 `goproxy.cn` 镜像（国内网络友好），无需修改即可在 GitHub Runner 使用；
- `release` job 需要仓库有 `Actions` 写权限（默认开启）；
- 若使用 GitLab CI，可按 `.github/workflows/ci.yml` 的 job 结构平移为 `.gitlab-ci.yml`（阶段：lint → test → build → e2e → release）。
