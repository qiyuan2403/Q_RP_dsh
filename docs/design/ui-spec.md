# UI 设计规范 · Android 原生复现 DSH 设计语言

来源：`dsh-official/packages/client`（ui-theme / ui-primitives / ui-layout / ui-chat）逐文件解剖，
联网核对了 Compose 官方性能指南与流式聊天组件的成熟实践（Stream Chat Android AI）。
本文是 Android 侧实现的唯一依据；与 CSS 原文冲突时以本文为准。

## 1. Token 体系（从 CSS 变量到 Go 常量）

DSH 的 token 分三层，我们原样保留分层：

| 层 | DSH 载体 | 我们的载体 |
|---|---|---|
| static | `--dsw-static-*`（色板原始值） | `DswColors`（Kotlin object） |
| alias | `--dsw-alias-*`（语义引用，指向 static） | `DswAliases`（light/dark 两套 map） |
| 字体/圆角/动效 | `--dsw-font-*`、`--dsw-radius-*`、`--ds-*` | `DswMetrics` |

### 1.1 色板（static 层，摘录 Android 界面会用到的全部）

品牌色（deepseek 系，浅色主题主色即 `-500`）：

| token | light | 用途 |
|---|---|---|
| `deepseek-100` | rgb(228,237,253) | 选中底 |
| `deepseek-400` | rgb(122,170,255) | 暗色 toast 主色（提亮版） |
| `deepseek-500` | rgb(65,118,230) | 链接/主色 |
| `deepseek-900` | rgb(40,49,66) | 深色底 |

中性色（bluish 系，界面骨架）：

| token | light | dark |
|---|---|---|
| `neutral-bluish-00` | rgb(255,255,255) | rgb(255,255,255) |
| `neutral-bluish-50` | rgb(249,250,251) | rgb(249,250,251) |
| `neutral-bluish-100` | rgb(235,238,242) | rgb(235,238,242) |
| `neutral-bluish-400` | rgb(173,178,184) | 同 |
| `neutral-bluish-500` | rgb(151,157,166) | 同 |
| `neutral-bluish-700` | rgb(97,102,107) | 同 |
| `neutral-bluish-750` | rgb(67,69,74) | 同（浅色 hover 用它） |
| `neutral-bluish-850` | rgb(44,44,46) | 同 |
| `neutral-bluish-900` | rgb(27,27,28) | 同 |
| `neutral-bluish-1000` | rgb(15,17,21) | 同 |

状态色：`green-500` rgb(34,197,94) / `red-500` rgb(239,68,68) / `red-600` rgb(236,19,19) /
`amber-500` rgb(245,158,11)。

### 1.2 语义层（alias，light 值；dark 换绑 static）

| alias | light 绑定 | 语义 |
|---|---|---|
| `bg-base` | bluish-00 | 窗口底 |
| `bg-layer-1/2/3` | bluish-00 | 层叠面板底 |
| `specific-sidebar-fill` | bluish-50（dark: bluish-875，实测源码） | 侧栏底 |
| `label-primary` | bluish-1000 | 主文本 |
| `label-secondary` | bluish-700 | 次文本 |
| `label-caption` | bluish-400 | 弱说明 |
| `border-l1/l2/l3/l4` | rgba(0,0,0,.04/.1/.12/.16) | 边框四档 |
| `button-primary-fill` | `= brand-primary` = bluish-1000（黑） | 主按钮底 |
| `button-primary-hover` | bluish-750 | 主按钮 hover |
| `label-primary-foreground` | bluish-00 | 主按钮文字 |
| `interactive-bg-hover` | rgba(38,49,72,.06) | 行 hover |
| `interactive-bg-active` | rgba(38,49,72,.1) | 行 active |
| `link` | deepseek-500 | 链接 |
| `code-diff-added/deleted` | green/red 的 8% 透明 | diff 底 |

暗色切换规则：`body[data-ds-dark-theme]` 重绑 static + alias，dark 下
`bg-base → bluish-950`、`label-primary → bluish-00`、sidebar-fill → bluish-875
（以 CSS 原文为准逐条抄录，不猜）。

### 1.3 几何与字体

- 圆角阶梯：xs 4 / sm 8 / **md 12（标准控件）** / lg 16 / xl 20 / panel 28
- 控件高度：**md 36px、sm 28px**；按钮字 14px/行高 22px；sm 字 12px/行高 18px
- 字号阶梯（theme-settings）：正文 14（10–22 可调）、代码 11（10–16）、终端 13（10–20）
- 动效：时长 fast 0.1s / 常规 0.2s / slow 0.3s，曲线 `cubic-bezier(0.4,0,0.2,1)`；
  `prefers-reduced-motion` 时全部取消
- 焦点：light `#4176E6` / dark `#7AAAFF`（focus.css）

## 2. 布局几何（AppFrame 三列）

columns.ts 的数字就是合同：

```
SIDEBAR_MIN=264  SIDEBAR_MAX=420  SIDEBAR_DEFAULT=280  SIDEBAR_COLLAPSED=56
SIDEBAR_AUTO_COLLAPSE=1024（视口低于此自动折叠）
CENTER_MIN=400   RIGHTBAR_MIN=300  RIGHTBAR_MAX_RATIO=0.7  RIGHTBAR_DEFAULT_RATIO=0.45
```

收缩顺序合同：**右列先缩、再消失轨道，然后 center 才允许低于 400**；sidebar 不参与让位。
Android 手机上没有三列：直接取 `sidebar=0（抽屉态）+ center=全宽` 的特例，
`computeColumns` 逻辑原样移植（它的收缩顺序在分屏/平板下仍然成立）。

## 3. 原子组件清单（Android 复现范围）

ui-primitives 共 41 个组件。Android 首批只复现会话必需的 11 个：

| 组件 | 尺寸/形态 | 备注 |
|---|---|---|
| Button | md 36 / sm 28；primary=黑底白字，ghost hover 灰，outline 0.5px 边 | |
| Input | 与 Button 同圆角；边框 border-l3，聚焦换焦点色 | |
| Switch | 36×20（源码实测） | |
| Pill / Tag / StateDot | 状态徽标 | |
| DisclosureRow | 折叠行（工具卡用） | |
| TerminalBlock / ReadBlock / DiffBlock | 工具结果三种卡 | DiffBlock 用 `code-diff-*` |
| MarkdownText | 见 §4 | |
| Toast | 深色底 deepseek-400 文字 | |

## 4. 会话视图（ui-chat）

结构（ChatView.module.css 原文）：

```
frame（column flex，占满）
├─ scroll（flex:1，overflow-y:auto，padding 16px + composer-clearance+16px）
│   └─ 消息流（按节点顺序，用户/助手/工具卡交替）
└─ composer（输入卡，宽度 = 转录区 - 32px 的合同）
```

- 跟随尾部的合同：`data-chat-following-tail` 时 `overflow-anchor:none`，
  新 token 到达 → 滚动到底；用户上滑 → 解除跟随，出现"回到底部"浮标
- 流式渲染：`AssistantMarkdown` 按增量追加；Compose 对应 LazyColumn +
  `key(node.seq)` + 每个 node 独立 `remember`，token 追加只重组最后一个
  node（Stream Chat AI 的做法与本结构同构）

## 5. Android 实现定案

### 5.1 运行时形态

- **Jetpack Compose（Kotlin）**，Activity 单窗口；Go 内核经既有 JNI（`Native.runTurn` +
  新增的流式事件回调）驱动
- 渲染管线 = 系统 HWUI：流式吐字只重组最后一条消息的 Text，GPU 只画脏区，
  绝无浏览器式全量合成

### 5.2 构建链（现实约束下的分步）

1. **第一步仍用现有手写链（javac/d8）+ View 体系**实现布局与 token（View 的
   XML/代码绘制完全能表达 §1–§4），先把内核↔UI 的数据通路打通
2. Compose 依赖 Gradle/AGP + Kotlin 编译器插件（Compose compiler 已并入
   Kotlin 发行版，裸 kotlinc `-Xplugin` 亦可驱动，但 androidx 构件树庞大）；
   切 Compose 的时机 = 引入 Gradle wrapper 的时机，两者一起切，不单独过渡
3. token 层先用 Kotlin 常量表达（static+alias 两层照抄本文表格），切 Compose
   后直接映射 Compose 主题

### 5.3 事件通路（流式的根）

`Native` 增加流式接口：Go 侧 `session.Log.Subscribe` 的事件经 JNI 回调
推给 Java worker 线程 → `runOnUiThread` 投递 → UI 状态更新。禁止 Go goroutine
反向 `AttachCurrentThread`（此前顾问已定）。

## 6. 明确不做

- 像素级复刻 CSS 动效（shimmer、渐变阴影文本）——移动端首版以静态等价物替代
- 三列可拖拽分栏——手机形态用抽屉；平板/桌面再启用 computeColumns 全逻辑
- 桌面端 titlebar/vibrancy 分支——Android 无此概念
