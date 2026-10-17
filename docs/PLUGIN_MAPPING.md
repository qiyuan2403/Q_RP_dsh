# 插件与 DSH 服务映射

## 判定

上游 DSH 插件合同（`vendor/cordis/src/registry.ts` 的 `Plugin`、各官方包 `src/index.ts`）：

- `name`：loader 诊断名。
- `inject`：依赖的服务名，服务不全时插件停在 pending。
- `Config`：schemastery schema，带默认值。
- `apply(ctx, config)`：插件体；工具经 `ctx.tools` 注册，注册项的 disposer 随 fiber 销毁回收。
- 服务由 `ctx.provide(name, value)` 提供，官方服务名包括 `tools`、`systemPrompt`、`web`、`fs`、`credentials`、`workingDirectory`。

Go 现状：

- `cordis.Plugin` 有 `Name`、`Inject`、`Provide`、`Apply`、`ResolveConfig`，形状够用（`/root/repos/Y_Cordis/cordis/registry.go:15`）。
- `runtime.loadRow` 给每行的 `Apply` 是空函数，注释写明“等插件注册表替换它”（`runtime/mount.go:174`）。所以 profile 里任何行现在都不执行插件体。
- `kernel.Provide` 只接受 `session`、`tools`、`llm`、`agent` 四个名字（`kernel/kernel.go:17`）；`web`、`fs`、`credentials` 等官方服务名会被当成拼写错误拒绝。
- 六个 Go 插件都是库：构造函数加方法，没有 `Plugin` 入口，也没有工具注册。主仓库 `go.mod` 不依赖插件仓库。

结论：**目前没有任何一个 Go 插件符合 DSH 插件合同。**“代码存在”和“已接入”必须分开记。

## 一、六个 Go 插件

| Go 包 | 现有能力 | 上游对应 | 需要的 DSH 服务 | 接入缺口 |
|---|---|---|---|---|
| `websource` | `Fetcher.Fetch` HTTP 取页并降为文本；`Fetcher.Search` 走通用 `{query,max_results}` 合同 | `packages/web/`（分层见第二节） | `tools`、`systemPrompt`，内部消费 `web` | 只算 provider 素材，缺 web 服务与 tool-web 等价物 |
| `card` | `Layout` 目录布局、PNG 角色卡 chunk 读写、`Card` 文档 | 无（RP 产品功能，上游无对应插件） | 无，纯库；要暴露工具则需 `tools` 和 `workingDirectory` | 无 `Plugin`/`Config`/工具 schema |
| `cardgit` | 进程内 go-git 管理卡片工作区：提交、日志、回滚、diff | 无 | `tools`、`workingDirectory` | 同上 |
| `gallery` | 图片资产索引：扫描、添加、按标签查找 | 无 | `tools`、`workingDirectory` | 同上 |
| `hubsync` | RP-Hub 索引、发布、下载 | 无 | `tools`、`credentials`（令牌） | 同上，另需凭据读取合同 |
| `imagegen` | 生图 HTTP 客户端 | 无 | `tools`、`credentials`（密钥） | 同上 |

上游没有对应插件不等于可以不守合同：这五个产品插件仍须按 `name`/`inject`/`Config`/`apply` 形状包装，配置由 DSH 的 loader 求值，工具经 `ctx.tools` 注册，才能称为“内置但仍由 DSH 插件机制加载”。

## 二、websource 与上游 web 分层

上游 `packages/web/` 是四层，不是一层：

| 上游包 | 职责 | 依据 | Go 现状 |
|---|---|---|---|
| `web` | `ctx.web` 服务：注册 search/fetch provider，按配置 id 或“唯一可用”选择执行 | `web/src/index.ts`（`WebRuntimeConfig`、`WebSearchProvider`、`WebFetchProvider`） | 无此服务 |
| `web-fetch-http` | fetch provider | `web/web-fetch-http` | `websource.Fetch` 近似，但不是 provider 接口 |
| `web-search-deepseek` / `web-search-exa` / `web-search-perplexity` | search provider | 各自 `src/index.ts` | `websource.Search` 是通用 JSON 合同，不对应其中任何一个 |
| `tool-web` | 注册 `web_fetch`、`web_search` 工具，`inject = ['tools','web','systemPrompt']` | `web/tool-web/src/index.ts:24,457,326` | 无 |

所以 `websource` 映射到的是 provider 层，不是工具层。把它当“联网插件”直接注册两个工具，工具名、schema、超时和 provider 选择都会和上游不一致。

## 三、接入顺序

1. 内核服务名登记：把 `web` 加入 `kernel` 的合法服务集合，保留拼写检查；后续 `fs`、`credentials` 按同一方式登记。
2. Go 版 `web` 服务：provider 注册表加执行期选择，语义照 `web/src/index.ts`。
3. `tool-web` 等价物：工具名 `web_fetch`/`web_search`，参数 schema 和输出上限照 `tool-web/src/fetch.ts`、`search.ts`。
4. `websource` 改成 provider 实现，注册进 `web` 服务。
5. 插件注册表：`runtime` 按行名映射到 `cordis.Plugin`，替换 `loadRow` 里的空 `Apply`。
6. 其余五个产品插件按同形状包装，逐个接入并验证配置、依赖和卸载。

## 未验证

- 上游工具 schema 的逐字段移植：本表只核对到工具名、注入服务名和分层归属。
- 五个产品插件对应的 DSH 配置字段：需要先定它们各自必须暴露的工具和配置，属于产品决策，未开始。
- npm 插件在设备上的加载：仍按工程计划标为未完成。
