# tools/

**开发与排障用的辅助脚本，不属于产品构建链。**

wbmux 自身是零第三方依赖的 Go 程序，这里的脚本不会参与构建、也不会被
打进发布的二进制。它们的作用是：把排查某类问题时用到的手段留下来，
下次遇到同类问题可以直接复用，而不是从零重写。

每个脚本都自带 docstring，说明**它是为了解决哪个具体问题才存在的**。

| 目录 | 脚本 | 解决什么 |
|---|---|---|
| `windows/` | `proc_dll_probe.py` | 确认 Win32 API 属于哪个 DLL |
| `macos/` | `macos_uuid.py` | 检查 Mach-O 有没有 LC_UUID |
| `image/` | `png_crop.py` | 纯 Python 裁剪 PNG（不装 Pillow） |
| `sqlite/` | `immutable_read.py` | 只读查客户端数据库，不碰 `-wal`/`-shm` |

## 为什么每一类都值得留下

- **`proc_dll_probe.py`** —— Go 的 `syscall.LazyProc` 找不到函数会
  **直接 panic 而非返回错误**。wbmux 把 `ShowWindow` 挂在 kernel32 上，
  导致双击启动时必崩，而 panic 信息正好被写进刚隐藏的控制台，排查了很久。
  仓库里现在有永久的 `TestProcsResolve` 回归，独立跑这个脚本则适合
  在写进代码之前先确认某个 API 在哪。

- **`macos_uuid.py`** —— macOS 26 的 dyld 要求 LC_UUID，Go 1.22 的链接器
  不写。现象只在 macOS 出现，还长期被构建缓存掩盖。有了它，在 Windows 上
  就能判定 macOS 产物，不必等 CI。

- **`png_crop.py`** —— 界面验收靠截图，而整页截图太高、缩放后字看不清。
  环境没有 Pillow，也不该为看图装依赖。

- **`immutable_read.py`** —— 客户端运行时数据库旁有 `-wal`/`-shm`，普通
  打开会去读写它们。`?immutable=1` 让 SQLite 完全跳过 WAL 文件，两侧都能读。
  这是做额度仪表盘的关键前提。

## 约定

- 只用标准库，不引入任何第三方依赖
- 脚本顶部的 docstring 必须写清「解决哪个具体问题」，而不只是「做什么」
- 注释一律中文，解释**为什么**而不是复述代码
