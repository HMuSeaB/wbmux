  // ---------- 会话工作区 ----------
  // 读与写分得很开：进页面只查询；迁移是两步（先看计划、再确认执行）。
  var wsLast = null;
  var wsPlanOk = false;

  function loadWorkspace() {
    var box = document.getElementById("ws-body");
    if (!box) return;
    clear(box);
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在读工作区…"]));

    api("/api/workspace")
      .then(function (data) {
        wsLast = data;
        renderWorkspace(data);
      })
      .catch(function (e) {
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "读工作区失败：" + e.message }));
      });
  }

  function renderWorkspace(data) {
    var box = document.getElementById("ws-body");
    var ov = (data && data.overview) || {};
    clear(box);

    var running = !!(data && data.running);
    var note = document.getElementById("ws-note");
    if (note) {
      note.innerHTML = running
        ? ICO("cross", "ico-sm") + " 客户端正在运行——迁移按钮已锁住"
        : ICO("check", "ico-sm") + " 客户端未运行，可以执行迁移";
    }
    // 把"能不能执行"与数据一起交给按钮判定，别在几处各写一遍。
    document.getElementById("btn-ws-apply").disabled = !(running === false && wsPlanOk);

    box.appendChild(el("div", { class: "note " + (running ? "warn" : "ok") }, [
      el("b", { text: running ? "客户端正在运行" : "客户端未运行" }),
      el("div", { text: running
        ? "改数据库会被它回写覆盖，所以相关归类与迁移按钮暂时锁定。请先完全退出客户端，再点「刷新现状」。"
        : "现在可以安全地归类会话或迁移工作区。" })
    ]));

    // 1. 散落会话智能收纳抽屉 (未指定工作区的临时时间戳任务)
    var looseSessions = data.looseSessions || [];
    if (looseSessions.length) {
      var looseCard = el("div", {
        style: "margin:14px 0;padding:12px 14px;background:rgba(59,130,246,0.06);border:1px solid rgba(59,130,246,0.25);border-radius:10px"
      });

      var looseHeader = el("div", {
        style: "display:flex;justify-content:space-between;align-items:center;margin-bottom:10px;flex-wrap:wrap;gap:8px"
      }, [
        el("div", { style: "display:flex;align-items:center;gap:6px" }, [
          el("span", { style: "font-size:16px" }, ["💡"]),
          el("span", { style: "font-weight:600;font-size:13px;color:var(--ink)" }, [
            "未指定空间的散落任务 (" + looseSessions.length + " 个)"
          ]),
          pill("快捷收纳", "ok")
        ]),
        el("span", { style: "font-size:11.5px;color:var(--ink-2)" }, [
          "右侧选定目标空间即可一键归入（免手动输入路径）"
        ])
      ]);
      looseCard.appendChild(looseHeader);

      var looseList = el("div", { style: "display:flex;flex-direction:column;gap:8px" });
      looseSessions.forEach(function (sess) {
        var sessRow = el("div", {
          style: "display:flex;padding:9px 12px;background:var(--g1);border:1px solid var(--border);border-radius:8px;align-items:center;gap:12px;flex-wrap:wrap"
        });

        var titleBox = el("div", { style: "flex:1;min-width:200px" }, [
          el("div", { style: "font-weight:600;font-size:13px;color:var(--ink);display:flex;align-items:center;gap:6px" }, [
            el("span", { style: "color:var(--brand)" }, ["📌"]),
            sess.displayTitle
          ]),
          el("div", { class: "path", style: "font-size:11px;color:var(--ink-3);margin-top:2px" }, [
            (sess.timeStr ? sess.timeStr + " · " : "") + sess.cwd
          ])
        ]);

        var selectTarget = el("select", {
          style: "height:30px;font-size:12px;padding:2px 8px;border-radius:6px;background:var(--g0);border:1px solid var(--border);color:var(--ink);max-width:260px"
        });
        selectTarget.appendChild(el("option", { value: "", text: "-- 选择目标空间 (如 SlateTerm) --" }));
        (data.knownTargets || []).forEach(function (tgt) {
          var opt = el("option", { value: tgt.path, text: tgt.name + " (" + tgt.sessions + " 会话)" });
          if (tgt.name.toLowerCase() === "slateterm") {
            opt.selected = true;
          }
          selectTarget.appendChild(opt);
        });

        var btnMove = el("button", {
          class: "primary",
          style: "height:30px;padding:0 12px;font-size:12px;white-space:nowrap",
          disabled: running
        }, [el("span", { text: "归入此空间" })]);

        btnMove.addEventListener("click", function () {
          var targetCwd = selectTarget.value;
          if (!targetCwd) {
            toast("请先选择目标空间（如 SlateTerm）", "warn");
            return;
          }
          if (running) {
            toast("客户端正在运行，请先完全退出客户端再归类", "bad");
            return;
          }
          btnMove.disabled = true;
          btnMove.textContent = "正在归档…";
          api("/api/workspace/move-session", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ sessionId: sess.id, targetCwd: targetCwd })
          }).then(function () {
            var targetName = selectTarget.options[selectTarget.selectedIndex].text.split(" ")[0];
            toast("已成功将「" + sess.displayTitle + "」归入 " + targetName + " 空间！", "ok");
            loadWorkspace();
          }).catch(function (err) {
            toast("归档失败：" + err.message, "bad");
            btnMove.disabled = false;
            btnMove.textContent = "归入此空间";
          });
        });

        sessRow.appendChild(titleBox);
        sessRow.appendChild(selectTarget);
        sessRow.appendChild(btnMove);
        looseList.appendChild(sessRow);
      });

      looseCard.appendChild(looseList);
      box.appendChild(looseCard);
    }

    // 2. 联动更新下方"迁移到别处"的目标下拉框
    var selTo = document.getElementById("ws-to-select");
    if (selTo) {
      clear(selTo);
      selTo.appendChild(el("option", { value: "", text: "-- 选择已有目标工作区 (免手动输入) --" }));
      (data.knownTargets || []).forEach(function (tgt) {
        selTo.appendChild(el("option", { value: tgt.path, text: tgt.name + " (" + tgt.path + ")" }));
      });
      selTo.onchange = function () {
        if (selTo.value) {
          document.getElementById("ws-to").value = selTo.value;
          wsPlanOk = false;
          clear(document.getElementById("ws-plan"));
        }
      };
    }

    // 3. 现状表
    var rows = ov.spaces || [];
    if (!rows.length) {
      box.appendChild(el("div", { class: "hint", style: "margin-top:10px",
        text: "没读到任何工作区。" }));
      return;
    }
    box.appendChild(el("div", { class: "hint", style: "margin:14px 0 6px",
      text: "共 " + rows.length + " 个工作区，会话合计 " + (ov.totalSessions || 0) +
            "；projects 目录 " + (ov.projectDirs || 0) + " 个" }));

    rows.forEach(function (sp) {
      var tags = [];
      var isLoose = sp.path.indexOf("WorkBuddy\\202") > -1 || sp.path.indexOf("WorkBuddy/202") > -1;
      if (isLoose) tags.push(pill("临时时间戳目录", "warn"));
      if (!sp.exists) tags.push(pill("目录不存在", "bad"));
      if (!sp.inList) tags.push(pill("不在清单里", "ghost"));
      if ((sp.variants || []).length > 1) {
        tags.push(pill(sp.variants.length + " 种写法", "warn"));
      }

      var btnSetDst = el("button", {
        style: "height:22px;padding:0 8px;font-size:11px;margin-left:8px;background:var(--g2);border:1px solid var(--border);border-radius:4px;color:var(--ink-2);cursor:pointer",
        title: "将此路径填入下方「到」目标输入框"
      }, [el("span", { text: "选为目标" })]);
      btnSetDst.addEventListener("click", function (ev) {
        ev.stopPropagation();
        document.getElementById("ws-to").value = sp.path;
        wsPlanOk = false;
        clear(document.getElementById("ws-plan"));
        toast("已将目标工作区设为：" + sp.path, "info");
      });

      var row = el("div", { class: "row", style: "margin:2px 0;align-items:center" }, [
        el("span", { class: "path", style: "flex:1;min-width:200px",
          text: sp.path, title: (sp.variants || []).join("\n") || sp.path }),
        el("span", { class: "desc", style: "white-space:nowrap",
          text: "会话 " + (sp.sessions || 0) }),
        btnSetDst,
        tags
      ]);
      // 点一行就把源路径填进下面的输入框——省得手敲长路径。
      row.style.cursor = "pointer";
      row.addEventListener("click", function () {
        document.getElementById("ws-from").value = sp.path;
        wsPlanOk = false;
        clear(document.getElementById("ws-plan"));
      });
      box.appendChild(row);
    });

    // 体检发现
    var probs = ov.problems || [];
    if (probs.length) {
      box.appendChild(el("div", { class: "hint", style: "margin:16px 0 6px",
        text: "体检发现 " + probs.length + " 处：" }));
      probs.forEach(function (pr) {
        var kindText = pr.kind === "missing" ? "目录不存在"
                     : pr.kind === "duplicate" ? "重复入口" : "写法非规范";
        var cls = pr.kind === "missing" ? "bad" : pr.kind === "duplicate" ? "warn" : "info";
        var item = el("div", { class: "desc", style: "margin:3px 0" }, [
          pill(kindText, cls),
          el("span", { text: " " + pr.path + (pr.note ? " — " + pr.note : "") })
        ]);
        if ((pr.variants || []).length) {
          var sub = el("div", { class: "path", style: "margin-left:8px" });
          pr.variants.forEach(function (v) { sub.appendChild(el("div", { text: v })); });
          item.appendChild(sub);
        }
        box.appendChild(item);
      });
      box.appendChild(el("div", { class: "hint", style: "margin-top:6px",
        text: "合并写法：把「从」填成上面前两种写法里的任意一个、" +
              "「到」填规范形式（盘符小写 + 反斜杠），执行后两个入口就并成一个。" }));
    }
  }

  function wsPlan() {
    var from = document.getElementById("ws-from").value.trim();
    var to = document.getElementById("ws-to").value.trim();
    var box = document.getElementById("ws-plan");
    clear(box);
    wsPlanOk = false;
    document.getElementById("btn-ws-apply").disabled = true;

    if (!from || !to) {
      box.appendChild(el("div", { class: "note warn", text: "「从」和「到」都要填。" }));
      return;
    }
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在算…"]));

    api("/api/workspace/plan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ from: from, to: to, kind: "move" })
    }).then(function (data) {
      clear(box);
      box.appendChild(el("div", { class: "note" }, [
        el("b", { text: "将要做的改动" }),
        el("div", { class: "path", style: "margin-top:6px;white-space:pre-wrap",
          text: data.summary || "" })
      ]));
      (data.blockers || []).forEach(function (b) {
        box.appendChild(el("div", { class: "note bad", style: "margin-top:8px", text: b }));
      });
      wsPlanOk = !!data.ready;
      var running = !!(wsLast && wsLast.running);
      document.getElementById("btn-ws-apply").disabled = !(wsPlanOk && !running);
      if (wsPlanOk && !running) {
        box.appendChild(el("div", { class: "hint", style: "margin-top:8px",
          text: "确认无误就点「确认执行」。执行前会自动备份数据库。" }));
      }
    }).catch(function (e) {
      clear(box);
      box.appendChild(el("div", { class: "note bad", text: "算不出来：" + e.message }));
    });
  }

  function wsApply() {
    var from = document.getElementById("ws-from").value.trim();
    var to = document.getElementById("ws-to").value.trim();
    var box = document.getElementById("ws-plan");
    // 最后一道人工确认：这个动作会改历史会话的归属，值得多问一句。
    if (!confirm("确定要把工作区从\n" + from + "\n迁到\n" + to + " 吗？\n\n" +
                 "文件会被复制到新目录（源目录保留），会话记录与正文里的路径会被改写。")) {
      return;
    }
    document.getElementById("btn-ws-apply").disabled = true;
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在迁移…"]));

    api("/api/workspace/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ from: from, to: to, kind: "move", confirm: true })
    }).then(function (data) {
      toast("迁移完成", "ok");
      wsPlanOk = false;
      clear(box);
      if (data.overview) {
        wsLast = { overview: data.overview, running: wsLast ? wsLast.running : false };
        renderWorkspace(wsLast);
      } else {
        loadWorkspace();
      }
    }).catch(function (e) {
      toast("迁移失败：" + e.message, "bad");
      box.appendChild(el("div", { class: "note bad", text: "迁移失败：" + e.message }));
    });
  }

  function loadRecycle() {
    var box = document.getElementById("recycle-body");
    clear(box);
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在读回收站…"]));
    recycleBusy(true);

    api("/api/recycle/scan")
      .then(renderRecycle)
      .catch(function (e) {
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "读回收站失败：" + e.message }));
      })
      .then(function () { recycleBusy(false); });
  }

  // 每档的展示与按钮配置。顺序即危险度从低到高。
  //
  // 为什么做成数据而不是三段写死的 DOM：之前只有"噪声 / 其他"两档，
  // 清完噪声才发现开发草稿根本没被覆盖（用户反馈"清不全"）。
  // 现在加一档就多一行配置，不用重写渲染逻辑。
  var RECYCLE_TIERS = [
    {
      cat: "noise", label: "工具噪声", tone: "warn",
      desc: "每次干活都会重新冒出来的：被中断的构建、测试临时树、" +
            "策略探测文件。可随时清，自动清理也只碰这一档。",
      btn: "清理噪声", btnClass: "primary"
    },
    {
      cat: "scratch", label: "开发草稿", tone: "intl",
      desc: "项目 tmp/dist 目录下的构建产物与临时脚本、系统临时目录里的散落文件。" +
            "确实是垃圾，但删了不可恢复，所以要你自己点。",
      btn: "连草稿一起清", btnClass: "ghost"
    },
    {
      cat: "keep", label: "保留", tone: "",
      desc: "你自己从桌面/文档/下载删掉的东西、wbmux 下载的升级包。" +
            "这些不该由工具替你决定，只有「全部清空」会动。",
      btn: "全部清空", btnClass: "ghost danger"
    }
  ];

  function renderRecycle(data) {
    var box = document.getElementById("recycle-body");
    var s = data.summary || {};
    clear(box);

    if (!s.total) {
      box.appendChild(el("div", { class: "note ok", text: "回收站是空的。" }));
      recycleLast = data;
      refreshRecycleButtons();
      applyAutoState(data.auto);
      return;
    }

    var tiers = {};
    (s.tiers || []).forEach(function (t) { tiers[t.category] = t; });

    RECYCLE_TIERS.forEach(function (cfg) {
      var t = tiers[cfg.cat] || { count: 0, bytes: 0, byKind: [], sample: [] };

      // 保留档不放"清理"按钮：那些是用户自己的文件，不该由工具代劳，
      // 只在最后那个「全部清空」里一并清掉。
      var actions = el("div", { class: "row", style: "margin-top:8px" }, [
        cfg.cat === "keep"
          ? el("button", {
              class: "ghost danger", id: "btn-recycle-nuke", disabled: !t.count,
              title: "清空回收站，包括你自己删掉的文件。只在确定不需要恢复时才用。",
              text: cfg.btn,
              onclick: function () { cleanRecycle("keep"); }
            })
          : el("button", {
              class: cfg.btnClass, id: "btn-recycle-" + cfg.cat, disabled: !t.count,
              text: cfg.btn,
              onclick: function () { cleanRecycle(cfg.cat); }
            })
      ]);

      box.appendChild(el("div", { class: "tile", style: "margin-bottom:12px" }, [
        el("div", { class: "row" }, [
          el("b", { text: cfg.label }),
          el("span", { class: "hint", style: "margin:0",
                       text: t.count + " 项 · " + humanBytes(t.bytes || 0) })
        ]),
        el("p", { class: "desc", style: "margin:6px 0 0", text: cfg.desc }),
        actions
      ]));

      if (t.byKind && t.byKind.length) {
        t.byKind.forEach(function (k) {
          var pct = t.count ? Math.max(2, Math.round(k.count * 100 / t.count)) : 0;
          box.appendChild(el("div", { class: "bar-row" }, [
            el("span", { class: "bar-name", text: k.kind }),
            el("span", { class: "bar-track" }, [
              el("span", { class: "bar-fill " + (cfg.tone || "intl"), style: "width:" + pct + "%" })
            ]),
            el("span", { class: "bar-num", text: k.count + " 项" })
          ]));
        });
      }

      // 体积大的几条列出来，让人知道自己点下去会删掉什么。
      if (t.sample && t.sample.length) {
        var det = el("details", { class: "fold", style: "margin:6px 0 0" });
        det.appendChild(el("summary", {}, ["看看这一档最大的几条"]));
        var inner = el("div", { class: "desc" });
        t.sample.forEach(function (o) {
          inner.appendChild(el("div", { class: "path", text: o.original }));
        });
        det.appendChild(inner);
        box.appendChild(det);
      }
    });

    // 记下这次的数据，交给 refreshRecycleButtons 统一判定按钮状态。
    // 不要在几处各写一遍 disabled，那样总会漏掉一处（这里就漏过）。
    recycleLast = data;
    refreshRecycleButtons();

    document.getElementById("recycle-note").textContent = "共 " + s.total + " 项";
    applyAutoState(data.auto);
  }

  function applyAutoState(auto) {
    if (!auto) return;
    var cb = document.getElementById("recycle-auto");
    if (cb) cb.checked = !!auto.enabled;
    var note = document.getElementById("recycle-auto-state");
    if (note) {
      note.textContent = auto.enabled
        ? "已开启，间隔 " + auto.interval +
          (auto.lastRun ? "；上次清了 " + auto.lastRemoved + " 项" : "；还没跑过")
        : "当前关闭";
    }
  }

  function cleanRecycle(through) {
    // through 是要清到哪一档：noise / scratch / keep。
    var labels = { noise: "清理噪声", scratch: "连草稿一起清", keep: "全部清空" };
    var label = labels[through] || "清理";
    // 越往上越危险，确认一次。清噪声不用问——它只碰反复产生的工具垃圾。
    if (through !== "noise" && !confirm(
        "「" + label + "」会彻底删除回收站里的条目，无法恢复。确定？")) {
      return;
    }
    recycleBusy(true);
    api("/api/recycle/clean", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ through: through })
    }).then(function (data) {
      var r = data.result || {};
      var msg = "已清 " + (r.removed || 0) + " 项";
      if (r.freed) msg += "，约 " + humanBytes(r.freed);
      if (r.failed) msg += "（" + r.failed + " 项失败，多半是被占用）";
      toast(msg, r.failed ? "bad" : "ok");
      renderRecycle(data);
    }).catch(function (e) {
      toast(label + "失败：" + e.message, "bad");
    }).then(function () { recycleBusy(false); });
  }

  function quit() {
    api("/api/quit", { method: "POST" })
      .then(function (r) {
        // headless（只做转发）模式下，关掉这一页**不等于**停服：
        // 注入到国内客户端的模型还指着这个端口。必须如实说，
        // 否则用户会以为"我已经关掉了"，而它其实还在跑。
        if (r && r.stopped === false) {
          document.body.innerHTML =
            '<div class="gone-wrap"><div class="note ok">' +
            '<b>界面已关闭，服务仍在后台运行。</b>' +
            '<div>' + (r.note || "当前是「只做转发」模式；要停服请用托盘右键退出。") + '</div>' +
            '<div style="margin-top:6px">想继续用界面：右键托盘图标 → 打开界面，或重新双击 wbmux.exe。</div>' +
            '</div></div>';
          return;
        }
        document.body.innerHTML =
          '<div class="gone-wrap"><div class="note ok">界面已关闭，可以关掉这个标签页了。</div></div>';
      })
      .catch(function (e) { toast(e.message, "bad"); });
  }

  // ---------- 历史搬运 ----------

  var migFrom = null;     // 来源档位
  var migTo = null;       // 目标档位
  var migSurvey = null;   // /api/migrate/survey 的结果
  var migPicked = {};     // 勾选表，键形如 "sessions:<会话id>"

  function migKey(kind, id) { return kind + ":" + id; }

  function backendById(id) {
    if (!state) return null;
    for (var i = 0; i < state.backends.length; i++) {
      if (state.backends[i].id === id) return state.backends[i];
    }
    return null;
  }

  function migTitle() {
    var a = backendById(migFrom), b = backendById(migTo);
    return (a ? a.displayName : migFrom) + " → " + (b ? b.displayName : migTo);
  }

  function migGroups(sv) {
    return [
      { kind: "sessions", label: "会话", list: sv.sessions || [] },
      { kind: "assets", label: "附件（会话里引用到的图片）", list: sv.assets || [] },
      { kind: "content", label: "内容与配置", list: sv.content || [] },
      { kind: "skills", label: "技能", list: sv.skills || [] },
      { kind: "memory", label: "记忆", list: sv.memory || [] }
    ];
  }

  function humanSize(n) {
    if (!n) return "";
    if (n < 1024) return n + " B";
    var units = ["KB", "MB", "GB", "TB"], i = -1, v = n;
    do { v /= 1024; i++; } while (v >= 1024 && i < units.length - 1);
    return v.toFixed(1) + " " + units[i];
  }

  function loadMigrate() {
    var box = document.getElementById("mig-body");
    document.getElementById("mig-direction").textContent = migTitle();
    clear(box);
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在扫描两侧数据目录，第一次会比较慢…"]));

    return api("/api/migrate/survey?from=" + encodeURIComponent(migFrom) + "&to=" + encodeURIComponent(migTo))
      .then(function (sv) {
        migSurvey = sv;
        // 默认只勾"目标端还没有"的。已存在的默认不勾——否则用户会以为
        // 点一下就要覆盖对方的东西，而这个工具恰恰承诺从不覆盖。
        migPicked = {};
        migGroups(sv).forEach(function (g) {
          g.list.forEach(function (c) {
            if (c.state === "new") migPicked[migKey(g.kind, c.id)] = true;
          });
        });
        renderMigrate();
      })
      .catch(function (e) {
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "扫描失败：" + e.message }));
      });
  }

  function renderMigGroup(g) {
    var box = el("div", { style: "margin-bottom:16px" });
    // 只显示条目数，不显示"已选/总数"：后者在勾选变化时不会跟着更新，
    // 会让人误以为默认全选。实时计数统一由页脚的"已选 N 项"负责。
    box.appendChild(el("div", { class: "row", style: "margin-bottom:4px" }, [
      el("b", { style: "font-size:13px", text: g.label }),
      pill(g.list.length + " 项", "ghost")
    ]));

    g.list.forEach(function (c) {
      var key = migKey(g.kind, c.id);
      var cb = el("input", { type: "checkbox" });
      cb.checked = !!migPicked[key];
      cb.disabled = c.state === "blocked";
      cb.addEventListener("change", function () {
        migPicked[key] = cb.checked;
        updateMigCount();
      });

      var bits = [];
      if (c.size) bits.push(humanSize(c.size));
      if (c.state === "exists") bits.push("目标端已有，跳过");
      else if (c.state === "blocked") bits.push(c.note || "不可搬运");

      // 分隔线交给 .mig-item 的样式，不写死颜色——写死就跟着调色板一起过期。
      box.appendChild(el("label", { class: "check mig-item" }, [
        cb,
        el("span", { style: "flex:1;min-width:0" }, [
          el("span", { text: c.title }),
          bits.length ? el("span", { class: "desc", text: bits.join("　") }) : null,
          c.subtitle && c.subtitle !== c.title ? el("span", { class: "desc", text: c.subtitle }) : null
        ])
      ]));
    });
    return box;
  }

  // 勾选变化时只刷新计数与按钮状态，不重绘整棵列表——
  // 重绘会让刚点的那一项失去焦点，键盘操作跑不了。
  function updateMigCount() {
    var n = 0;
    Object.keys(migPicked).forEach(function (k) { if (migPicked[k]) n++; });
    var cnt = document.getElementById("mig-count");
    if (cnt) cnt.textContent = "已选 " + n + " 项";
    var btn = document.getElementById("btn-mig-run");
    if (btn) btn.disabled = n === 0;
  }

  function migPickAll(on) {
    if (!migSurvey) return;
    migGroups(migSurvey).forEach(function (g) {
      g.list.forEach(function (c) {
        if (c.state === "blocked") return;
        migPicked[migKey(g.kind, c.id)] = on;
      });
    });
    renderMigrate();
  }

  function renderMigrate() {
    var sv = migSurvey;
    var box = document.getElementById("mig-body");
    clear(box);

    box.appendChild(el("div", { class: "row", style: "margin-bottom:12px" }, [
      pill(migTitle(), "info"),
      sv.targetRunning ? pill("目标端正在运行", "warn") : null
    ]));

    if (sv.targetRunning) {
      box.appendChild(el("div", { class: "note warn", style: "margin-bottom:12px" }, [
        el("b", { text: "目标端客户端还在运行。" }),
        el("div", { text: "搬运可以照做，但会话列表是它启动时读进内存的，要重启后才会出现搬运来的历史。" })
      ]));
    }
    (sv.warnings || []).forEach(function (w) {
      box.appendChild(el("div", { class: "note warn", style: "margin-bottom:10px", text: w }));
    });

    var total = 0;
    migGroups(sv).forEach(function (g) {
      total += g.list.length;
      if (g.list.length) box.appendChild(renderMigGroup(g));
    });

    if (!total) {
      box.appendChild(el("div", { class: "note ok", text: "另一侧没有可搬运的内容。" }));
      return;
    }

    box.appendChild(el("div", { class: "row", style: "margin-top:14px" }, [
      el("button", { class: "primary", id: "btn-mig-run", text: "开始搬运", onclick: runMigrate }),
      el("button", { class: "ghost", text: "全选待搬项", onclick: function () { migPickAll(true); } }),
      el("button", { class: "ghost", text: "全不选", onclick: function () { migPickAll(false); } }),
      el("span", { class: "hint", id: "mig-count", style: "margin:0" })
    ]));
    box.appendChild(el("p", { class: "hint", text: "只新增，绝不覆盖：目标端已有的文件与索引行一律跳过。写索引前会先整份备份目标端数据库（含 -wal / -shm）。" }));
    updateMigCount();
  }

  function runMigrate() {
    if (busy || !migSurvey) return;

    var kinds = [], ids = [];
    migGroups(migSurvey).forEach(function (g) {
      var any = false;
      g.list.forEach(function (c) {
        if (migPicked[migKey(g.kind, c.id)]) {
          any = true;
          if (g.kind === "sessions") ids.push(c.id);
        }
      });
      if (any) kinds.push(g.kind);
    });
    if (!kinds.length) return;

    busy = true;
    var btn = document.getElementById("btn-mig-run");
    if (btn) { btn.disabled = true; btn.textContent = "正在搬运…"; }

    api("/api/migrate/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ from: migFrom, to: migTo, kinds: kinds, sessionIds: ids })
    }).then(function (rep) {
      renderMigReport(rep);
      toast("搬运完成：复制 " + rep.copiedFiles + " 个文件，写入 " + rep.rowsInserted + " 条索引。", "ok");
    }).catch(function (e) {
      toast("搬运失败：" + e.message, "bad");
      if (btn) { btn.disabled = false; btn.textContent = "开始搬运"; }
    }).then(function () { busy = false; });
  }

  function renderMigReport(rep) {
    var box = document.getElementById("mig-body");
    clear(box);

    box.appendChild(el("div", { class: "note ok", style: "margin-bottom:12px" }, [
      el("b", { text: "搬运完成。" }),
      el("div", { text: "重新启动" + (backendById(migTo) || {}).displayName + "客户端，即可在列表里看到搬运来的历史。" })
    ]));

    var rows = [
      ["整项跳过", rep.skippedItems ? rep.skippedItems + " 项（目标端已有）" : "0 项"],
      ["复制文件", rep.copiedFiles + " 个（跳过已存在 " + rep.skippedFiles + " 个）"],
      ["写入索引", rep.rowsInserted + " 行（已存在 " + rep.rowsSkipped + " 行）"]
    ];
    // 改写路径意味着搬过去的会话文件与来源不再逐字节一致，用户有权知道。
    if (rep.pathRewrites) rows.push(["改写图片路径", rep.pathRewrites + " 处（会话内指向目标端数据目录）"]);
    if (rep.backupDir) rows.push(["数据库备份", rep.backupDir]);

    box.appendChild(el("dl", { class: "kv" }, rows.map(function (r) {
      return [el("dt", { text: r[0] }), el("dd", { text: r[1] })];
    }).reduce(function (a, b) { return a.concat(b); }, [])));

    (rep.warnings || []).forEach(function (w) {
      box.appendChild(el("div", { class: "note warn", style: "margin-top:10px", text: w }));
    });

    box.appendChild(el("div", { class: "row", style: "margin-top:14px" }, [
      el("button", { text: "重新扫描", onclick: loadMigrate })
    ]));
  }
