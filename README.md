# wbmux

**用一份安装，连两套后端。** WorkBuddy 国内版 / 国际版后端复用器。

```
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
| **`productFeatures` 整块透传，不随后端改写** | 里面 124 / 132 项大多与后端无关（`ImageGen`、`BrowserUse`、知识库等），按后端改写会误伤一片。代价是"远程控制"的渠道入口跟随宿主安装而非后端——见 [FAQ](docs/FAQ.md) |
| **数据目录跟随目标后端** | 两套后端账号体系互不相通，共用 profile 会互相冲掉登录态 |
| **不修改安装目录** | 只设环境变量，出问题删掉快捷方式即回到原状 |
| **内置机制自检** | 依赖未公开机制，官方改版可能静默失效，必须能主动发现 |
| **Go 单二进制，零第三方依赖** | 下载即用；`go.mod` 里没有任何 `require` |

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
wbmux list                 # 列出本机探测到的安装与可用后端
wbmux doctor               # 体检：安装位置、配置、覆盖机制是否仍有效
wbmux run intl             # 用国际后端启动
wbmux run cn               # 用国内后端启动
wbmux run intl --dry-run   # 只打印将要执行的内容，不启动
wbmux run intl --native    # 用国际版自己的安装原生启动，作对照
wbmux export intl -o x.json  # 只生成合并配置，不启动
```

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
生成配置的逐字段比对）。

尚未验证：macOS 与 Linux 上的实际行为（代码已按平台分支编写并通过交叉编译，
但未实机运行）；以及"启动后的客户端确实在跟目标后端通信"这一步
（配置内容与环境变量注入已确认正确，但没有完成一次真实登录）。
详见 [docs/EVIDENCE.md](docs/EVIDENCE.md) 第 6 节。

已确认的一项设计限制："远程控制"（IM 渠道远程下发任务）功能本身不受切换影响，
但其**渠道入口跟随宿主安装而非目标后端**——用国内版安装连国际后端时，
Slack / Discord / Telegram 的入口仍被隐藏。依据与取舍见
[docs/EVIDENCE.md](docs/EVIDENCE.md) 第 8 节与 [FAQ](docs/FAQ.md)。

## 免责声明

本项目是**非官方的第三方工具**，与 WorkBuddy、CodeBuddy、腾讯及其关联方
不存在隶属、授权或背书关系。

它依赖客户端未公开的配置加载行为。该行为可能在任何版本中变化或移除。
项目提供 `wbmux doctor` 主动检测这种失效，但不做任何兼容性承诺。

使用前请自行确认符合你所在地区的法律法规与软件许可条款。

## 许可

[MIT](LICENSE)
