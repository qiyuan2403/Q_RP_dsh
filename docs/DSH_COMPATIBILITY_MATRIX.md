# DSH 兼容差异表

## 基准与判定

- 上游：`@deepseek-ai/dsh-root 0.2.1-alpha.4`，提交 `86a402200d95fecef4529d969836f1fe13c6cb0d`；本地只读源码位于 `/root/dsh-official`。
- Go 基座：`Q_RP_dsh`；Cordis/Loader/Mount 位于相邻仓库 `/root/repos/Y_Cordis`。
- `有实现` 仅表示 Go 侧有代码；`部分` 表示合同或路径尚未覆盖；`未对照` 表示没有固定上游样本/行为验证；不据此宣称兼容。
- 本表是首轮范围清单，优先覆盖计划明确要求的基座模块、事件、配置、插件和会话。上游包含的 Host/Web/Remote/ACP 等产品表面不自动属于 Android 首发范围，纳入与否需由首发支持清单确定。

## 核对记录

2026-10-17 复核：

- 表中上游与 Go 路径逐项检查存在性。修正两处：Cordis 与 Loader 在上游 `vendor/` 下，不在 `packages/`；上游没有 `packages/core/mount`，mount 行为在 `vendor/loader/src/index.ts` 和 `packages/boot/app-boot/src/profile-context.ts`。
- 事件名称：上游 `known-event-types.ts` 与 Go `session/vocabulary.go` 各 59 个，`diff` 为空。只证明名称集合一致，不证明 payload、顺序、投影或恢复。
- `Q_RP_dsh` 全仓库 `go test ./...` 通过（表格“当前证据”列的本地测试即来自这次运行）。
- 插件侧的映射见 [PLUGIN_MAPPING.md](PLUGIN_MAPPING.md)。

## 模块映射

| DSH 上游模块/合同 | 上游源码 | Go 对应实现 | 当前证据 | 差异/下一项 |
|---|---|---|---|---|
| Cordis Kernel、Context、Service、Events | `vendor/cordis/src/` | `/root/repos/Y_Cordis/cordis/` | Go kernel 与事件/服务代码及测试存在 | [ ] 建立固定提交的官方样本；逐项对照事件派发、作用域、销毁和服务可见性 |
| Loader、配置插值、patch | `vendor/loader/src/`；官方应用 patch 在 `packages/*/` | `/root/repos/Y_Cordis/loader/`、`testdata/official/` | 有解析、插值、对象合并及官方 patch golden 测试 | [ ] 覆盖官方 profile/bundle 激活和 config 求值行为；保留差异报告 |
| Scope、Mount | `packages/core/scope/src/`、`vendor/loader/src/index.ts`、`packages/boot/app-boot/src/profile-context.ts` | `/root/repos/Y_Cordis/cordis/`、`mount/` | scope/mount 实现与测试存在 | [ ] 以官方组合测试确认生命周期、跨 scope 服务可见性及退出顺序 |
| Agent API 与事件 | `packages/core/agent/src/` | `Q_RP_dsh/agent/`、`kernel/` | Agent turn/tool 基础实现和本地测试存在 | [ ] 对照完整 Agent 状态、取消、生命周期事件和插件拦截合同 |
| Agent loop、工具回合 | `packages/core/agent-loop/src/agent.ts`、`tool-calls.ts` | `agent/agent.go` | OpenAI-compatible SSE 文本、tool-call delta 拼装和多步工具回合有测试 | [ ] 官方 loop 样本覆盖 turn/step 边界、错误/重试、取消和恢复 |
| 临时 assistant-stream | `packages/core/agent-loop/src/assistant-stream.ts`；上游事件 `agent/assistant-stream` | `agent/agent.go` 当前先收集流，完成后提交 `assistant/message` | 计划明确记录无临时帧 | [ ] 实现 start/chunk/end 传输、终态与 durable event 对应及 UI 增量消费 |
| Session store、surface 与 request header | `packages/core/session/src/index.ts`、`surface.ts`、`request-header.ts` | `session/log.go`、`vocabulary.go`、`agent/replay.go` | JSONL envelope、v4、未知必需事件拒绝、surface 元数据有实现和测试 | [ ] 用官方 session v4 样本比较 header/event 写入读取、surface fold、header fold、投影和恢复 |
| Session repair 与 tool-call recovery | `packages/core/session/src/repair.ts` | 当前 `session/` 尚无完整对应的官方恢复流程 | 上游有中断 turn closers、未完成 tool call 恢复；计划明确恢复对齐未完成 | [ ] 对照中断 turn、取消、崩溃尾部、未完成工具调用和重启续写 |
| Tools registry/schema/execution | `packages/core/tools/src/` | `tools/registry.go`、`tools/builtin/`、`agent/agent.go` | 注册、参数校验、调用和结果事件有实现 | [ ] 对照 schema 语义、执行边界、错误分类、并发/取消及官方 PTC 行为 |
| System prompt assembly | `packages/core/system-prompt/src/` | `systemprompt/registry.go`、`boot/` | prompt registry/assembly 有实现和测试 | [ ] 对照分段、排序、模板变量和官方 profile 组合样本 |
| Profile/bundle 插件激活 | `packages/boot/app-boot/`、`packages/boot/plugin-manager/`、`packages/bundle/` | `boot/`、`internal/pluginmgr/`、`runtime/mount.go`、`/root/repos/Y_Cordis/loader/` | 有 Go 加载、配置解析、挂载代码；`Y_Cordis/testdata/official/` 有 patch golden | [ ] 用官方 profile/bundle 真样本验证实际激活结果、依赖顺序、禁用谓词和 config waterfall；现有 golden 不等同完整装载验证 |
| 配置编辑与运行时重载 | `packages/boot/config-editor/`、`packages/boot/hmr/` | 当前有 Loader config 解析；无已确认的完整 editor/HMR 对应 | 未建立完整对应关系 | [ ] 确认 Android 首发需要的配置写入/重载合同；排除不支持表面并记录原因 |
| npm 插件兼容侧车 | 官方插件包与 Node/JS 插件加载合同 | `android/app/.../NodeRuntime.java`、`jni/main.go`、`Y_Cordis/jsbridge/`、`tools/builtin/plugin_manager.go` | Node 可打包与隔离 evaluator/plugin manager 存在；APK 结构已验证 | [ ] 在设备运行 npm 插件加载、服务/config 可见性、生命周期、权限和失败结算的真实组合测试 |

## 持久事件覆盖

上游事件类型真值来源：`packages/core/session/src/known-event-types.ts`，由 `SessionEventMap` 生成。Go 类型集合位于 `session/vocabulary.go`。两份当前事件名列表按源码比对一致；这只证明名称集合一致，不证明每个 payload、顺序、投影或恢复行为相同。

| 对照面 | 上游依据 | 当前 Go 状态 | 未完成验证 |
|---|---|---|---|
| 格式版本和 envelope | `packages/core/session/src/types.ts` (`SESSION_FORMAT_VERSION`, `SessionEvent`) | v4；`type/seq/time/data/ignorable/surfaceOp/sourceEventSeqs` 字段有对应 | [ ] 用官方生成样本比较编码、缺省字段、字段拒绝与跨实现 round-trip |
| 事件名称 | `packages/core/session/src/known-event-types.ts` | `session/vocabulary.go` 当前名称集相符 | [ ] 机械集合差异检查加入 CI，防上游变化漏同步 |
| Agent durable 事件 | `packages/core/session/src/types.ts`、`packages/core/agent-loop/src/agent.ts` | Go 写入基础 user/turn/step/assistant/tool 事件；`tool/result` 在 `output` 之外保存模型可见 `text`，恢复优先使用它 | [ ] 逐事件 payload、序号、时间和失败/取消路径对照 |
| 临时 stream 帧 | `packages/core/agent-loop/src/assistant-stream.ts` | Go 当前未发布等价帧 | [ ] 实现并覆盖 start/chunk/end 及 abandoned/committed 终态 |
| Session surface/projection | `packages/core/session/src/surface.ts`、`types.ts` | Go event envelope 可保存 surface 元数据；重放/UI投影覆盖面有限 | [ ] 对照 append/replace、source seq、image/offload 解释和恢复结果 |
| 损坏/中断恢复 | `packages/core/session/src/repair.ts`、`preparation.ts` | Go 可拒绝格式不匹配、非单调序号；没有证据证明 repair 行为等价 | [ ] 官方故障样本驱动的兼容测试 |

## 配置与插件合同

| 合同 | 上游源码依据 | Go 当前实现/证据 | 状态 |
|---|---|---|---|
| 插件 manifest 与默认配置 | 官方插件包 `package.json`、`src/index.ts`、config catalog | `boot/` 支持部分 Cordis patch 配置 | [ ] 逐插件映射 name/inject/Config/apply 与依赖 |
| Loader 生命周期 | 官方 Cordis/Loader 与 `packages/boot/app-boot/` | Go `Y_Cordis/loader`、`mount` 已有实现及测试 | [ ] 对照失败回滚、禁用、scope 销毁和依赖顺序 |
| 服务可见性 | 官方 Context/Scope 行为 | Go `cordis/visible.go`、scope 服务访问实现 | [ ] 官方组合中验证任意服务按名访问和注入限制 |
| profile/bundle 组合 | `packages/bundle/`、`packages/boot/plugin-manager/` | Go Loader 和 plugin manager 有基础实现 | [ ] 用官方真实 profile、bundle 和禁用谓词做端到端样本 |
| 插件生命周期/权限 | 各官方插件 manifest/config/权限合同 | 六个 Go 插件在 `Q_RP_DSH_plug-in`，主仓尚未依赖该仓库（见工程计划） | [ ] 先建立六插件逐项映射，再接入主构建并验证生命周期和授权 |

## 接受差异的门槛

- 每项差异须指向固定上游路径/符号和 Go 对应实现；不可用同名包或本地测试替代兼容证据。
- “完成”需有官方固定版本输入、可重复的 Go 输出/事件比较和覆盖失败边界的测试；未测试的项继续标为未验证。
- Android 首发不支持的官方产品面需进入首发支持清单并明确排除依据，不能默默从差异范围删除。
