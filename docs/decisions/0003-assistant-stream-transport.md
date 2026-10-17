# ADR 0003：assistant-stream 临时帧与 JNI 传输

状态：已接受（2026-10-17）
依据：上游 `packages/core/agent-loop/src/assistant-stream.ts`、`packages/core/agent/src/runtime-types.ts`；Go `agent/`、`android/jni/`、Android UI。

## 背景

DSH 的会话日志是持久事实源，但模型回复在生成过程中就需要展示：用户要在 token 流入时看到文字出现，而不是等整段回复提交后才刷新。上游把这两件事分开：

- durable 事件：`assistant/message`（或 attempt 记录）写入 session 日志，恢复与投影以它为准。
- 临时帧：`agent/assistant-stream` 通知，只发给实时观察者，不持久化。`start`/`chunk`/`end` 三次通知，`end` 的 `committed` 分支在 durable 事件提交成功后发出并携带其 seq。

Go 版本此前只做前半段：SSE 增量在 provider 与 agent 内被收集成完整回复，UI 在回合结束后重放日志，长回复期间界面没有反馈。

## 决定

### 1. Go 侧帧合同

`agent.AssistantStreamFrame`（JSON 字段即传输格式）：

- `start`：`attemptId`、`revision`、`turn`（1 起）、`step`（0 起）。
- `chunk`：`attemptId`、`revision`、`index`（attempt 内从 0 连续递增）、`time`（Unix ms，同一条 chunk 只取一次）、`chunk`（`textDelta`、可选 `toolCallDelta`、可选 `usage`）。
- `end`：`attemptId`、`revision`、`index`（该 attempt 已发 chunk 数）、`outcome`（`committed` 带 `eventType` 与 durable `seq`，或 `abandoned`）。

规则：

- 每个 Agent 生命周期内 `revision` 单调递增，从 1 开始；一个 attempt 恰好一个 terminal 帧。
- 只有 `llm.StreamProvider` 产生帧。非流式 provider 不发帧，UI 以 durable 消息兜底。
- 一帧都不写 session 日志。durable 事实仍是 `assistant/message`。
- 回调 `agent.Config.OnAssistantStream` 在 turn 的 goroutine 上同步执行，必须不阻塞：观察者慢不能拖停模型调用。
- 模型流失败、取消、durable append 失败都发 `abandoned`；只有 append 成功才发 `committed` 并携带 append 返回的 seq。

### 2. JNI 传输

`pollEvent` 的既有通道不变，仍是 session 事件 JSON。临时帧被包装成一条不持久化的事件：

```json
{"type":"agent/assistant-stream","time":<ms>,"seq":0,"data":{<AssistantStreamFrame>}}
```

- 每个回合一个合并通道（map 条目只在该回合存活）：日志订阅事件与临时帧写进同一个 channel，Java 侧只轮询一个入口，不用两套协议。
- 同一 session 同时只允许一个回合：宿主在进入昂贵准备前就做互斥，第二个回合返回 `jni: a turn is already running for this session`。清理按所有权校验，旧回合的 defer 不会删掉后注册的 stream/cancel。
- 临时帧写入是非阻塞 send：通道满时丢帧而不是阻塞模型。丢的是视觉进度，durable 消息在回合结束后从日志重放，所以丢弃不影响事实。
- durable 事件在通道满时的 teardown 兜底路径可能丢（copier 中尚未入队的事件），队列里已有的事件会送达。live 通道不是 durable 事实，UI 回合结束用 `openSessionId` 全量重放，其他消费者必须从日志读事实。
- 回合结束时先退订日志、等复制 goroutine 退出（最多 100ms，满通道时兜底放弃），再关闭合并通道；`pollEvent` 读到关闭返回空串，与“暂无事件”保持同一语义。
- 不用 Go 线程回调 JVM：Java 侧在自己的工作线程上调用 `pollEvent`，JNIEnv 始终属于 JVM 线程。JNI 返回字符串的 C 副本在 helper 内释放，不再按调用泄漏。

### 3. 取消

`cancelTurn(sessionId)` 通过 per-session 的 `context.CancelFunc` 取消正在运行的回合。注册发生在回合开始时、所有昂贵准备之前，所以准备阶段点取消也能立即生效。取消后 turn 以 `cancelled` 结束，未提交的 attempt 发 `abandoned`，UI 清掉直播段。取消不写额外日志：已进入 RunTurn 时 `turn/end` 的 reason 就是事实记录；在 RunTurn 之前取消则回合没有开始，不会写 turn/start、turn/end。

只有 `context.Canceled` 归为 cancelled。`context.DeadlineExceeded`（JNI 的 10 分钟上限，包含准备阶段）记为模型错误并带错误文案，不会被 UI 当成用户取消。

### 4. UI 消费规则

- 直播段是临时展示，不是事实：`chunk` 追加文本、`end committed` 后仍以 `openSessionId` 重放的 durable 日志为准，避免直播段与最终消息重复。
- 流式期间用纯文本渲染；durable 消息落定后才走 Markdown。原因：每 token 跑 Markdown 解析在设备上是明显的 CPU 开销，用户感知不到差异。
- 直播文本用独立 state，LazyColumn 末尾固定 key 的单个 item 承载，UI 刷新按时间节流（约 50ms）。禁止每个 chunk 重建整个列表。
- 自动滚动只在用户接近底部时触发，避免抢走用户上滑查看历史的操作。

## 已知差异

- **Agent 生命周期按回合重建。** JNI 宿主每个 `runTurn` 都新建一个 Agent（会话历史从日志重建），所以 `turn` 恒从 1 开始、`revision` 与 `attemptId` 序列每回合重置。UI 必须以 `start` 帧为重置边界，不能跨回合用 revision 排序。上游 Agent 生命周期跨回合。若要跨回合保持，需要让 JNI 按会话复用 Agent，属后续改动。
- **step 从 0 开始。** 与本地 `step/start` 事件一致；上游帧里的 step 是 1 基。
- **start 在请求之前发。** 上游先建立 stream 再发 start，请求本身失败时零帧；Go 在调用 provider 前发 start，因此连接失败也会看到 start+abandoned 一对帧。
- **中断不落半截消息。** 上游在中断且已有内容时提交带 `interrupted: true` 的 `assistant/message`，内容为空时提交 `assistant/attempt`，provider 流错误也会提交 `assistant/attempt`；Go 没有 attempt 事件类型，未提交的 attempt 只发 `abandoned`，不写部分内容。
- **取消检查与帧发射之间有窗口。** 取消后不再发 chunk、不再累积文本，但取消检查与 push 之间没有加锁，极小概率多发一帧；尝试终态仍是 abandoned。
- **丢帧可能让直播段缺字。** 通道满时丢帧，UI 不校验 `index`/`revision` 连续性，缺口要等回合结束重放才补齐。
- **JNI 每回合新建 Agent** 导致宿主侧 `turn` 恒为 1、`revision`/`attemptId` 每回合重置；`agent` 包本身跨回合连续。UI 以 start 帧为重置边界，不跨回合用 revision 排序。

## 不做什么

- 不把临时帧写入 session 日志。上游明确将其定义为进程内通知；写入会重复表达 durable 事实，并给恢复逻辑制造第二条来源。
- 不做逐帧 ACK/重传。丢帧可接受，durable 日志兜底。
- 不为非流式 provider 伪造帧。宁可 UI 显示等待状态，也不编造不存在的流。

## 验证门槛

- Go：帧顺序、attempt 边界、revision 连续、committed seq 与日志一致、abandoned 路径（流错误/取消/append 失败）、非流式 provider 零帧。
- JNI：回合期间能收到 `agent/assistant-stream` 事件且日志里没有该 type；取消返回 cancelled。
- UI：Gradle 编译通过；真机上中文输入、滚动、取消的真实表现另列验收项。
