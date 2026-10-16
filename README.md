# Q_RP_dsh

**简体中文** | [English](#english)

> 以 Go 重写的 DSH 兼容宿主，面向 RP-Hub 角色卡生态的 Agent 客户端。

[![License: CC BY-NC 4.0](https://img.shields.io/badge/License-CC%20BY--NC%204.0-lightgrey.svg)](./LICENSE)

---

## 这是什么

`Q_RP_dsh` 是一个 **Agent 宿主客户端**：既可以作为通用 agent 使用，也可以切换为**角色卡工坊**模式。

底座是 [DSH（DeepSeek Harness）](https://github.com/deepseek-ai/deepseek-harness) 及其内核语义的 Go 重写，内核部分见 [Y_Cordis](https://github.com/qiyuan2403/Y_Cordis)。

## 目标平台

**Android**
**windows**

## 核心业务：角色卡工坊

角色卡是「PNG 容器 + JSON 载荷」，直接对 PNG 做版本管理是灾难：改一个字段就重写整个二进制，无法 diff。

因此采用**解包工作区**：

```text
<workspace>/<slug>/
├── .original/card.png     导入件，永不覆写
├── meta.json              身份：名称、版本、来源关键字
├── character.json         卡片文档，缩进格式化以便 diff
├── greetings/             开场白，一个一份
│   ├── default.md
│   └── alternate_1.md
├── lorebook/entries.json  世界书词条
├── assets/                图片等二进制
└── dist/<slug>.png        打包产物，可直接导入游玩
```

**收益**（实测）：改一个字段 → Git 显示 `1 insertion(+), 1 deletion(-)` 的可读 diff，而非整个 PNG 重写。

打包从 **`.original/` 原件**出发，而非上次打包产物——保证打包是「(原图, 当前文档)」的纯函数，重复打包不会累积漂移。

## 兼容性：以 RP-Hub 为标准

角色卡读写

| 项 | RP-Hub 行为 | 说明 |
|---|---|---|
| **读取优先级** | `chara` **优先**，`ccv3` 回退 | 与通用规范相反 |
| **无关键字卡** | 宽松回退：文本 >50 字符且以 `{` / `ey` 开头 | 拒收会破坏 RP-Hub 能读的真实文件 |
| **写入位置** | **IHDR 之后** | |
| **其他关键字 chunk** | **保留**，不删除 | |

导入同时兼容 V1 / V2 / V3 三种形态与历史拼写别名（`char_name`、`char_persona`、`creatorcomment`）。

导出结构对齐 RP-Hub 的 `buildCharacterCardData`：`{data:{...}}`，含 `extensions.rp_hub_watermark`，**不写**顶层 `spec`/`spec_version`。

**一处有意的收紧**：RP-Hub 跳过 PNG chunk 的 CRC 校验；本实现**拒绝**损坏文件——静默接受损坏输入会在导出时把损坏写回。

## 主路径零 JS 引擎

Agent 主循环、工具调度、会话持久化、卡片处理**全部纯 Go**。

需要 JavaScript 的只有两处，且都可选：

1. `!!js` 配置表达式求值
2. 第三方 npm 插件加载

这两处通过**独立进程侧车**承载，带硬超时、内存上限、环境擦洗、Node 权限模型（`--permission`）与按路径的 fs 授权。实测热调用 **~400µs**（占 200ms 回合的 0.2%），冷启动仅首次。

## 许可证

[CC BY-NC 4.0](./LICENSE)

**明确禁止任何形式的商业化使用。**

---

## English

`Q_RP_dsh` is a Go rewrite of a DSH-compatible agent host, targeting the RP-Hub character-card ecosystem. It runs as a general agent and switches into a character-card workshop.

**Android first.** Verified on a real device: shared storage (`/storage/emulated/0`) is mounted `noexec` — writable but not executable. A workspace therefore has two roots: a DATA root for user-visible documents and an EXEC root for anything that must run. Capabilities are probed, never inferred, and judged by errno rather than exit status.

**Card workshop.** A card is a PNG wrapping JSON, which is undiffable. The workspace decomposes it into text (`character.json`, `greetings/`, `lorebook/`) plus an untouched `.original/`, so Git shows a one-line diff for a one-field edit. Packing starts from the original, making repeated packs idempotent.

**RP-Hub compatibility.** Card read/write is aligned to RP-Hub's own implementation: `chara` is read before `ccv3`, a keywordless payload still imports via the loose fallback, the chunk is written after IHDR, and other keyword chunks are preserved. The export shape matches `buildCharacterCardData`.

**Zero JS on the main path.** Only `!!js` evaluation and third-party npm plugins need JavaScript, both via an isolated sidecar with hard timeouts, memory caps, environment scrubbing, and Node's permission model.

**License**: [CC BY-NC 4.0](./LICENSE) — non-commercial use only.
