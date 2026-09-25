# 常见问题

## 它会改我的安装目录吗？

不会。`wbmux` 只做两件事：

1. 往自己的缓存目录（`~/.wbmux/generated/`）写一份合并后的配置；
2. 设置环境变量 `ACC_PRODUCT_CONFIG_PATH` 并启动客户端。

安装目录零改动。不想要了，删掉 `wbmux` 和 `~/.wbmux/` 就干净了。

## 切换后登录态会丢吗？

不会。两个后端各自使用自己的数据目录（`~/.workbuddy` 与 `~/.workbuddy-ai`），
与"装了两份"时完全一致。来回切换互不影响。

这是刻意设计的：两套后端的账号体系互不相通，共用一份 profile 会互相冲掉登录态。

## 能同时用两个后端吗？

不能。同一个安装目录、同一份 `app.asar`，一次只能切换出一个后端。
想同时开两个，还是得装两份。

## 我的客户端装在奇怪的位置，它找不到怎么办？

先跑：

```bash
wbmux list
```

看它是靠哪条线索找到的。如果显示"未找到"，直接指定：

```bash
wbmux config set --exe "D:\你\的\路径\WorkBuddy.exe"
```

`wbmux` 会按顺序尝试五条路：显式配置 → 数据目录线索 → 注册表 →
常见安装目录（含盘根） → PATH。绝大多数情况注册表就够了。

## 客户端升级后要重新配置吗？

不用。生成的配置**每次启动都重新生成**，因此客户端升级后自动跟着更新。

但如果升级改动了配置格式，建议跑一次 `wbmux doctor` 确认机制仍在。

## `doctor` 报"配置覆盖机制"失败怎么办？

说明客户端不再包含那两个关键标记，`wbmux` 的路线在这个版本上失效了。
此时**不要**指望切换成功——继续用 `run` 会连到原后端。

可以：

1. 升级 `wbmux`（如果新版本适配了）；
2. 或者退回装两份的原始方式；
3. 欢迎到 issue 里带上 `wbmux doctor` 的完整输出和客户端版本号。

## `--native` 是干什么的？

用目标后端**自己的安装**原样启动，不做任何改写。

它的作用是留一条对照路径：当覆盖模式出问题时，先跑 `wbmux run intl --native`。
如果原生启动正常、覆盖启动异常，说明问题在覆盖机制；如果两者都异常，
说明问题在客户端本身。排错时能省很多时间。

前提是目标档位确实装了。

## 生成配置里哪些字段被改了？

`--dry-run` 会完整列出来：

```bash
wbmux run intl --dry-run
```

只改后端相关的字段：`endpoint`、`stagingEndpoint`、`officialEndpoints`、
`dataFolderName`、`isOversea`、`authentication` 里的三个标识、以及四个域名列表。
其余一律保持宿主原样——特别是 `updates`（避免国内版被引导去拉国际版安装包）
和 `productName`（宿主身份）。

`productFeatures` 整块（国内 124 项 / 国际 132 项）也**原样透传**，一项不动。
它里面装着大量与后端无关的功能开关（`ImageGen`、`BrowserUse`、`ComputerUse`、
`TencentDocsKnowledge` 等），按后端去改会误伤一堆东西。
但这也带来一个真实限制，见下一节。

## 切换后"远程控制"还能用吗？

**分两半，结论完全不同。** 渠道分两类：

- **客户端直连服务商的渠道**：切换后端**不影响**。
- **必须走后端代理的渠道**（微信系全部）：**会失效**。

> 本文档早期版本笼统写过"不受影响"，那是错的。微信系渠道确实依赖后端，
> 详见下文第 2 条。

"远程控制"（内部代号 `claw`）指通过 IM 渠道给本机 Agent 下发任务。

### 1. 直连型渠道：不受影响

| 渠道 | 连接方式 |
|---|---|
| Slack | `@slack/socket-mode`（`apps.connections.open`），客户端直连 |
| Discord | `wss://gateway.discord.gg`，客户端直连 |
| Telegram | `api.telegram.org` + `getUpdates` 长轮询，客户端直连 |
| 企微 AIBot | `wss://openws.work.weixin.qq.com`，客户端直连（WebSocket 模式） |

判定依据是渠道保存路径里的这段代码：

```js
if (config.connectionMode === "webhook" || config.registration?.webhookUrl
    || channelType === "wecomaibot")
  await this.registerChannelWithBackend(...);
```

即 **WebSocket 模式不做后端注册**，客户端与服务商直接通信。

### 2. 代理型渠道：依赖目标后端

微信系渠道没有直连通道，全部走 `${endpoint}/v2/backgroundagent/…`：

| 渠道 | 后端端点 |
|---|---|
| **微信小程序** | `POST /v2/backgroundagent/wechatmpProxy/push` |
| 微信客服 | `POST /v2/backgroundagent/wechatkfProxy/{link,bindStatus,bind}` |
| 微信 bot | `/v2/backgroundagent/wechatbotProxy/…` |
| 企微回复 | `POST /v2/backgroundagent/wecom/local-proxy/receive` |

而这里的 `endpoint` **正是 `wbmux` 切换的那个字段**：

```js
getEndpoint() { return this.productManager.getEndpoint().replace(/\/+$/, ""); }
```

`BgAgentApiClient.getEndpoint()` 和 `resolveApiContext()` 都取自
`productManager.getEndpoint()`。所以这些请求会打到目标后端的域名上。

**结论：用国内版宿主连国际后端时，"小程序控制"会失效。**
调用会打到 `https://www.workbuddy.ai/v2/backgroundagent/wechatmpProxy/push`，
而国际后端大概率没有这个服务。

### 3. 更糟的是：入口还会显示

渠道入口的可见性由 `productFeatures` 里的 `ChannelSlack` / `ChannelDiscord` /
`ChannelTelegram` / `ChannelWechatKf` 决定，而这块**整块透传宿主**。于是：

| 宿主 | 目标后端 | 微信系入口 | 实际能否用 |
|---|---|---|---|
| 国内版 | 国内 | 显示 | 能用 |
| 国内版 | **国际** | **仍然显示** | **用不了**（后端无对应服务） |
| 国际版 | 国际 | 隐藏 | —— |
| 国际版 | 国内 | 隐藏 | 本可用，但入口被藏住了 |

第一行和第二行是问题所在：**看得见、点不动**。
这比"入口被隐藏"更糟——用户会以为功能坏了。

顺带说明，`productFeatures` 里的 `Channel*` 开关其实是厂商自己表达
"这个后端支持哪些渠道"的方式：国内版开微信系、关 Slack/Discord/Telegram，
国际版正好相反。`wbmux` 整块透传后，这套开关就与后端脱钩了。

### 4. 怎么用

- **只用国际后端**：用国际版安装做宿主，Slack / Discord / Telegram 正常。
- **只用国内后端**：用国内版安装做宿主，微信系正常。
- **要来回切**：接受"切到国际后端时微信系不可用"。
  这是当前版本的已知限制，不是配置能绕过的。
- 注意 `MobileConnectAppOnly`（国际版 `true`）还会隐藏"连接移动端"面板里的
  小程序 Tab；`DisableAutomationWechatMiniProgramPush`（国际版 `true`）
  隐藏自动化任务里的"推送到小程序"开关。这两个也随宿主走。

### 5. 未实测的部分

**"国际后端到底实现了哪些端点"我没有实测。** 上面的"用不了"是由
（a）代码里 `endpoint` 的来源、（b）国际版 `product.json` 显式关闭小程序入口
这两条**推导**出来的，没有真实登录 + 抓包验证。
欢迎有条件的用户实测后反馈。

另有一个仅影响企业账号的治理缺口：`EnableEnterpriseLicenseCheck` 国内为
`true`、国际缺失，企业渠道管控 `GET /v2/enterprises/{id}/claw/control` 按
`endpoint` 走（且 fail-open）。国内企业账号若用国际宿主连国际后端，
该管控会静默失效。此项同为推导，未实测。

## 会不会有法律风险？

`wbmux` 不分发任何腾讯的产物。它只在运行时读你自己安装里的配置文件，
生成一份改动过的副本放在你自己的用户目录下。

不过它依赖客户端未公开的配置加载行为，这可能与软件许可条款存在张力。
请自行确认符合你所在地区的法律法规与软件许可条款。

## 为什么不用现成的账号切换工具？

社区方案走的是"登录态互换"路线——切换数据目录或替换会话文件。
`wbmux` 走的是配置覆盖路线，更轻：不改安装、不动会话文件、不碰注册表
（只读）。

两者解决的问题也不完全一样。那些工具是"切换账号"，`wbmux` 是
"用一份安装连两个后端"。

## 能同步两个后端的历史记录吗？

不能，也不打算做。

两个后端对应不同账号体系（国内受《个人信息保护法》约束，国际受 GDPR 约束），
会话数据无法跨后端复用。手动搬运会话文件技术上可行，但两边的内核版本
并不一致（实测国内 2.147.0 / 国际 2.137.1），风险自负。

## 为什么二进制是 Go 写的单文件？

零运行时依赖。下载解压即用，不需要装 Node、Python 或任何东西。
目前 `go.mod` 里没有任何第三方依赖。

## 支持 macOS / Linux 吗？

代码已按平台分支编写并通过交叉编译，但**只在 Windows 上做过真机验证**。
macOS / Linux 上的实际行为未经验证，欢迎反馈。

## `~/.wbmux/` 里存了什么？

```
~/.wbmux/
├── config.json          你的设置（宿主档位、主程序路径、附加端点）
└── generated/           生成的合并配置
    ├── cn-to-intl.json
    └── intl-to-cn.json
```

生成的配置可以随时删掉，下次启动会重建。`config.json` 删掉即恢复自动探测。
