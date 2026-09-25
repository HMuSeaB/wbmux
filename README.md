# wbmux

**用一份安装，连两套后端。** WorkBuddy 国内版 / 国际版后端复用器。

```
wbmux gui          # 打开图形界面（双击 wbmux.exe 同此）
wbmux run cn       # 连国内后端  www.workbuddy.cn
wbmux run intl     # 连国际后端  www.workbuddy.ai
```

同一个 `WorkBuddy.exe`，同一份程序文件，重启一次即切换。
安装目录零改动。

---

## 这解决什么问题

WorkBuddy 有国内版和国际版两个发行版。两者是同一个 Electron 应用的两个构建，
但安装目录、数据目录、账号体系全部独立。同时使用的人得装两份：

- 多占约 1.3 GB 安装空间
- 两套自动更新各自下载、各自解压
- 两个桌面图标、两套快捷方式

而它们的**内核是同源的**——实测 Electron 运行时、`resources.pak`、`icudtl.dat`
三者哈希完全一致，主程序 exe 字节数也一致（均为 204,585,000 字节）。
差异集中在产品配置里。

`wbmux` 只做一件事：让同一份程序按你指定的后端启动。

## 原理

客户端在读取产品配置时，**环境变量优先于安装包内自带的 `product.json`**：

```
ACC_PRODUCT_CONFIG_PATH   →  指向一个 product.json 文件路径
ACC_PRODUCT_CONFIG_V3     →  直接内联 JSON（与上面互斥）
```

`wbmux` 读取宿主安装自己的 `product.json`，只覆盖决定后端的少数几个字段
（endpoint、数据目录、区域标志、认证标识），生成一份合并配置，再把它交给客户端。
其余字段——更新通道、遥测、品牌资源——一律保持宿主原样。

这个机制不是猜的，是从发布产物里读出来的。完整过程与实测数据见
[docs/EVIDENCE.md](docs/EVIDENCE.md)。

## 它是怎么找到你的客户端的

客户端可能装在任意位置，`wbmux` 按可靠性分五层依次尝试：

1. **显式配置** — 你用 `--exe` 指定。权威，路径不存在就硬失败，绝不悄悄换一个
2. **数据目录线索** — 客户端注册 Office 文件关联时，会在 `settings.json`
   里写下自己的安装路径
3. **注册表** — 遍历 HKCU / HKLM（含 32/64 位视图）的卸载记录，
   从中取 `DisplayIcon` / `InstallLocation`
4. **常见安装目录** — 环境变量推导的位置，加上各盘根
5. **PATH**

第 3 层是必需的：实测国际版既没写过数据目录线索（它不注册 Office 文件关联），
又装在盘根 `D:\WorkBuddyAI`，前两层都覆盖不到。

## 设计取舍

| 决定 | 理由 |
|---|---|
| **不随包分发官方 `product.json`** | 只内置后端描述符（URL 与标识符），运行时读用户自己的安装。法律干净，且天然跟随官方升级 |
| **最小补丁而非整体替换** | 只改后端字段，其余保持宿主原样，避免版本错配。实测改写 9 处、零字段丢失 |
| **`productFeatures` 整块透传，不随后端改写** | 里面 124 / 132 项大多与后端无关（`ImageGen`、`BrowserUse`、知识库等），按后端改写会误伤一片。代价见下方「已知限制」 |
| **数据目录跟随目标后端** | 两套后端账号体系互不相通，共用 profile 会互相冲掉登录态 |
| **不修改安装目录** | 只设环境变量，出问题删掉快捷方式即回到原状 |
| **内置机制自检** | 依赖未公开机制，官方改版可能静默失效，必须能主动发现 |
| **图形界面内嵌成网页，不引原生窗口库** | 原生窗口库要 cgo 与第三方依赖，会毁掉交叉编译。内嵌网页用标准库即可，仍是单文件分发 |
| **Go 单二进制，零第三方依赖** | 下载即用；`go.mod` 里没有任何 `require` |

## 已知限制

**「远程控制」里的微信系渠道，在切到国际后端后会失效。**

远程控制的渠道分两类（依据见 [docs/EVIDENCE.md](docs/EVIDENCE.md) 第 8 节）：

- **直连型**（Slack / Discord / Telegram / 企微 WebSocket 模式）：客户端直接
  连服务商，切换后端**不受影响**。
- **代理型**（微信小程序 / 微信客服 / 微信 bot / 企微回复）：全部走
  `${endpoint}/v2/backgroundagent/…`，而 `endpoint` 正是被切换的字段。
  国际后端没有这些服务，**会失效**。

更麻烦的是入口仍会显示：渠道开关住在 `productFeatures` 里，该块整块透传宿主，
所以国内版宿主连国际后端时，微信系入口**看得见但点不动**。

**规避方式**：按你主要使用的渠道来选宿主。只用国际后端就用国际版安装做宿主，
只用国内后端就用国内版安装做宿主。要来回切，就得接受上述代价——
这是当前版本的已知限制，不是配置能绕过的。

## 安装

从 [Releases](../../releases/latest) 下载对应平台的压缩包，解压后放到 `PATH` 里即可。

从源码构建：

```bash
git clone https://github.com/HMuSeaB/wbmux.git
cd wbmux
go build ./cmd/wbmux
```

## 用法

```bash
wbmux gui                  # 打开图形界面
wbmux list                 # 列出本机探测到的安装与可用后端
wbmux doctor               # 体检：安装位置、配置、覆盖机制是否仍有效
wbmux run intl             # 用国际后端启动
wbmux run cn               # 用国内后端启动
wbmux run intl --dry-run   # 只打印将要执行的内容，不启动
wbmux run intl --native    # 用国际版自己的安装原生启动，作对照
wbmux export intl -o x.json  # 只生成合并配置，不启动
wbmux migrate                # 看看另一侧有什么历史可以搬过来
wbmux migrate intl cn --yes  # 把国际版的会话搬进国内版
```

### 图形界面

`wbmux gui` 会打开一个本地界面：列出探测到的安装、选目标后端、预览将要改写的
字段、再启动客户端。**双击 `wbmux.exe` 等同于执行本命令。**

界面本身是一份内嵌的网页，由 wbmux 在 `127.0.0.1` 上提供，再用系统浏览器
打开——优先用 Edge / Chrome 的 `--app=` 模式，出来的窗口没有地址栏和标签页，
看起来就是一个本地程序。

几点设计上的说明：

- **只监听回环地址**，不接受 `--addr 0.0.0.0:…`。这个服务能启动本机进程，
  暴露到局域网等于把机器交出去，所以直接拒绝而不是警告放行。
- **一次性令牌**：每次启动随机生成，写进访问地址。任何网页都能向
  `127.0.0.1` 发请求，没有令牌就调不动接口，以此挡住本地 CSRF。
- **空闲自动退出**：页面每 60 秒发一次心跳，标签页关掉后 30 分钟内进程自行结束。
  这是为了不留残留——双击启动时进程没有控制台窗口，否则只能去任务管理器收拾。
- **命令行与界面共用同一条代码路径**（`internal/runner`），避免两边行为漂移。
  尤其是写配置后的回读校验，漏掉就会静默连错后端。

```bash
wbmux gui --addr 127.0.0.1:8080   # 指定端口
wbmux gui --no-open               # 只起服务，自己打开地址
wbmux gui --idle off              # 不自动退出
```

代价是二进制约 6.3 MB（纯命令行版 2.5 MB），多出来的部分是 `net/http`
带进来的 `crypto/tls` 与 `crypto/x509`。压缩后的下载体积从 1.1 MB 涨到 2.6 MB。
零依赖与交叉编译不受影响。

### 历史搬运

两套后端的账号体系不互通、数据目录也不同，所以在国际版里干过的活切回国内版
就看不见。`migrate` 把一侧的会话、技能与记忆搬到另一侧。

```bash
wbmux migrate                       # 只读，列出另一侧有什么可以搬
wbmux migrate intl cn --yes         # 真正执行
wbmux migrate --kind skills --yes   # 只搬技能
wbmux migrate --dry-run             # 预演，明确不做任何改动
```

默认**不加 `--yes` 就只列清单，一个字节都不动**。图形界面里也有同样入口。

搬五类东西：**会话**、**附件**、**内容与配置**、**技能**、**记忆**。

| 类别 | 内容 |
|---|---|
| 会话 | `projects/` 里的正文 + 索引库里的行 |
| 附件 | 会话**实际引用到**的图片（`blobs/`、`clipboard-images/`） |
| 内容与配置 | 身份文件、`settings.json`、`models.json`、MCP、连接器、插件、任务、文件历史 |
| 技能 | `skills/` |
| 记忆 | 账号级记忆文件 |

**设备绑定的东西一律不搬**（`keyblob`、`device-id`、`edge-sync-mapping*.db`），
以及缓存与运行时（`binaries/`、`logs/`、`traces/`）。用白名单而不是整目录复制，
就是为了把"内容"和"设备"分开。

#### 会话里的图片会被改写路径

会话里的图片存的是**绝对路径**，直指来源端数据目录。所以只把图片复制过去没有用
——引用还指着来源端。`migrate` 会把引用到的图片一并搬过来，**并把会话内的路径
改写到目标端数据目录**，这样你删掉来源端数据目录后图依然在。

代价是搬过去的 `.jsonl` **不再与来源逐字节一致**（改写次数会在结果里报出来）。
这是有意的取舍：要"完整"就没法同时"字节一致"。

#### 为什么不能只复制会话文件

会话列表读的是数据目录下 `workbuddy.db` 的 `sessions` 表，不是 `projects/` 目录。
客户端那个"从 `projects/` 重建索引"的函数只在**数据库损坏自愈**时才被调用，
正常启动不会跑。所以只把 `.jsonl` 复制过去，列表里什么都不会出现——必须同时写索引。

#### 它保证不覆盖

目标端已有同名文件、已有同 id 会话行，一律跳过。写索引前先把目标端数据库**整份
备份**（含 `-wal` / `-shm`）。只给"jsonl 确实落在目标目录"的会话写索引，不会造出
打不开的列表项。连跑两次不会产生新增。

如果目标端还没有索引库（从没登录过），会话搬运会**整批中止**而不是留下一堆
搬了文件却没进列表的半成品。

#### 写数据库没有引入任何依赖

Go 标准库没有 SQLite 驱动，而本项目零第三方依赖。解法是借用**客户端自己捆的**
better-sqlite3：Electron 主程序加上 `ELECTRON_RUN_AS_NODE=1` 就是普通 Node 运行时，
再把它的 `better_sqlite3.node` 通过 `nativeBinding` 选项喂给 better-sqlite3 的 JS
封装（绕开打包时留在 `app.asar` 里、拿不到的 `bindings` 模块）。好处是既不新增
依赖，又天然与客户端同一次构建、同 ABI。

搬运会按会话自带的 `cwd` 把它放进目标端对应的工作区目录（目录名是 cwd 的
有损压缩，规则已用本机 11 个真实目录逐一验证）。放不进去的（读不到 `cwd`）
会跳过并说明原因，而不是凭空造一个工作区。

### 体检输出

```
$ wbmux doctor
wbmux 体检
  宿主程序  D:\Tools\WorkBuddy\WorkBuddy.exe
  目标后端  国际版  https://www.workbuddy.ai

检查项
✓ OK   定位宿主安装
    · D:\Tools\WorkBuddy\WorkBuddy.exe（国内版，来源：数据目录线索）
· 信息   宿主版本
    · 5.6.2 (build 37a65c0b)
✓ OK   自带产品配置
    · 53 个顶层字段，endpoint=https://www.workbuddy.cn
✓ OK   数据目录
    · C:\Users\…\.workbuddy
✓ OK   生成配置目录
    · C:\Users\…\.wbmux\generated
✓ OK   配置覆盖机制
    · 在 app.asar 中确认 2 项标记齐备
✓ OK   后端描述符
    · https://www.workbuddy.cn → https://www.workbuddy.ai（国际版）

结论
✓ 全部通过。
```

### 切换预览

```
$ wbmux run intl --dry-run
国内版 → 国际版
  目标后端  国际版  https://www.workbuddy.ai
  宿主程序  国内版  D:\Tools\WorkBuddy\WorkBuddy.exe
  生成配置  C:\Users\…\.wbmux\generated\cn-to-intl.json
  数据目录  C:\Users\…\.workbuddy-ai

配置改写 9 项
    · endpoint: https://www.workbuddy.cn → https://www.workbuddy.ai
    · dataFolderName: .workbuddy → .workbuddy-ai
    · authentication.attributes.platform: workbuddy → workbuddy-ai
    …

将要执行
  命令行    D:\Tools\WorkBuddy\WorkBuddy.exe --user-data-dir=C:\Users\…\.workbuddy-ai
  环境变量  ACC_PRODUCT_CONFIG_PATH=C:\Users\…\.wbmux\generated\cn-to-intl.json
```

## 文档

- [设计说明](docs/DESIGN.md) — 机制、字段取舍、探测策略、为什么这么选
- [证据](docs/EVIDENCE.md) — 机制怎么发现的、实测数据、**以及哪些部分尚未验证**
- [常见问题](docs/FAQ.md) — 登录态、升级、排错、法律风险

## 状态

可用，已在 Windows 上真机验证通过（`list` / `doctor` / `run --dry-run` /
生成配置的逐字段比对，以及图形界面的完整交互路径）。

尚未验证：macOS 与 Linux 上的实际行为（代码已按平台分支编写并通过交叉编译，
但未实机运行）；以及"启动后的客户端确实在跟目标后端通信"这一步
（配置内容与环境变量注入已确认正确，但没有完成一次真实登录）。
详见 [docs/EVIDENCE.md](docs/EVIDENCE.md) 第 6 节。

已确认的一项设计限制："远程控制"的微信系渠道（小程序 / 微信客服 / 微信 bot）
在切到国际后端后会失效，且入口仍显示——见上方[已知限制](#已知限制)与
[docs/EVIDENCE.md](docs/EVIDENCE.md) 第 8 节。Slack / Discord / Telegram
等直连型渠道不受影响。

## 免责声明

本项目是**非官方的第三方工具**，与 WorkBuddy、CodeBuddy、腾讯及其关联方
不存在隶属、授权或背书关系。

它依赖客户端未公开的配置加载行为。该行为可能在任何版本中变化或移除。
项目提供 `wbmux doctor` 主动检测这种失效，但不做任何兼容性承诺。

使用前请自行确认符合你所在地区的法律法规与软件许可条款。

## 许可

[MIT](LICENSE)
