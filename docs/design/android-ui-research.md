# 联网调研：Android 原生复现 dsh UI 的技术选型

承接 `dsh-ui-observations.md`。来源：Android 官方文档、生产案例（Tailscale
Android）、社区实践。调研时间 2026-10-16。

## 1. Go↔Kotlin 边界：维持手工 JNI，不引入 gomobile bind

Tailscale Android 用 gomobile bind 生成 JNI 绑定，DeepWiki 对其架构的分析
确认这是可行路线。但 gomobile bind 有一个对本项目致命的约束：**所有跨界
调用串行通过一把全局锁**（gomobile limitations 文档明确记载）。我们的
jsbridge 池、session 订阅、turn 驱动都长时持有 Go 侧 goroutine，全局锁会
把并行 turn 拖成串行。

现有手写 JNI（`Native.runTurn` 返回 JSON 字符串、单 worker 线程、
`runOnUiThread` 回传）已实测无 ANR 无泄漏，且没有反向 JNI
（Java→Go 单向调用，结果经回调接口推送），绕开了 gomobile 的锁。

**定案：保持手写 JNI。事件流回传用 Kotlin 侧轮询文件/管道或
`Native.subscribe(sessionId, callback)` 回调接口（callback 在 Java 线程
执行，Go 侧只调 CallVoidMethod，不做 AttachCurrentThread）。**

## 2. 流式 UI：行级订阅 + LazyColumn，与 dsh 同构

- dsh 的做法：每行订阅一个稳定 node key，chunk 只重渲流式中的那行。
- Compose 等价物：`LazyColumn` + 稳定 `key(id)` + 每行 `collectAsState`
  自己的 `StateFlow<MessageRow>`。Compose 的跳过机制（skip）在参数不变时
  不重组合——天然得到行级订阅，无需额外工作。
- 官方性能文档（developer.android.com/develop/ui/compose/performance/
  bestpractices）的要点与本设计一致：稳定 key、延迟状态读取（`derive`
  把读取下推到 lambda）、避免在组合里做昂贵计算。
- 社区案例（chat 屏 10 项性能修复）核心也是：稳定 key、`contentType`、
  item 内部状态上提、避免 `Modifier` 链重建。

**定案：`LazyColumn(contentType = rowKind)` + 每
行 `key(eventId)` + 行内 `collectAsState`。**

## 3. 流式 Markdown：mikepenz/multiplatform-markdown-renderer

- 明确声明 "Built for streaming"（`rememberStreamified` API），支持
  完整 GFM（表格/任务列表/删除线/自动链接/告警块），每个 AST 节点映射到
  可覆盖的 `@Composable`。
- 对比：eagle-render（新，AI 流式专用但生态小）、compose-richtext（作者
  自述 experimental、路线图不明）、jeziellago/compose-markdown（一次性
  渲染，无流式优化）。
- dsh 的增量冻结思想（append-only 流只重解析尾部块）在
  multiplatform-markdown-renderer 的 streaming API 中有对应物；若其冻结
  粒度不足，可在 Kotlin 侧复刻 `UNSTABLE_TAIL_BLOCKS` 逻辑（纯文本行
  扫描，无解析器依赖）。

**定案：multiplatform-markdown-renderer 为主，自研冻结层为兜底。**

## 4. 侧栏：ModalNavigationDrawer，不是三列

dsh 三列（264–420px）是桌面形态；`SIDEBAR_AUTO_COLLAPSE=1024` 本身就承认
窄屏走单列。移动端直接用官方 ModalNavigationDrawer（官方 quick guide），
会话列表进 drawer，主区单列，右栏功能（会话详情/设置）进全屏面板。

**定案：单列 + ModalNavigationDrawer，桌面三列留到 Windows 客户端再做。**

## 5. 构建链：Gradle + AGP，依赖走阿里云镜像

dl.google.com 与 maven.google.com 在本环境不可达（000），阿里云
google 镜像 200。构建脚本需配 `repositories { maven("https://
maven.aliyun.com/repository/google") }`。AGP 8.13 + Kotlin 2.0.21 +
Compose BOM 均实测可达。Gradle 9.7.1 本地已有缓存。

**定案：Gradle 9.7.1 + AGP 8.13 + Compose BOM + 阿里云镜像；构建脚本
固化到 android/（替换手写 javac/d8 链），CI 单独 job 跑 Gradle 构建。**

## 6. 风险登记

| 风险 | 缓解 |
|---|---|
| Compose 编译器与 Kotlin 版本锁死 | Kotlin 2.x 后 compose-compiler 随 Kotlin 发布，用同版本对 |
| 阿里云镜像滞后缺包 | 备用 mirror（腾讯）已在 nodebundle.sh 验证可达 |
| gomobile 不用后 aar 工具链缺失 | 不需要：手写 JNI + c-shared 已在用 |
| 桌面 Windows 客户端 | Compose Multiplatform 可后置复用同一 Kotlin UI 代码 |
