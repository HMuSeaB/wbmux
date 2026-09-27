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
| `go-tmp/` | `clean_gotmp.py` | 清理 `go build` 被中断后泄漏的临时目录 |
| `recycle-noise/` | `main.go` | 查出是"谁"在往回收站塞垃圾，并清掉噪声 |

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

- **`clean_gotmp.py`** —— `go build` 被中断（超时、Ctrl-C、进程被杀）时，
  它放编译中间产物的 `%TEMP%\go-buildNNNNNNNNN` 不会自己清掉。这些残渣被
  Windows 的存储感知扫进**回收站**，堆到几千项之后清理回收站直接卡死。
  根治办法是把 `GOTMPDIR` 指到固定位置（脚本 docstring 里有），这个脚本
  负责按特征清掉已泄漏的那些——只认 `_pkg_.a` / `importcfg` 这类标志，
  不碰 `%LOCALAPPDATA%\go-build`（那是构建缓存，删了只会变慢）。

- **`recycle-noise/`** —— 回收站被灌满的真正原因不是体积而是**条目数**：
  每个条目只有几字节到几十字节，但堆到几千个之后，资源管理器要逐个读
  元数据加载列表，直接卡死。读回收站的 `$I` 元数据（含"原路径"字段）
  按原路径聚合，直接告诉你是**谁**在塞。
  实测主要来源是工具和应用框架自己的临时树：
  - 一次被中断的 `go build` 会在回收站里炸出上百条（它下面的每个子目录
    各自成为一条记录，实测平均 77 条、最多 245 条）
  - 一次 `go test ./...` 能炸出上千条（`t.TempDir()` 铺的假安装树）
  - Shell 每次启动写的策略探测脚本、工具每次调用建的随机名目录

  这一项**有界面**：面板里的「回收站清理」页用的就是同一个包
  （`internal/recyclenoise`），还带"每 30 分钟自动清噪声"的开关。
  这里的命令行入口是给"界面没开着"时用的。

  ```bash
  go run ./tools/recycle-noise          # 报告
  go run ./tools/recycle-noise clean -all   # 连用户文件一起清
  ```

  > 曾有一版 Python 脚本做同样的事，已删。原因有两个：模式表是抄的，
  > 两边很快漂了（面板能认出的它认不出）；清理上千个条目要 10 分钟以上，
  > 而 Go 版同样的量级只花 3 秒，差两百倍。

## 约定

- 只用标准库，不引入任何第三方依赖
- 脚本顶部的 docstring 必须写清「解决哪个具体问题」，而不只是「做什么」
- 注释一律中文，解释**为什么**而不是复述代码
