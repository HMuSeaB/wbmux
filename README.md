# wbmux

**用一份安装，连两套后端。** WorkBuddy 国内版 / 国际版后端复用器。

```
wbmux run cn       # 连国内后端  www.workbuddy.cn
wbmux run intl     # 连国际后端  www.workbuddy.ai
```

同一个 `WorkBuddy.exe`，同一个数据盘上的程序文件，重启一次即切换。
安装目录零改动。

---

## 这解决什么问题

WorkBuddy 有国内版和国际版两个发行版。两者是同一个 Electron 应用的两个构建，
但安装目录、数据目录、账号体系全部独立。同时使用的人得装两份：

- 多占约 1.3 GB 安装空间
- 两套自动更新各自下载、各自解压
- 两个桌面图标、两套快捷方式

而它们的**内核是同源的**（实测 Electron 运行时、`resources.pak`、`icudtl.dat`
三者哈希完全一致，主程序 exe 字节数也一致）。差异集中在产品配置里。

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

详见 [docs/DESIGN.md](docs/DESIGN.md) 与 [docs/EVIDENCE.md](docs/EVIDENCE.md)。

## 设计取舍

| 决定 | 理由 |
|---|---|
| **不随包分发官方 `product.json`** | 只内置后端描述符（URL 与标识符），运行时读用户自己的安装。法律干净，且天然跟随官方升级 |
| **最小补丁而非整体替换** | 只改后端字段，其余保持宿主原样，避免版本错配 |
| **不修改安装目录** | 只设环境变量，出问题删掉快捷方式即回到原状 |
| **内置机制自检** | 依赖未公开机制，官方改版可能静默失效，必须能主动发现 |
| **Go 单二进制** | 零运行时依赖，非技术用户下载即用 |

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
wbmux doctor              # 体检：安装位置、配置、机制是否仍有效
wbmux list                # 列出本机探测到的安装与可用后端
wbmux run intl            # 用国际后端启动
wbmux run cn              # 用国内后端启动
wbmux run intl --dry-run  # 只打印将要执行的内容
```

## 状态

早期开发中。命令与配置格式可能变化。

## 免责声明

本项目是**非官方的第三方工具**，与 WorkBuddy、CodeBuddy、腾讯及其关联方
不存在隶属、授权或背书关系。

它依赖客户端未公开的配置加载行为。该行为可能在任何版本中变化或移除。
项目提供 `wbmux doctor` 主动检测这种失效，但不做任何兼容性承诺。

使用前请自行确认符合你所在地区的法律法规与软件许可条款。

## 许可

[MIT](LICENSE)
