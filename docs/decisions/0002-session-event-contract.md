# ADR 0002：会话事件存储与消费边界

状态：已接受（2026-10-17）
依据：`session/vocabulary.go`、`session/log.go`、`agent/agent.go`；目标上游为 DeepSeek Harness（DSH）。

## 背景

本项目是 DSH 的 Go 重写。会话事件名称、数据含义、写入顺序、恢复和投影行为必须遵循固定版本的 DSH；Go 的日志实现只能替换底层机制，不能定义一套不同的会话语义。

当前实现使用 session v4 事件词汇、JSONL 追加日志、单调序号和实时订阅。本 ADR 冻结这些已存在的存储实现边界，并明确：当前 Go 实现的行为不自动等于 DSH 兼容，仍须按固定上游版本逐项对照。

## 决定

### 1. 上游兼容基准

- DSH 官方源码和固定的上游版本是事件类型、payload、顺序、投影及恢复语义的权威来源。
- `session.KnownEventTypes` 是本 Go 构建实际识别的事件集合，不是独立于 DSH 的事件规范。
- 新增、移除或改变事件前，必须先确认固定 DSH 版本中的对应行为，并更新兼容测试。
- 若 Go 实现与 DSH 不同，记录为兼容缺口并修复；不得以本 ADR 或本地测试把差异重新定义成 DSH 合同。

### 2. 当前持久化实现

- 会话目录使用 `header.json` 和 `events.jsonl`。
- `header.json` 的版本由 `session.FormatVersion` 决定，当前实现值为 `4`；该数值及字段须以 DSH session v4 定义为准。
- `events.jsonl` 每行是 JSON 事件对象。当前 envelope 包括 `type`、`seq`、`time`、`data`，并支持 `ignorable`、`surfaceOp`、`sourceEventSeqs`。
- `Log.Append` 校验 JSON，分配缺省序号和时间，并拒绝本实现不认识的非 ignorable 事件。
- `Open` 拒绝格式版本不匹配和非单调序号，避免在无法解释的日志后继续追加。
- 这些是当前 Go 实现的行为记录；与固定 DSH 版本的完全互操作仍须由官方样本和对照测试证明。

### 3. 事件处理与订阅

- `Log.Append` 是当前 Go 写入入口；实时订阅只用于通知消费者有新事件。
- 订阅通道有界，慢消费者可能丢事件，不能把该通道当作持久事实源。
- 消费者需要完整状态时应从日志读取和重放，并利用事件序号检查连续性。
- UI 只消费 DSH 事件或从这些事件派生的只读视图，不得自定义替代 DSH 的持久事件语义。
- Android JNI/Compose 与 Windows UI 使用同一 Go 事件来源；平台传输封装不能改变事件含义。

### 4. Agent 与回合事件

- 当前 `agent/agent.go` 会写入 `user/message`、`turn/start`、`step/start`、`assistant/message`、`tool/call`、`tool/result`、`step/end` 和 `turn/end` 等事件。
- 上述列表只描述当前代码路径，不构成新的规范顺序或对完整 DSH 生命周期的兼容声明。
- `assistant/message`、工具调用、错误、取消和回合结束的 payload 与顺序，必须按固定 DSH 版本逐项验证。
- 当前 `llm.Provider` 是一次性 `Complete` 接口；本项目尚未据此声明 DSH 流式行为已实现。不得添加本地专有持久事件来替代 DSH 流式语义。

### 5. 可靠性边界

- 持久化成功与实时 UI 通知是不同保证；订阅丢事件必须可通过日志回放恢复。
- 未知必需事件、损坏 JSON 或格式版本不匹配不得静默解释为其他事件。
- 取消、错误、进程中断和未闭合 turn 的恢复行为以 DSH 为准；当前 Go 实现尚未完成全部恢复合同验证。
- 只有在固定 DSH 版本中确认的持久化边界才作为兼容性承诺。

## 不在本 ADR 内

- DSH 全部事件类型的 payload 和生命周期语义清单；由上游对照表维护。
- token 级 provider API、JNI 方法命名及线程调度细节。
- 插件接口、权限和平台 UI 组件。
- 日志压缩、迁移和归档工具。

## 验收

- `go test ./session ./agent` 验证当前存储和 Agent 实现的本地行为。
- DSH 兼容性必须另以固定上游版本、官方样本及逐事件比较验证，不能仅以本地单元测试通过作为证明。
- Android/Windows UI 必须可以从同一 session 事件日志恢复视图，不得依赖实时订阅永不丢事件。
