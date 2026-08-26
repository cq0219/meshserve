# 提示词：MeshServe Web 控制台——模型注册与管理 + 模型交互界面

> 用途：将本需求直接下发给开发人员或 AI 编程助手执行。
> 目标仓库：github.com/cq0219/meshserve（Go + Go embed 前端，单二进制）。

## 0. 角色与总目标

你是一名资深全栈工程师。请在 **MeshServe** 项目中，把 Web 控制台从"只读监控"升级为"可操作的管理与交互工作台"：
实现**模型注册与管理功能**，并提供**与已注册模型的聊天式交互界面**。
要求前后端一体化交付（后端 REST API + 前端响应式页面，全部内嵌在同一个二进制中），并在完成后运行测试与构建验证。

**禁止**：引入新的前端构建链（不引入 Node/Vite/React）；沿用现有 `Go embed` 内嵌静态前端方案；不得破坏现有 `/api/status`、`/api/nodes`、`/api/models`、`/api/instances`、`/api/gpu` 端点。

---

## 1. 背景与现有资产（必须复用）

| 现有能力 | 位置 | 说明 |
|---|---|---|
| 模型元数据存储 | `internal/raftstore`（bbolt） | `Model{Name,Path,Engine,Quant,VRAMBytes,ParamsBillions,TensorParallel,PipelineParallel,Replicas}` |
| 模型仓库 | `internal/modelrepo` | 注册/列表/删除/校验和 |
| 推理网关 | `internal/gateway` | OpenAI 兼容：`GET /v1/models`、`POST /v1/chat/completions`（SSE 流式）、负载均衡 |
| 实例管理 | `internal/agent` | 部署/停止/自愈，实例状态 ready/loading/error |
| 控制台 API | `internal/console` | `/api/status` `/api/nodes` `/api/models` `/api/instances` `/api/gpu` |
| 前端 | `internal/console/web/index.html`（单页，原生 JS） | 现有面板：集群总览、节点、模型列表、集群实例、GPU 监控 |

**架构约定**：前端 → console REST API（管理操作）；前端 → gateway `/v1/chat/completions`（推理交互，SSE 流式直连，经 CORS 或同源代理）。

---

## 2. 功能需求（含验收标准）

### 模块一：模型注册（核心）

**功能点：**
1. 「注册模型」入口：控制台顶部导航按钮 + 模型列表页空态引导。
2. 注册表单字段（前端表单 → `POST /api/models`）：
   - 必填：`name`（唯一，小写字母/数字/中划线）、`engine`（vllm / sglang / llamacpp / fake）、`path`（模型权重路径或 HF 模型名）或 `endpoint`（外部 OpenAI 兼容 API 地址，二选一）
   - 选填：`description`、`version`、`quant`（fp16/bf16/int8/int4）、`params`（参数量，自动估算显存）、`vram`（显存需求，字节，覆盖自动估算）、`tp`/`pp`（张量/流水线并行）、`replicas`（副本数）
3. 提交校验：name 唯一性、engine 合法性、path/endpoint 二选一、数值范围（tp≥1、pp≥1、replicas 1–8）。
4. 注册成功后：模型写入存储 → 立即触发部署（engine=fake 直接部署；vllm 探测 8000 就绪后挂载）→ 状态流转：`registered → deploying → online`（或 `error`）。
5. 结果反馈：表单页显示成功/失败 toast，失败展示具体错误原因（含校验错误、部署错误）。

**验收标准：**
- [ ] 表单提交后模型出现在「模型列表」且状态为 online（fake 引擎下 3 秒内）
- [ ] 重名注册被拒绝并提示；path 与 endpoint 同填被拒绝
- [ ] 非法 engine 名被拒绝

### 模块二：模型管理列表（增强）

**功能点：**
1. 列表展示（表格）：名称、引擎、量化、显存需求、副本数、**状态**、操作列。
2. 状态推导（新增 `status` 字段，由 agent 实例状态聚合）：`在线`（≥1 实例 ready）/ `部署中`（实例 loading）/ `离线`（无实例）/ `错误`（实例 error）/ `已停用`（手动停用）。
3. 筛选与搜索：名称关键字搜索框 + 类型（engine）下拉 + 状态下拉，前端即时过滤（数据量小无需后端分页）。
4. 行操作：
   - **编辑**：打开与注册表单同构的编辑表单（预填当前值），`PATCH /api/models/{name}`；引擎/路径变更需重新部署。
   - **停用/启用**：`POST /api/models/{name}/toggle`——停用=停止全部实例并标记；启用=重新部署。
   - **删除**：二次确认弹窗；`DELETE /api/models/{name}`（可选 `?remove_files=1` 同时删权重目录）。
5. 详情视图：点击行展开/进入详情页，展示模型全部元数据 + 关联实例列表（复用 `/api/instances` 过滤）。

**验收标准：**
- [ ] 搜索"qwen"过滤出名称含 qwen 的模型；状态筛选正确
- [ ] 停用后模型状态变为"已停用"且实例停止；启用后恢复在线
- [ ] 删除需二次确认，删除后列表与实例均消失

### 模块三：模型交互界面（核心）

**功能点：**
1. 「聊天工作台」：控制台导航「模型对话」页，布局为左侧模型列表（可搜索）+ 右侧对话区。
2. 对话区：
   - 模型选择下拉（仅列出 `在线` 状态的模型），默认选中第一个
   - 参数面板（可折叠）：`temperature`（0–2，默认 0.7）、`max_tokens`（默认 1024）、`top_p`（默认 1.0）、`stream`（默认开）
   - 消息列表：用户/助手气泡，Markdown 渲染（引入轻量 marked 库或简易渲染器，不引入构建链）
   - 输入框：多行、Enter 发送 / Shift+Enter 换行、发送中禁用并显示"生成中…"
3. 请求与响应：
   - 调用网关 `POST /v1/chat/completions`（SSE 流式），`Authorization: Bearer <empty>` 或网关配置的 key
   - **实时流式渲染**：逐 token 追加显示
   - **统计展示**：每次响应头部显示「响应耗时 Xms · 输出 N tokens」（从 SSE 首包计时，`usage` 取 completion_tokens）
   - **错误处理**：HTTP 4xx/5xx、连接失败、超时（30s）分别展示明确错误条；网络错误提示"模型服务不可达"
4. 对话历史：
   - 会话内上下文：保留当前会话消息数组，随请求发送（≤ 最近 20 条）
   - 历史持久化：localStorage 按模型名分 key 保存最近 N 个会话（每条含时间戳、消息数）；「新建对话」「历史会话」入口，可切换/清空
5. 非模型可用态：所选模型离线时输入框禁用并提示"模型离线，请先在模型管理中启用"。

**验收标准：**
- [ ] fake 引擎模型可完成一轮完整对话，流式逐字渲染
- [ ] 响应后显示耗时与 token 数；错误场景（停用模型后对话）展示明确错误
- [ ] 刷新页面后历史会话可恢复；新建对话清空上下文

### 模块四：技术实现与非功能

**技术约束：**
1. 后端（Go）：console 包新增 handler，遵循现有 `writeJSON` 风格；模型 CRUD 复用 `modelrepo`/`raftstore`；部署触发复用 `agent.DeployInstance` 与 `scheduler.Place`（或直接本地部署）。
2. 前端（原生 JS + CSS）：单页应用，`fetch` 调 API；SSE 用 `fetch + ReadableStream` 解析（不支持 EventSource 的 POST 场景）；响应式布局（现有 CSS Grid 扩展，≥375px 移动端可用）。
3. 颜色/风格：沿用现有主题（主色 `#1a4f8b`、强调 `#d97706`、成功 `#15803d`、背景 `#f5f7fb`）；组件化（卡片、表格、弹窗、toast 复用现有 class）。
4. 安全：删除/停用/注册接口需与现有控制台一致（无鉴权时保持本地信任模型，文档注明生产应置于内网）。

**非功能：**
- 页面加载 < 1s（本地静态资源）；交互接口响应与网关一致；所有 API 错误返回结构化 `{"error": "..."}`
- 控制台 `/api/models` 变更时，其他页面（实例/GPU）数据自动一致（轮询已有 10s/5s，不改动）

**验收标准：**
- [ ] `go build ./...`、`go vet ./...`、`go test ./...` 全绿；console 包新增 handler 均有单测（表单校验、CRUD、toggle 状态流转）
- [ ] 桌面（≥1280px）与移动（375px）布局无横向滚动、操作可达
- [ ] 现有 6 个 API 端点行为不回归（控制台测试全量通过）

---

## 3. API 契约（新增/变更）

| 方法 | 路径 | 说明 | 请求体（节选） | 响应 |
|---|---|---|---|---|
| POST | `/api/models` | 注册模型 | `{name, engine, path, endpoint?, description?, version?, quant?, params?, vram?, tp?, pp?, replicas?}` | 201 Model / 400 校验错误 / 409 重名 |
| PATCH | `/api/models/{name}` | 编辑元数据 | 同 POST（可部分字段） | 200 Model / 404 |
| POST | `/api/models/{name}/toggle` | 停用/启用 | `{}` | 200 Model（status 变化） |
| DELETE | `/api/models/{name}` | 删除 | `?remove_files=1` | 204 / 404 |
| GET | `/api/models?q=&engine=&status=` | 列表（含筛选） | — | 200 Model[] |
| POST | `/v1/chat/completions` | 对话（复用现有网关，SSE） | `{model, messages, temperature?, max_tokens?, top_p?, stream}` | 200 SSE / 错误 JSON |

> `Model` 结构新增 `Status`（枚举：online/deploying/offline/error/disabled）、`Description`、`Version`、`Endpoint`、`Params` 字段——迁移时对旧记录默认 `Status=offline`（或按实例推导）。

## 4. 数据模型（raftstore.Model 扩展）

```go
type Model struct {
    Name        string `json:"name"`
    Path        string `json:"path,omitempty"`
    Endpoint    string `json:"endpoint,omitempty"` // 外部 OpenAI 兼容 API
    Engine      string `json:"engine"`
    Quant       string `json:"quant,omitempty"`
    VRAMBytes   uint64 `json:"vram_bytes,omitempty"`
    Params      float64 `json:"params,omitempty"`   // 参数量（十亿）
    TensorParallel   int `json:"tensor_parallel,omitempty"`
    PipelineParallel int `json:"pipeline_parallel,omitempty"`
    Replicas    int    `json:"replicas"`
    Description string `json:"description,omitempty"`
    Version     string `json:"version,omitempty"`
    Status      string `json:"status"` // online|deploying|offline|error|disabled
}
```

## 5. 里程碑与工作量估算

| 阶段 | 内容 | 预估 |
|---|---|---|
| 一：后端 API | Model 扩展 + CRUD/toggle handler + 状态推导 + 单测 | 5–6h |
| 二：前端管理 | 注册/编辑表单、列表筛选搜索、行操作、详情 | 5–6h |
| 三：交互界面 | 聊天工作台、SSE 流式、参数面板、耗时统计、错误处理 | 6–8h |
| 四：历史与打磨 | 对话历史 localStorage、响应式、空态/加载态、回归测试 | 3–4h |
| **合计** | | **约 20–24h（3–4 个工作日）** |

## 6. 交付物与最终验收清单

- [ ] `internal/console` 新增模型管理 + 交互相关 handler 与测试；`internal/raftstore` Model 扩展（向后兼容迁移）
- [ ] `internal/console/web/` 新增「模型管理」与「模型对话」两个视图，导航可达
- [ ] `go test ./...`、`go vet ./...`、`golangci-lint` 全绿
- [ ] 本地 `meshserve run --engine fake` 全流程演示通过（注册 → 列表在线 → 对话流式 → 停用 → 删除）
- [ ] README 迭代记录追加本特性条目

## 7. 边界与后续扩展（本期不做）

- 多租户权限/登录鉴权
- 模型文件上传（仅支持路径/HF 名/端点引用）
- 多模态（图片输入）对话
- 对话历史云端同步（本期 localStorage）
