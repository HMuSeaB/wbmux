#!/usr/bin/env python3
"""只读地查客户端的 workbuddy.db —— 不碰它的 -wal / -shm。

用途
----
wbmux 需要读客户端的数据目录来了解额度消耗、会话索引等。但客户端正在跑时，
数据库旁边会有 `-wal` 与 `-shm`；用普通方式打开会去读写这两个文件，
既可能失败（"unable to open database file"），也属于动别人的活动状态——
不该做。

`?immutable=1` 告诉 SQLite「这个库是只读的、不会变」，它就会**完全跳过
WAL 文件**。实测两侧都能读，客户端照常运行不受影响。

代价：拿到的可能是一个**略微陈旧的快照**（还没从 WAL 落到主库的部分看不到）。
对"看个仪表盘"来说完全够用。

用法
----
    python immutable_read.py tables cn
    python immutable_read.py sql cn "select * from session_usage limit 5"
    python immutable_read.py sql intl "select count(*) from sessions"

（cn / intl 是 wbmux 对两个档位的叫法：cn=国内版，intl=国际版）
"""
import os
import sqlite3
import sys

# 两个档位各自的数据目录名，与 internal/variant 里的一致。
DATA_DIRS = {"cn": ".workbuddy", "intl": ".workbuddy-ai"}

SLASH = chr(92)


def db_path(side):
    if side not in DATA_DIRS:
        raise SystemExit("档位只能是 cn / intl，收到 %r" % side)
    home = os.environ.get("USERPROFILE") or os.path.expanduser("~")
    return os.path.join(home, DATA_DIRS[side], "workbuddy.db")


def connect(side):
    """以 immutable 模式打开，不读写 -wal / -shm。"""
    p = db_path(side).replace(SLASH, "/")
    return sqlite3.connect("file:" + p + "?immutable=1", uri=True)


def cmd_tables(side):
    c = connect(side)
    try:
        for (name,) in c.execute("select name from sqlite_master where type='table' order by name"):
            print("  ", name)
    finally:
        c.close()


def cmd_sql(side, sql):
    c = connect(side)
    try:
        cur = c.execute(sql)
        cols = [d[0] for d in cur.description] if cur.description else []
        if cols:
            print("  " + " | ".join(cols))
            print("  " + "-" * min(72, 4 * len(cols)))
        for row in cur:
            print("  " + " | ".join(str(v)[:40] if v is not None else "-" for v in row))
    finally:
        c.close()


def main(argv):
    if len(argv) < 3:
        print(__doc__)
        return 2
    op, side = argv[1], argv[2]
    if op == "tables":
        cmd_tables(side)
    elif op == "sql":
        if len(argv) < 4:
            print("sql 模式还要给一条 SQL")
            return 2
        cmd_sql(side, argv[3])
    else:
        print(__doc__)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
