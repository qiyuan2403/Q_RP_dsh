# DSH Go 重写工程状态

> `[x]` 表示已有决策或代码证据；`[ ]` 表示未完成。完整体 = DSH 基座按上游行为重写完成 + `Q_RP_DSH_plug-in` 中全部 Go 插件按 DSH 插件机制内置并随应用分发。

## 范围

- [x] 项目是 DeepSeek Harness（DSH）的 Go 重写；DSH 是基座行为、事件、配置、插件、会话和交互的规范来源。
- [x] 基座以 Go 重写；平台原生代码只承担必要 UI 和系统 API 适配，保持 DSH 可观察行为。
- [x] Android 优先；Windows 后续复用 Go 基座和插件实现，并添加必要平台适配。
- [x] Android UI 使用 Compose/Kotlin，Go 内核通过手写 JNI 接入；主 UI 不运行 JS、React、DOM 或浏览器。见 [ADR 0001](decisions/0001-android-ui-native-compose.md)。
- [x] DSH 官方 UI 是界面规格与行为参考，不作为运行时代码依赖。
- [x] 安装包体积上限为 500 MB；低于上限不主动压缩。性能测试按用户安排后置。

## 原则落实

### 简约优先

- [x] DSH 基座先按上游行为完整重写，再将 `Q_RP_DSH_plug-in` 中全部现有 Go 插件内置。
- [x] 内置只表示随应用构建和分发；插件仍通过 DSH 插件机制加载，保留其配置、作用域和生命周期。
- [x] 插件属于基座之上的扩展，不把插件能力并入或改写 DSH 基座。
- [x] 对照固定 DSH 版本列出基座支持范围、插件映射及缺口；差异表见 [DSH_COMPATIBILITY_MATRIX.md](DSH_COMPATIBILITY_MATRIX.md)，插件映射见 [PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。当前上游基线为 `@deepseek-ai/dsh-root 0.2.1-alpha.4`，提交 `86a402200d95fecef4529d969836f1fe13c6cb0d`。

### 经济优先

- [x] Go 统一内核、Agent、会话、工具和运行时实现。
- [x] 平台原生语言仅承担必要 UI、系统 API 和薄适配。
- [x] 优先使用成熟标准库与上游格式，不复制 JavaScript 运行机制。
- [ ] 完成 Android 可安装、可启动、可完成 DSH 对话的纵向切片。

### 可靠优先

- [x] Session 使用追加式 JSONL，具备序号、格式版本、未知必需事件拒绝和实时订阅。
- [x] 工具参数调用前校验。
- [x] session v4 当前合同边界见 [ADR 0002](decisions/0002-session-event-contract.md)。
- [ ] 逐事件对照 DSH 官方 session v4 写入、读取和恢复行为。
- [ ] 完成中断 turn、取消、尾部损坏和重启恢复的 DSH 对齐实现。

### 持续优先

- [x] DSH 官方插件生命周期、profile/bundle 和服务语义是插件边界。
- [x] 需要的 Go 插件目标是内置分发，同时仍通过 DSH 插件机制装载。
- [x] 对照 DSH 官方插件清单，完成基座模块与插件映射/缺口表；见 [PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。
- [ ] 证明每个内置插件都对应已确认的功能需求，并对齐 DSH 插件接口。

### 标准优先

- [x] 以 DSH 官方源码和行为为第一规范来源。
- [x] Cordis/Loader 保留可观察语义，不机械复制 Proxy、原型链等 JS 实现机制。
- [x] 使用 DSH 既有 Session、配置、工具和插件协议。
- [x] 建立上游版本/提交基线：本地官方 checkout 固定为 `0.2.1-alpha.4` / `86a402200d95fecef4529d969836f1fe13c6cb0d`。
- [ ] 建立官方样本对照流程。

## 仓库职责

### `Y_Cordis`

- [x] 重写 DSH 所依赖的 Cordis、Loader 与 include 语义。
- [x] 以行为合同为目标，使用 Go 原生结构实现。
- [ ] 完成对固定 DSH 上游版本的持续差异验证。

### `Q_RP_dsh`

- [x] 承载 DSH 宿主、Agent、模型调用、工具调度、会话和运行时组合的 Go 实现。
- [x] Android 使用 Compose/Kotlin UI + Go/JNI，见 [ADR 0001](decisions/0001-android-ui-native-compose.md)。
- [x] 已有 Agent、Session、Runtime、Tools、Workspace 和 JS 兼容侧车代码。
- [ ] 按 DSH 行为逐项完成 Go 宿主实现，不以“存在同名包”作为兼容完成证明。
- [ ] 完成 Android Activity、JNI、Gradle、Go native library 的真实端到端验收。
- [ ] 完成 Windows 平台适配及同一 DSH 行为合同验证。

### `Q_RP_DSH_plug-in`

- [x] 承载完整体所需的 Go 插件实现：card、cardgit、gallery、hubsync、imagegen、websource。
- [ ] 将全部插件接入 Q_RP_dsh 构建，由 DSH 插件机制注册、配置和挂载；当前主模块尚未依赖此仓库。
- [ ] 按 DSH 插件语义验证全部插件的生命周期、服务可见性和行为。

## 已冻结合同

- [x] Android UI 与 Go 内核边界：Compose/Kotlin + JNI，见 [ADR 0001](decisions/0001-android-ui-native-compose.md)。
- [x] Session v4 存储实现的现状与约束，见 [ADR 0002](decisions/0002-session-event-contract.md)；其与完整 DSH 行为的一致性仍需逐项验证。
- [ ] Go-Kotlin 事件传输与线程调度合同。
- [ ] DSH Agent 流式调用及 UI 增量更新的语义映射；Go 已支持 OpenAI-compatible SSE provider 和增量 tool-call 拼装，但 Agent 当前在最终 assistant 消息提交前只收集增量，尚未发布官方 `assistant-stream` 临时帧。
- [x] Web seam 合同：provider 注册与执行期选择、5 个上游错误码、`capSources` 截断，见 `web/` 包；已知取舍记在 [PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。
- [x] `tool/result` 持久化模型可见文本，恢复优先使用文本；见 [ADR 0002](decisions/0002-session-event-contract.md)。
- [ ] DSH 插件注册、依赖和权限语义映射。
- [ ] Android 首发支持的 DSH 行为清单。

## 发布链状态

2026-10-17 核实并处理：

- [x] 本地 `replace` 已移除：`go.mod` 与 `android/jni/go.mod` 改为 `Y_Cordis v0.0.3`，不再指向 `/root/repos/Y_Cordis`，提交 `ef01001` 已推送。
- [x] `Y_Cordis` 发布 `v0.0.3`（标签指向 `209eed2`），包含主仓库使用的 `jsbridge.Config.RequireRoot`。
- [x] 干净构建验证：从 GitHub 全新 clone、清空 `GOMODCACHE`、`GOPROXY=direct`，`go build ./...`、`go test ./...`、`android/jni` 的 `go build ./...` 均通过。
- [ ] 插件仓库无版本标签（HEAD `dab356a`），主仓库尚未依赖它；插件接入前需要一个可拉取的模块版本。
- [ ] Go 模块代理在当前网络不可达（`proxy.golang.org`、`goproxy.cn` 均无响应），干净构建需 `GOPROXY=direct` 或配置可用代理。
- [ ] 提交后重做干净 clone 验证：新增 `golang.org/x/text` 依赖，需确认 `go.sum` 在空模块缓存下可解析。

## 阶段

### 阶段 0：上游合同与差异基线

- [x] 确认目标为 DSH Go 重写，不另立 Agent 产品逻辑。
- [x] 确认 Go 主实现、Android 优先、Windows 后续。
- [x] 确认 Android UI 原生实现，不运行浏览器主渲染链。
- [x] 冻结 session v4 当前实现决策。
- [x] 固定 DSH 上游版本并记录提交：`0.2.1-alpha.4` / `86a402200d95fecef4529d969836f1fe13c6cb0d`。
- [x] 建立模块、事件、配置和插件差异表：见 [DSH_COMPATIBILITY_MATRIX.md](DSH_COMPATIBILITY_MATRIX.md)。

### 阶段 1：内核与运行时对齐

- [x] Cordis/Loader、Agent、Session、Tools 已有 Go 实现基础。
- [ ] 用 DSH 官方样本验证 profile/bundle 装载结果。
- [x] 验证 Go Agent 的基础流式响应路径：SSE 文本增量、分段 tool-call 参数拼装和既有工具回合继续。
- [x] 移植 DSH web 能力层：`web` seam、`tool-web` 工具、`web-fetch-http` 与 `web-search-deepseek` provider、官方行名到 Go 插件体的映射；见 [PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。测试覆盖选择规则、SSRF 策略、重定向、大小上限、响应映射与插件行生命周期；未接真实 API、未上真机。
- [x] 工具模型可见输出与会话恢复一致：`tool/result` 同时保存结构化 `output` 与模型可见 `text`，恢复优先用 `text`；旧日志回退到 `tools.Tool.Render` 投影。`Render` 的契约（旧日志需接受 JSON 解码形状）已写入 `tools.Tool` 文档。
- [x] web fetch 解码与重定向对齐：`htmlindex` 支持 GBK/GB18030/Big5/Shift_JIS/EUC/UTF-16/windows-125x；`maxRedirects: 0` 表达不跟随重定向；新增 `golang.org/x/text v0.41.0` 依赖（仅 `web/httpfetch` 使用）。
- [ ] 验证 DSH Agent 的官方 assistant-stream 临时帧、错误/取消结算和 UI 增量更新语义。
- [ ] 验证 DSH Session 读写、恢复和投影行为。
- [ ] 验证无 Node 时的 DSH 核心功能边界。

### 阶段 2：内置插件

- [x] `Q_RP_DSH_plug-in` 已有六个 Go 包：card、cardgit、gallery、hubsync、imagegen、websource。
- [x] 目标是把全部六个现有 Go 插件纳入应用完整体。
- [x] 确认插件与 DSH 插件/服务的逐项映射和依赖：六个插件均为库、无插件入口；`websource` 对应上游 provider 层而非工具层。见 [PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。
- [ ] 接入主构建并通过 DSH 插件机制注册、配置和挂载。
- [ ] 对照 DSH 语义验证加载、配置、作用域和卸载。
- [ ] 验证 npm 插件侧车兼容行为。

### 阶段 3：Android 客户端

- [x] 已接受 Compose/Kotlin + Go/JNI 架构决策。
- [ ] 完成真实 Activity 启动和 Go library 加载。
- [ ] 按 DSH 用户流程完成会话列表、创建、打开、发送和工具交互。
- [x] JNI 宿主在多轮会话中恢复模型侧历史：用日志中 `tool/result.text` + `agent.RebuildHistory` + `a.Seed`；含日志、web 工具与恢复历史的集成测试。
- [x] Android 设备产物用最新源码重建：`android/jni/build.sh` 交叉编译 arm64 `libqrpdsh.so`（约 12.5 MB），`android/build.sh` 打包 APK（约 72.8 MiB），`apksigner verify` 通过，包内含 libqrpdsh/libnodebin/Node 资源/manifest；未上真机。
- [ ] 验证原生 UI 消费 DSH 会话事件并正确恢复投影（服务端恢复已通，设备 UI 未验证）。
- [ ] 完成中文输入、Markdown、滚动和文件访问。

### 阶段 4：Windows 客户端

- [ ] 复用 Go 内核、Agent、Session、Tools 和 DSH 兼容合同。
- [ ] 增加 Windows 原生 UI 与平台适配。
- [ ] 验证 Windows 行为与 Android/DSH 合同一致。

## 下一步

- [x] 记录 session v4 当前事件存储合同。
- [x] 明确完整体由 DSH 基座和内置插件组成，插件仍使用 DSH 插件机制。
- [x] 固定上游 DSH 版本与行为基线：`0.2.1-alpha.4` / `86a402200d95fecef4529d969836f1fe13c6cb0d`。
- [ ] 冻结 JNI 传输合同。
- [x] 完成 DSH 基座差异清单与插件映射，见 [DSH_COMPATIBILITY_MATRIX.md](DSH_COMPATIBILITY_MATRIX.md)、[PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。
- [x] 内核登记 `web` 服务名，实现 Go 版 `web` 服务与 `tool-web` 等价工具，补插件注册表。
- [ ] 把五个产品插件（card、cardgit、gallery、hubsync、imagegen）按 DSH 插件形状包装并接入 `plugins/builtin`，逐个验证配置、依赖与卸载。
- [ ] 真机验证：安装新 APK、冷启动、会话列表、多轮对话恢复、web 工具联网与中文页面解码；`adb` 当前无设备。
- [ ] 接真实 DeepSeek 搜索 API 验证 `web_search`（需要 `DEEPSEEK_API_KEY`）。
