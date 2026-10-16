# UI 设计观察（dsh 客户端解剖）

来源：官方仓库 `packages/client/` 源码逐文件阅读，非猜测。

## 1. 分层架构（可移植的核心思想）

```
host（权威状态）
  └─ connection（RPC/事件流）
      └─ client model（框架无关快照 + store）
          └─ slots（插槽注册：'root'|'sidebar'|'main'|'rightbar'|'shell.bottom'|'shell.overlay'）
              └─ ui-*（55 个 React 插件包）
```

关键设计事实：

- **client model 是纯数据投影**：`packages/client/store` 只有 393 行，
  `ObservableSnapshot<T> { getSnapshot, subscribe }` + `StoreSpec<T,A> { init,
  persist, actions }`，actions 是纯 immer 草稿变换，是 store 的**完整写集**。
  组件只能通过 actions 表写入——写集即审计面。
- **行级订阅**：ChatView 注释明示"每行订阅一个稳定 node key"。流式 chunk
  不会重渲整个列表，只重渲正在流式输出的那一行。
- **增量流式解析**：`ui-primitives/src/markdown/incremental.ts`（360 行）。
  追加式流的 CommonMark 块解析是行基的，追加只能重塑解析前沿——最后
  `UNSTABLE_TAIL_BLOCKS` 个块之外全部冻结，只重解析尾部。整体重解析是
  O(n²)，这个设计把每个源区域限制为"有界次数"解析。
- **插槽而非页面**：`ui-renderer` 按 slot 名渲染，`main` 槽的 entryKey 决定
  会话或全局面板；插件（如 whale-widget）通过 webserver 注入行进入页面。

## 2. 布局（ui-layout）

- 三列 AppFrame：`sidebar | center | rightbar`，网格轨道由 `columns.ts`
  纯函数求解：
  - `SIDEBAR_MIN=264 / MAX=420 / DEFAULT=280 / COLLAPSED=56（图标栏）`
  - `SIDEBAR_AUTO_COLLAPSE=1024`（窄视口自动收成图标栏）
  - `CENTER_MIN=400 / RIGHTBAR_MIN=300 / RIGHTBAR_MAX_RATIO=0.7`
  - 收缩次序：右列先缩、再丢轨道，center 低于 400 前不变；sidebar 不让步
  - 右列是"轨道不是盒子"：occupant 贴 frame 右缘画自己的面板，可悬于中列上
- 拖拽手柄：pointer capture + rAF 节流，拖拽直接写宽度不做缓动。

## 3. Token 体系（ui-theme）

- **190 个唯一 token**，两层：`--dsw-static-*`（调色板，light/dark 两套通过
  `body[data-ds-dark-theme]` 切换）与 `--dsw-alias-*`（语义别名：
  `bg-base/layer-1..3/mask-1..3`、`border-l1..l4`、`button-primary-fill/
  ghost-active-*`、`code-diff-added/deleted`、`file-diff-*-bg/gutter/marker`）。
- 品牌色 `--dsw-alias-brand-primary` = deepseek-500 `rgb(65,118,230)`；
  焦点环 light `#4176E6` / dark `#7AAAFF`（deepseek-400）。
- 字号三项用户可调：text / code / terminal（theme-settings.ts）。

## 4. 组件库（ui-primitives，47 个组件）

Button（variant: primary/ghost/outline/toolbar；size: md=36px 高 12px 圆角 /
sm=28px 高 8px 圆角；leading 16px 图标槽）、Checkbox、CodeCard、CommandText、
ConfigField、ConnectionIndicator、DiffBlock、DisclosureRow、HoverCard、
ImageLightbox、MarkdownText（含流式解析）、用户文本 user-text 等。
CSS Modules 每组件一个文件。

## 5. 移植到 Android 原生的对应关系

| dsh（web） | Android 原生（Go 内核 + Kotlin 壳） |
|---|---|
| ObservableSnapshot + useSyncExternalStore | `StateFlow<T>` + `collectAsState`（行级订阅天然成立） |
| StoreSpec actions 表 | sealed class UiEvent + 单 reducer，写集同审计面 |
| `--dsw-*` CSS token | Kotlin object `Dsw { val bgBase = Color(0xFFFFFFFF) … }`，dark 用 `isSystemInDarkTheme()` 分支；190 个 token 按 UI 用到的子集移植 |
| AppFrame 三列 | `Row(Modifier.widthIn(min=264.dp, max=420.dp))` 侧栏 + `Column` 主区；窄屏（<1024dp 等价）走 ModalNavigationDrawer 图标栏 |
| incremental markdown | Compose 不适用 DOM 流式；用 `Markdown` 库按块冻结重解析（同样的 UNSTABLE_TAIL 思路）或直接按行 append AnnotatedString |
| MarkdownText | `MarkdownText` 组件（JetBrains markdown-renderer 或自封装 commonmark） |
| DiffBlock | 两列 gutter 自绘或复用 diff utils |
| 行级订阅 | `LazyColumn` + 每行订阅自己的 Flow——与 dsh 行级订阅同构 |

## 6. 不移植的

- 55 个 ui-* 包里的桌面向功能（dockkit、browser 侧栏、多窗口布局）：
  移动端单列即可，AppFrame 的窄屏分支已给出答案。
- webserver 注入行、tapIndex（web 专属机制）。
- Montserrat 品牌字体（OFL 许可，可后置）。

## 7. 对 Go 内核侧的要求（从 UI 反推）

- 会话事件流必须能按行 key 订阅增量（session.Log 的 Subscribe 已满足）
- 事件词表已对齐官方 SESSION_FORMAT_VERSION 4
- UI 所需的最小 RPC：列会话、开/关会话、追加 user message、订阅事件、
  中断 turn、审批回调
