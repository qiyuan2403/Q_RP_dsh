# ADR 0001: Android UI 终局形态 —— 原生 Compose + 手写 JNI

状态：已接受（2026-10-16）
依据：`docs/design/dsh-ui-observations.md`（观察）、
`docs/design/android-ui-research.md`（联网调研）、本文记录决定与理由。

## 背景

UI 形态在本项目历史上折返三次：WebView（HTTP/SSE）→ Gio → 原生 View
表单。每次折返都留下半截代码（server/assets 已删、harness 包已删、Gio
探针在 /tmp）。用户约束：不直接挪用 dsh 的 web 前端，用 Go 重构内核、
原生复现其设计语言，走标准设计流程。

## 决定

1. **UI 层**：Jetpack Compose（Kotlin），设计语言按 dsh 的 token 体系
   移植（`docs/design/dsh-ui-observations.md` §3 的 Dsw token 表）。
2. **Go↔Kotlin 边界**：保持手写 JNI（`Native.runTurn` + 回调接口），
   不用 gomobile bind。边界数据形状：入参 JSON 字符串，出参经
   `subscribe(sessionId, callback)` 回调接口推送结构化事件。
3. **布局**：单列 + ModalNavigationDrawer（会话列表）。三列 AppFrame
   留给 Windows 客户端（Compose Multiplatform 可复用同一 Kotlin 代码）。
4. **流式 Markdown**：multiplatform-markdown-renderer（streaming API +
   完整 GFM）；冻结粒度不足时自研 `UNSTABLE_TAIL` 层兜底。
5. **列表**：`LazyColumn` + 稳定 `key(eventId)` + `contentType`，行内
   `collectAsState`——与 dsh 的行级订阅同构。
6. **构建**：Gradle 9.7.1 + AGP 8.13 + Kotlin 2.0.21 + Compose BOM，
   依赖走阿里云 google 镜像（dl.google.com 不可达，实测）；构建脚本
   固化进 android/，替换手写 javac/d8 链；CI 单独 job。
7. **模式 prompt**：三种制卡模式的提示词由用户自理；产物只实现"模式 =
   提示词片段 + 工具白名单"的机制。

## 被否决的方案（及原因，防止再折返）

| 方案 | 否决原因 |
|---|---|
| WebView + 本地 HTTP/SSE | Chromium 合成管线在流式重绘下 GPU 持续高载（用户实测 dsh web 端 GPU 99.5%）；额外 85MB PSS、双进程 |
| Gio | 纯 Go 但 CJK 输入/选择/惯性滚动需自手搓，实测 PSS 62MB 只省 23MB，工程风险不成比例；且 UI 代码无法复用到 Windows 桌面 |
| gomobile bind | 全局跨界锁串行化长 turn；手写 JNI 已实测稳定 |
| 保留 WebView 仅做设置页 | 引入双渲染栈，维护成本 > 收益 |

## 后果

- Kotlin 进入技术栈（此前纯 Go + 6 行 Java 壳）。Kotlin 代码只做视图与
  状态映射，业务逻辑全在 Go —— 这是"UI 用原生复现 dsh 设计语言"与
  "内核纯 Go"的交点。
- 构建链迁移到 Gradle 是一次性大改动；手写 javac/d8 链在迁移 PR 中删除。
- nodebundle（npm 生态）与 UI 无耦合：插件产出的 JSON 事件直接进
  Compose 渲染层。
