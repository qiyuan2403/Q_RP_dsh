# Q_RP_dsh

**简体中文** | [English](#english)

> DeepSeek Harness（DSH）的 Go 重写，面向 Android，后续支持 Windows。

[![License: CC BY-NC 4.0](./LICENSE)](./LICENSE)

---

## 这是什么

`Q_RP_dsh` 是 DSH 的 Go 重写。完整体由两部分组成：

1. 按固定上游版本重写并验证 DSH 基座行为。
2. 将 `Q_RP_DSH_plug-in` 仓库中的全部 Go 插件纳入应用构建，并通过 DSH 自身的插件机制加载。

插件随应用分发不改变其 DSH 插件身份，仍遵守 DSH 的配置、服务作用域、加载顺序和生命周期。插件扩展基座能力，不替换或另造 Agent、会话、工具及插件逻辑。

内核实现位于 [Y_Cordis](https://github.com/qiyuan2403/Y_Cordis)，宿主位于本仓库，插件实现位于 [Q_RP_DSH_plug-in](https://github.com/qiyuan2403/Q_RP_DSH_plug-in)。

## 目标平台

- Android 优先：Compose/Kotlin 原生 UI，Go 内核通过 JNI 接入。
- Windows 后续：复用 Go 基座与插件，按需增加平台适配。

主 UI 与 Go Agent 主路径不运行浏览器、React 或 JS。为兼容 DSH 的 `!!js` 配置和 npm 插件，可按需使用隔离的 Node 侧车。

安装包体积上限为 500 MB；未超过上限不以压缩为目标。性能压力由用户在真实设备流程中验证，当前不作为规划门槛。

## 当前状态

目前已有 Go 版 Cordis/Loader、Agent、Session、Tools、Runtime，以及独立仓库中的 Go 插件代码。主仓库尚未依赖并构建 `Q_RP_DSH_plug-in`；插件完成接入和 DSH 兼容验证之前，不能称为完整体。进度见 [工程状态清单](docs/ENGINEERING_PLAN.md)。

## 许可证

[CC BY-NC 4.0](./LICENSE)

**明确禁止任何形式的商业化使用。**

---

## English

`Q_RP_dsh` is a Go rewrite of DeepSeek Harness (DSH), targeting Android first and Windows later. The complete application consists of (1) a DSH base whose behavior is verified against a fixed upstream version, and (2) the Go plugins from `Q_RP_DSH_plug-in`, built into the application and loaded through DSH's own plugin mechanism.

Bundled plugins remain DSH plugins and follow its configuration, service scope, load order, and lifecycle. They extend the base without replacing DSH's agent, session, tool, or plugin semantics.

Android uses a native Compose/Kotlin UI over JNI. The main UI and Go agent path do not run browser/React/JavaScript runtimes. An isolated Node sidecar may be used when DSH `!!js` expressions or npm plugins require it.

The package-size ceiling is 500 MB. Performance testing is deferred to the user's real-device workflow. Existing Go code does not prove complete DSH compatibility or plugin integration; see [engineering status](docs/ENGINEERING_PLAN.md).

**License**: [CC BY-NC 4.0](./LICENSE) — non-commercial use only.
