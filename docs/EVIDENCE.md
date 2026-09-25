# 证据

这份文档记录 `wbmux` 所依赖的每一个事实，以及它是怎么被验证的。
目的是让任何人都能独立复核，而不是只能相信 README 里的说法。

测量环境：Windows，国内版装在 `D:\Tools\WorkBuddy`，
国际版装在 `D:\WorkBuddyAI`。

## 1. 两份安装高度重合

### 字节级相同的部分

| 文件 | 校验 |
|---|---|
| Electron 运行时 | `37.10.3-24`（两版本一致） |
| `resources/resources.pak` | md5 `eb2d3eb4dcc925e4cf15fed6ce9063d9` |
| `resources/icudtl.dat` | md5 `4171402e385f07007de57f45ff540e9a` |
| 主程序 exe | 均为 `204,585,000` 字节 |

也就是说，占安装体积大头的那几样东西是**逐字节相同**的。

### 版本

| | 国内版 | 国际版 |
|---|---|---|
| 产品版本 | 5.6.2 | 5.5.2 |
| 构建号 | `37a65c0b…` | `910352f0…` |
| 内核版本 | 2.147.0 | 2.137.1 |
| `app.asar` | 317 MB（302.4 MiB） | 296 MB（282.8 MiB） |
| 自带 `product.json` | 385,413 字节 / 53 个顶层字段 | 370,961 字节 / 56 个顶层字段 |

结论：**不是"一个构建配置了两次"，而是两个独立构建**。因此不能靠改一个
构建的配置来"变成"另一个产品，只能靠外部覆盖后端指向。

### 差异集中在哪

拆开 `app.asar` 对比，差异只在少数文件上：

| 路径 | 国内版 | 国际版 |
|---|---|---|
| `/cli/dist/codebuddy.js` | 无 | 23.5 MB |
| `/cli/dist/codebuddy-lite-wb.mjs` | 11.3 MB | 无 |
| `/cli/dist/web-ui/` | 无 | 有 |
| `/main/handlers.js` | 707 KB | 2.6 KB |

## 2. 配置覆盖机制

### 怎么发现的

在发布产物里搜索环境变量名：

```
grep -a "ACC_PRODUCT_CONFIG_PATH" <安装目录>/resources/app.asar
```

命中后，从 `app.asar` 里取出 `workbuddy-product-config` 相关的源码片段，
读到解析顺序：

```js
const resolvedProductConfigPathEnv =
    productConfigPathEnv ?? processEnv["ACC_PRODUCT_CONFIG_PATH"]
    ?? fallbackProductConfigPathEnv;
const resolvedProductConfigEnv =
    productConfigEnv
    ?? (resolvedProductConfigPathEnv ? void 0 : processEnv.ACC_PRODUCT_CONFIG_V3)
    ?? fallbackProductConfigEnv;
```

以及一行显式声明两者互斥的常量：

```js
var MUTUALLY_EXCLUSIVE_ENV_GROUPS = [["ACC_PRODUCT_CONFIG_PATH", "ACC_PRODUCT_CONFIG_V3"]];
```

读法：**先看 `ACC_PRODUCT_CONFIG_PATH`，有值就完全忽略内联变量，
两者都为空才回退到安装包内自带的 `product.json`。**

`wbmux` 走第一条通道。虽然解析逻辑本身已经保证互斥，但 `wbmux` 仍会主动
把 `ACC_PRODUCT_CONFIG_V3` / `_V2` / `ACC_PRODUCT_CONFIG` 从子进程环境里
剥掉，以免残留值在将来某个版本里反过来压过我们的设置。

### 机制是否仍然有效：自检

`wbmux doctor` 直接扫 `app.asar`，确认上述两个标记仍在：

```
✓ OK   配置覆盖机制
    · 在 app.asar 中确认 2 项标记齐备
```

扫描采用流式分块（4 MiB），块间保留 `maxLen-1` 字节重叠，
避免标记恰好跨块时漏检。实测耗时：

| 目标 | 大小 | 耗时 |
|---|---|---|
| 国内版 `app.asar` | 302.4 MiB | 143 ms |
| 国际版 `app.asar` | 282.8 MiB | 210 ms |

对照实验：换一个不存在的标记去扫，正确报告"未找到"，无误报。

## 3. 安装位置探测

### 数据目录线索只对国内版有效

客户端的 `settings.json` 里有一条自述安装位置的标记
（在把自己注册为 Office 文件关联时写下）：

| 数据目录 | `officeFileAssociationsRepairMarker` |
|---|---|
| `~/.workbuddy`（国内版） | `v3:win32:5.6.2:D:\Tools\WorkBuddy\WorkBuddy.exe:WorkBuddy:aac,avif,bmp,…` |
| `~/.workbuddy-ai`（国际版） | **不存在** |

国际版的 `settings.json` 只有 `sandbox`、`claw`、`enabledPlugins` 三个键。
原因很可能是国际版不注册 Office 文件关联。

**因此这条线索只能定位国内版。** 只靠它，国际版会探测失败。

### 注册表里两条记录都在

遍历 `…\CurrentVersion\Uninstall`：

| 位置 | 子键数 | 命中 |
|---|---|---|
| `HKLM`（64 位视图） | 147 | 0 |
| `HKLM`（32 位视图） | 229 | 0 |
| `HKCU` | 38 | **2** |

命中的两条：

```
HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{BFD312E9-1019-4F57-9F44-F86246833B50}
    DisplayName     = "WorkBuddy 5.6.2"
    DisplayIcon     = "D:\Tools\WorkBuddy\WorkBuddy.exe,0"
    Publisher       = "Tencent Technology (Shenzhen) Company Limited"
    UninstallString = '"D:\Tools\WorkBuddy\Uninstall WorkBuddy.exe" /currentuser'

HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{C02C88CB-5DAE-4330-AEC1-572CFD048A59}
    DisplayName     = "WorkBuddy AI 5.5.2"
    DisplayIcon     = "D:\WorkBuddyAI\WorkBuddyAI.exe,0"
    Publisher       = "Tencent Technology (Shenzhen) Company Limited"
    UninstallString = '"D:\WorkBuddyAI\Uninstall WorkBuddyAI.exe" /currentuser'
```

两点值得注意：

- **都在 HKCU，不在 HKLM。** 安装器默认按当前用户安装
  （`UninstallString` 带 `/currentuser`），因此只查 HKLM 会一无所获。
- **`InstallLocation` 为空**，可用的路径在 `DisplayIcon` 里（形如
  `路径,图标序号`）。

`wbmux` 因此同时查 HKCU 与 HKLM（含 32/64 位视图），并优先从
`DisplayIcon` 取路径。

### 产品名匹配的陷阱

`WorkBuddy` 是 `WorkBuddy AI` 的前缀。若用前缀匹配判断"这条记录属于哪个产品"，
`WorkBuddy AI 5.5.2` 会被算作国内版。

`wbmux` 要求匹配之后紧跟版本号或括号说明，因此：

| DisplayName | 对 `WorkBuddy` | 对 `WorkBuddy AI` |
|---|---|---|
| `WorkBuddy 5.6.2` | ✓ | ✗ |
| `WorkBuddy AI 5.5.2` | ✗ | ✓ |

同理，路径推断按特征串**从长到短**匹配，因为国内版主程序名
`WorkBuddy` 是国际版 `WorkBuddyAI` 的子串。

## 4. 补丁结果实测

对**真实**的官方配置跑补丁，逐字段比对：

### 国内版 → 国际版

| 指标 | 结果 |
|---|---|
| 顶层字段数 | 53 → 54 |
| 新增字段 | 仅 `isOversea` |
| 丢失字段 | **0** |
| 值发生变化的顶层字段 | `endpoint`、`stagingEndpoint`、`officialEndpoints`、`authentication`、`dataFolderName`（共 5 个，展开为 9 处改写） |

### 国际版 → 国内版

| 指标 | 结果 |
|---|---|
| 顶层字段数 | 56 → 55 |
| 新增字段 | 0 |
| 丢失字段 | 仅 `isOversea`（切国内版时**删除**该字段，而非置 `false`） |

### 未触碰字段抽查

以下字段在补丁前后完全一致（深度相等）：

- `updates`、`updateUrl`——避免国内版被引导去拉国际版安装包
- `smhHost`——区域性安全遥测端点
- `productName`（仍为 `WorkBuddy`）——宿主身份
- `models`

### 数字精度

配置里存在超过 2^53 的整数标识符。若用默认的 `float64` 解码，
重新序列化时会变成科学计数法并可能丢精度。

`wbmux` 解码时启用 `UseNumber()`，并在测试中用
`9007199254740993`（2^53+1）做回归断言，同时检查输出文本里
不出现 `e+` 形式的科学计数法。

## 5. 端到端验证

```
$ wbmux list
  国内版    D:\Tools\WorkBuddy\WorkBuddy.exe
    · 来源  数据目录线索
    · 版本  5.6.2  build 37a65c0b
  国际版    D:\WorkBuddyAI\WorkBuddyAI.exe
    · 来源  注册表
    · 版本  5.5.2  build 910352f0

$ wbmux doctor
✓ OK   定位宿主安装
✓ OK   自带产品配置      · 53 个顶层字段，endpoint=https://www.workbuddy.cn
✓ OK   数据目录
✓ OK   生成配置目录
✓ OK   配置覆盖机制      · 在 app.asar 中确认 2 项标记齐备
✓ OK   后端描述符
结论：全部通过。

$ wbmux run intl --dry-run
国内版 → 国际版
  生成配置  C:\Users\…\.wbmux\generated\cn-to-intl.json
  数据目录  C:\Users\…\.workbuddy-ai
配置改写 9 项
将要执行
  命令行    D:\Tools\WorkBuddy\WorkBuddy.exe --user-data-dir=C:\Users\…\.workbuddy-ai
  环境变量  ACC_PRODUCT_CONFIG_PATH=C:\Users\…\.wbmux\generated\cn-to-intl.json
```

交叉编译验证：`windows/amd64`、`windows/386`、`darwin/amd64`、
`darwin/arm64`、`linux/amd64`、`linux/arm64` 六种目标全部构建通过。

## 6. 未验证的部分

诚实起见，以下内容**没有**经过真机验证，不应被当作已确认的事实：

- **macOS / Linux 上的实际行为**。探测逻辑已按平台分支编写，但只在
  Windows 上跑过真机。`/Applications`、`/opt` 等路径的假设来自惯例，未实测。
- **客户端真正连上目标后端**。`wbmux` 已确认生成的配置内容正确、
  环境变量注入正确、进程能启动，但"启动后的客户端确实在跟
  `www.workbuddy.ai` 通信"这一步需要抓包或看客户端界面才能确认。
- **登录流程是否完全正常**。已把 `authentication.id`、
  `.attributes.platform` 和四个域名列表一并改写（这些字段缺失会导致登录
  被导向原后端），但没有完成一次真实登录。
- **官方后续版本是否仍保留该机制**。这正是 `wbmux doctor` 存在的意义。

## 7. 同类项目调查

在动手前调查过 GitHub 上的现成方案。结论：

- 社区方案**全部**走"登录态互换"路线（切换数据目录 / 替换会话文件），
  没有项目使用配置覆盖通道。在若干相关仓库里搜索
  `ACC_PRODUCT_CONFIG_PATH`、`ACC_PRODUCT_CONFIG`、`isOversea`，
  命中数均为 0。
- 因此 `wbmux` 的路线与现有项目不重叠。它更轻（不改安装、不动会话文件），
  但也更依赖未公开机制，故必须自带自检。

调查中另有一类风险需要提醒：某些同类项目只提供 README、
二进制通过群文件分发。这类产物无法审计，不建议使用。

## 8. "远程控制"（claw 渠道）在切换后端后的行为

这一节是对 FAQ「切换后"远程控制"还能用吗？」的证据支撑。

### 它是什么

`claw` 是远程控制的内部代号：绑定 IM 渠道（微信客服 / 企业微信 / QQ / 飞书 /
钉钉 / 元宝 / Slack / Discord / Telegram 等），从 IM 侧给本机 Agent 下发任务。
代码注释（`ProductFeature["RemoteControl"]`）：

> 远程控制功能总开关。默认启用；仅当显式设置为 false 时才禁用
> `/remote-control` 命令、Web UI 远程控制入口及相关渠道管理 API。

注意"默认启用"这一语义——**未配置即为开**。实测两套 `product.json` 的
`productFeatures` 里都没有显式设置 `RemoteControl`，因此两侧功能都是开着的。

### 连接器代码两侧完全相同

从两套 `app.asar` 中提取渠道类型定义：

```
国内版  CLAW_CHANNEL_TYPES = ["feishu","wecomaibot","qq","dingtalk","yuanbao",
        "weixinClawBot","wecomIOA","wechatkf","slack","discord","wecomNew",
        "custom","wechatmp","telegram","mobileApp"]        # 15 项
国际版  CLAW_CHANNEL_TYPES = [……同上……,"telegram"]          # 14 项
```

唯一差别是国内版多一个 `mobileApp`。其余 14 个渠道两边都实现了。
渠道字符串计数也一致（`claw.channel.` 196 / 197，各具体渠道均 6 / 6）。

### 渠道是客户端主动外连，不经过 WorkBuddy 后端

两套 asar 中出现的 WebSocket 主机**完全一致**，且都是 IM 服务商的官方地址：

| 主机 | 国内版 | 国际版 | 归属 |
|---|---|---|---|
| `wss://openws.work.weixin.qq.com` | 13 | 13 | 企业微信官方 |
| `wss://gateway.discord.gg` | 1 | 1 | Discord 官方 |
| `wss://bot-wss.yuanbao.tencent.com` | 1 | 1 | 腾讯元宝 |

`product.json` 里没有任何 `ws://` / `wss://` 地址，说明中继主机不是从后端配置
下发的。渠道注册走的是 Electron 主进程与渲染进程之间的本地 RPC
（`CLAW_RPC_CHANNELS`：`claw:registerChannel`、`claw:unregisterChannel`、
`claw:getChannelStatus` 等），不是后端 HTTP。

### 唯一的后端调用是 fail-open 的

```
CLAW_CONTROL_PATH_TEMPLATE = "/v2/enterprises/{enterpriseId}/claw/control"
```

代码注释：

> 启用后，登录的企业账号（`account.enterpriseId` 非空）会每 5 分钟查询一次
> GET /v2/enterprises/:enterpriseId/claw/control …… 字段缺失 / 非企业 /
> 任何 HTTP / 解析 / 网络异常一律 fail-open（masterEnabled=true、channels 空）

即接错后端时这条管控查不到，功能**保持开启**。

### 但渠道入口可见性跟随宿主安装

`ChannelSlack` / `ChannelDiscord` / `ChannelTelegram` / `ChannelWechatKf`
住在 `productFeatures` 里，而该块被整块透传。实测（国内宿主 → 国际后端）：

```
源 productFeatures 124 项 → 产物 124 项，深度相等 == True

产物中：ChannelSlack=False  ChannelDiscord=False  ChannelTelegram=False
        ChannelWechatKf=True  EnableClawChannelControl=True
对照（国际后端本该用的值）：
        ChannelSlack=True   ChannelDiscord=True   ChannelTelegram=True
        ChannelWechatKf=False
```

所以切到国际后端后，Slack / Discord / Telegram 的**入口仍被隐藏**。
底层能力在，只是 feature flag 没跟着后端走。

这是**有意的取舍**而非疏漏：`productFeatures` 里另有 `ImageGen`、
`BrowserUse`、`ComputerUse`、`TencentDocsKnowledge`、`ImaKnowledge` 等
大量与后端无关的开关（两侧共 23 + 31 项不对称），整块按后端改写会误伤它们。
而渠道跟用户的 IM 账号和地区更相关，跟模型后端无关——
用微信的人即使偶尔切到国际后端，多半还是想用微信渠道。

### 治理缺口（仅企业账号）

`EnableEnterpriseLicenseCheck` 国内为 `true`、国际缺失。企业渠道管控按
`endpoint` 走，故国内企业账号若用国际宿主连国际后端，该管控 fail-open。
个人账号无影响。此项**未实测**，由代码路径推导。
