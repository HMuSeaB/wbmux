  // ---------- ZCode 会话（只读） ----------
  // 读的是 ZCode 的库，导出的是 Markdown。全程不写 ZCode 的任何数据。
  var zcLoaded = false;

  function loadZCode(force) {
    var box = document.getElementById("zc-list");
    var note = document.getElementById("zc-note");
    if (zcLoaded && !force) return;
    note.textContent = "读取中…";
    api("/api/zcode/list").then(function (r) {
      zcLoaded = true;
      renderZCodeList(r);
      note.textContent = r.available ? "" : (r.note || "本机没有 ZCode");
    }).catch(function (e) {
      note.textContent = "读取失败：" + e.message;
      toast("读取 ZCode 列表失败：" + e.message, "bad");
    });
  }

  function renderZCodeList(r) {
    var box = document.getElementById("zc-list");
    clear(box);
    if (!r.available) {
      box.appendChild(el("p", { class: "hint", text: r.note || "本机没有 ZCode 的会话库。" }));
      return;
    }
    var list = r.sessions || [];
    if (!list.length) {
      box.appendChild(el("p", { class: "hint", text: "ZCode 里还没有会话。" }));
      return;
    }
    var card = el("div", { class: "card" });
    card.appendChild(el("div", { class: "card-head" }, [
      el("span", { class: "card-title", text: "会话（" + list.length + "）" })
    ]));
    list.forEach(function (s) {
      var row = el("div", { class: "zc-row" }, [
        el("div", { class: "zc-title", text: s.title }),
        el("div", { class: "zc-meta", text: (s.when || "") + " · " + (s.directory || "") })
      ]);
      row.onclick = function () { showZCode(s); };
      card.appendChild(row);
    });
    box.appendChild(card);
  }

  function showZCode(s) {
    var view = document.getElementById("zc-view");
    clear(view);
    view.appendChild(el("p", { class: "hint", text: "读取中…" }));
    api("/api/zcode/show?id=" + encodeURIComponent(s.id)).then(function (r) {
      renderZCodeView(r.session);
    }).catch(function (e) {
      clear(view);
      view.appendChild(el("p", { class: "hint", text: "读取失败：" + e.message }));
    });
  }

  function renderZCodeView(tr) {
    var view = document.getElementById("zc-view");
    clear(view);
    var card = el("div", { class: "card" });
    card.appendChild(el("div", { class: "card-head" }, [
      el("span", { class: "card-title", text: tr.title }),
      el("span", { class: "pill info", text: tr.msgs.length + " 条" })
    ]));
    card.appendChild(el("div", { class: "path", text: tr.directory || "" }));

    var actions = el("div", { class: "row", style: "margin-top:10px" }, [
      el("button", { class: "ghost", onclick: function () { exportZCode(tr.sessionId, false); } }),
      el("button", { class: "ghost", onclick: function () { importZCode(tr.sessionId, tr.title); } }),
    ]);
    actions.children[0].textContent = "导出 Markdown";
    actions.children[1].textContent = "导入到 WorkBuddy…";
    card.appendChild(actions);

    var body = el("div", { class: "zc-body" });
    (tr.msgs || []).forEach(function (m) {
      var name = m.role === "user" ? "我" : "助手";
      var block = el("div", { class: "zc-msg " + (m.role === "user" ? "me" : "ai") }, [
        el("div", { class: "zc-who", text: name }),
        el("div", { class: "zc-text", text: m.text || "（无正文）" })
      ]);
      if (m.tools && m.tools.length) {
        block.appendChild(el("div", { class: "zc-tools", text: "工具：" + m.tools.join("、") }));
      }
      body.appendChild(block);
    });
    card.appendChild(body);
    view.appendChild(card);
  }

  function exportZCode(id, withReasoning) {
    var note = document.getElementById("zc-note");
    note.textContent = "导出中…";
    api("/api/zcode/export", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: id,
        withReasoning: withReasoning || document.getElementById("zc-reasoning").checked
      })
    }).then(function (r) {
      note.textContent = "";
      toast(r.note || ("已导出到 " + r.path), "ok");
    }).catch(function (e) {
      note.textContent = "";
      toast("导出失败：" + e.message, "bad");
    });
  }

  document.getElementById("btn-zc-load").addEventListener("click", function () { loadZCode(true); });

  // 导入到 WorkBuddy：写操作，所以先问一次要导到哪、并让服务端给出说明文案。
  function importZCode(id, title) {
    var to = prompt("导入到哪一档？（cn = 国内，intl = 国际）\n\n" +
      "注意：目标档位的客户端要**完全退出**——它握着会话列表的缓存，\n" +
      "边跑边写会看不到新条目。", "cn");
    if (to === null) return;
    to = String(to).trim().toLowerCase();
    if (to !== "cn" && to !== "intl") { toast("只能填 cn 或 intl", "bad"); return; }

    var note = document.getElementById("zc-note");
    note.textContent = "准备导入…";
    // 第一次不带 confirm：拿服务端写的说明文案来问，避免前端自己编一遍。
    api("/api/zimport", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: id, to: to })
    }).then(function (r) {
      note.textContent = "";
      if (!confirm((r.note || "确认导入？") + "\n\n继续？")) return;
      note.textContent = "导入中…";
      return api("/api/zimport", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: id, to: to, confirm: true,
          withReasoning: document.getElementById("zc-reasoning").checked
        })
      }).then(function (r2) {
        note.textContent = "";
        toast(r2.note || "已导入", "ok");
      });
    }).catch(function (e) {
      note.textContent = "";
      toast("导入失败：" + e.message, "bad");
    });
  }

  // ---------- 二级页面 ----------
  // 侧边栏切换：状态 / 代理日志 / 切换与双开 / 历史搬运 / 体检。
  // 整页 hidden 切换，比一页长滚动清晰——页面之间互不挤压。
  function showPage(page) {
    document.querySelectorAll("main [data-page]").forEach(function (el) {
      el.hidden = el.dataset.page !== page;
    });
    document.querySelectorAll(".nav-item").forEach(function (n) {
      n.classList.toggle("active", n.dataset.page === page);
    });
    // 切进来先拉一次，别让用户对着空页面等下一个刷新周期。
    if (page === "status") {
      loadModelRadar(true);
      loadUsage(true, true);
    }
    if (page === "log") loadProxyLog(true);
    if (page === "growth") loadDomesticGrowth(false);
    if (page === "switch") {
      loadClientUpdateState();
      refreshClientProcStatus();
    }
    // 签到**不在切页时重发写请求**。
    //
    // 打开界面时已经签过一遍（loadCheckin(true)，见启动段）。切回来再发一次
    // POST /api/checkin/auto 是多余的写操作——服务端虽然幂等，但"用户只是切了
    // 个 tab"不该产生一次改账号状态的请求。已经拿到了状态就直接重绘，
    // 拿不到（启动时那次失败了）才重新查一次。
    if (page === "checkin") {
      // renderCheckin 只重绘 #checkin-body，不动 #checkin-auto-note，
      // 所以启动时那句"打开 wbmux 时已自动签：…"会留在原处——正是想要的。
      // 它不会被清掉，是因为那个提示是**这一趟界面会话里发生过的事实**，
      // 不该因为切了个 tab 就消失。
      if (checkinLast) {
        renderCheckin(checkinLast);
        refreshCheckinButtons();
      } else {
        loadCheckin(false);
      }
      loadClientUpdateState();
    }
    if (page === "workspace") loadWorkspace();
    if (page === "recycle") loadRecycle();
    if (page === "providers") loadProviders(true);
    if (page === "compact") loadCompact();
    if (page === "gateway") loadGatewayAudit(true);
    if (page === "zcode") loadZCode(false);
    if (page === "websearch") loadWebSearch();
    if (page === "sessions") {
      var fr = document.getElementById("sessions-frame");
      if (fr && !fr.src) fr.src = "/sessions?t=" + encodeURIComponent(TOKEN);
    }
  }
  document.querySelectorAll(".nav-item").forEach(function (n) {
    n.addEventListener("click", function () { showPage(n.dataset.page); });
  });

  function runProbe() {
    var box = document.getElementById("probe-body");
    clear(box);
    box.appendChild(el("div", { class: "hint", text: "正在两侧各发一条最小消息并对比账单…" }));
    api("/api/probe", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })
      .then(function (res) {
        clear(box);
        (res.results || []).forEach(function (r) {
          if (!r.ok) {
            box.appendChild(el("div", { class: "note bad", style: "margin:4px 0",
              text: "× " + sideLabel(r.side) + "：" + r.err }));
            return;
          }
          var bill = r.billingChanged
            ? "账单变化：" + r.delta
            : "账单无变化";
          box.appendChild(el("div", { class: "note ok", style: "margin:4px 0",
            text: "√ " + sideLabel(r.side) + "：" + (r.model || "?") +
                 " · 消耗 " + (r.credit || 0).toFixed(2) + " 积分 · " +
                 (r.promptTokens || 0) + "+" + (r.completionTokens || 0) + " tokens · " + bill }));
          r.reply ? box.appendChild(el("div", { class: "hint", style: "margin:0 0 6px",
            text: "回复：" + r.reply })) : null;
        });
      })
      .catch(function (e) {
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "测试失败：" + e.message }));
      });
  }

  function runInject() {
    if (busy) return;
    busy = true;
    var btn = document.getElementById("btn-inject");
    if (btn) { btn.disabled = true; btn.textContent = "注入中…"; }
    api("/api/inject-custom-models", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}"
    }).then(function (res) {
      toast(res.note + "（注入 " + res.added + " 个）", "ok");
      loadUsage();
    }).catch(function (e) {
      toast("注入失败：" + e.message, "bad");
    }).then(function () {
      busy = false;
      if (btn) { btn.disabled = false; btn.innerHTML = ICO("up", "ico-sm") + "一键注入免费模型到国内端"; }
    });
  }

  function runInjectCNToIntl() {
    if (busy) return;
    busy = true;
    var btn = document.getElementById("btn-inject-cn-intl");
    if (btn) { btn.disabled = true; btn.textContent = "反向注入中…"; }
    api("/api/inject-cn-into-intl", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}"
    }).then(function (res) {
      toast((res.note || "反向注入完成") + "（共 " + (res.added || 0) + " 个）", "ok");
    }).catch(function (e) {
      toast("反向注入失败：" + e.message, "bad");
    }).then(function () {
      busy = false;
      if (btn) {
        btn.disabled = false;
        btn.innerHTML = '<svg class="ico ico-sm" aria-hidden="true"><use href="#i-download"/></svg> 反向注入国内模型到国际端';
      }
    });
  }

  function openClientFolder(path) {
    if (!path) return;
    api("/api/open-folder", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: path })
    }).catch(function (err) {
      toast("打开目录失败: " + err.message, "bad");
    });
  }

  function launchSide(id) {
    if (busy) return;
    busy = true;
    toast("正在启动 " + sideLabel(id) + " 客户端…", "info");
    api("/api/launch", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: id, native: true })
    }).then(function (pv) {
      toast("已启动 " + pv.targetName + " 客户端。", "ok");
      refreshClientProcStatus();
    }).catch(function (e) {
      toast("启动失败: " + e.message, "bad");
    }).then(function () { busy = false; });
  }

  function restartSide(id) {
    if (busy) return;
    var name = sideLabel(id);
    if (!confirm("会先关闭正在运行的【" + name + "】客户端，然后重新启动它。\n\n" +
                 "注意：仅重启该端，绝不影响另一侧并行运行的客户端！\n请确认当前对话或任务已保存。要继续吗？")) return;
    busy = true;
    toast("正在重启 " + name + " 客户端…", "info");
    api("/api/restart-client", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: id, native: true })
    }).then(function (pv) {
      var closed = pv.killed ? ("已关闭 " + pv.killed + " 个旧进程，") : "";
      toast(closed + "已重新启动 " + pv.targetName + " 客户端，最新模型配置已生效。", "ok");
      refreshClientProcStatus();
    }).catch(function (e) {
      toast("重启失败: " + e.message, "bad");
    }).then(function () { busy = false; });
  }

  function terminateSide(id) {
    if (busy) return;
    var name = sideLabel(id);
    if (!confirm("确定要仅退出【" + name + "】客户端进程吗？\n\n注意：另一侧客户端将保持运行，不受影响。")) return;
    busy = true;
    api("/api/terminate-client", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: id })
    }).then(function (res) {
      toast("已退出 " + name + "（关闭了 " + (res.killed || 0) + " 个进程）。", "ok");
      refreshClientProcStatus();
    }).catch(function (e) {
      toast("关闭客户端失败: " + e.message, "bad");
    }).then(function () { busy = false; });
  }

  function refreshClientProcStatus() {
    return api("/api/clients/status").then(function (res) {
      if (res && res.clients) {
        state.clients = res.clients;
        if (state.installs) {
          state.installs.forEach(function (inst) {
            var c = res.clients[inst.id];
            if (c) {
              inst.running = c.running;
              inst.pids = c.pids;
            }
          });
        }
      }
      renderInstalls();
      syncControlHub();
    }).catch(function () {});
  }

  function syncControlHub() {
    var cnStat = (state && state.clients && state.clients.cn) || { running: false, pids: [] };
    var intlStat = (state && state.clients && state.clients.intl) || { running: false, pids: [] };

    var elCn = document.getElementById("hub-stat-cn");
    if (elCn) {
      if (cnStat.running) {
        elCn.className = "pill ok";
        elCn.textContent = "🟢 运行中" + ((cnStat.pids && cnStat.pids.length) ? (" · PID " + cnStat.pids.join(",")) : "");
      } else {
        elCn.className = "pill ghost";
        elCn.textContent = "⚪ 未启动";
      }
    }

    var elIntl = document.getElementById("hub-stat-intl");
    if (elIntl) {
      if (intlStat.running) {
        elIntl.className = "pill ok";
        elIntl.textContent = "🟢 运行中" + ((intlStat.pids && intlStat.pids.length) ? (" · PID " + intlStat.pids.join(",")) : "");
      } else {
        elIntl.className = "pill ghost";
        elIntl.textContent = "⚪ 未启动";
      }
    }

    var btnLabel = document.getElementById("btn-smart-both-label");
    if (btnLabel) {
      if (cnStat.running && intlStat.running) {
        btnLabel.textContent = "两端均在运行中 (点击重新检测)";
      } else if (cnStat.running && !intlStat.running) {
        btnLabel.textContent = "⚡ 智能补开国际版 (国内端在跑)";
      } else if (!cnStat.running && intlStat.running) {
        btnLabel.textContent = "⚡ 智能补开国内版 (国际端在跑)";
      } else {
        btnLabel.textContent = "⚡ 智能自适应双开";
      }
    }
  }

  function launchBoth(force) {
    if (busy) return;
    busy = true;
    var btn = document.getElementById("btn-both");
    var smartBtn = document.getElementById("btn-smart-both");
    var smartLabel = document.getElementById("btn-smart-both-label");
    if (btn) { btn.disabled = true; btn.textContent = "正在处理…"; }
    if (smartBtn) { smartBtn.disabled = true; }
    if (smartLabel) smartLabel.textContent = "正在处理…";

    api("/api/launch-both", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ force: !!force })
    }).then(function (res) {
      var launched = [], already = [], bad = [];
      (res.results || []).forEach(function (r) {
        var label = sideLabel(r.side);
        if (r.alreadyRunning) {
          already.push(label + "（PID " + ((r.pids && r.pids.length) ? r.pids.join(",") : "活动") + " 已在运行）");
        } else if (r.ok) {
          launched.push(label);
        } else {
          bad.push(label + "（" + r.err + "）");
        }
      });
      if (bad.length === 0) {
        if (launched.length > 0 && already.length > 0) {
          toast("智能自适应完成：已补开 " + launched.join("、") + "；" + already.join("、"), "ok");
        } else if (launched.length > 0) {
          toast("双端均已启动：" + launched.join("、"), "ok");
        } else {
          toast("双端均已在运行中：" + already.join("、"), "info");
        }
      } else if (launched.length === 0 && already.length === 0) {
        toast("双开失败：" + bad.join("、"), "bad");
      } else {
        toast("部分成功：" + launched.concat(already).join("、") + "；失败：" + bad.join("、"), "warn");
      }
      refreshClientProcStatus();
    }).catch(function (e) {
      toast("双开失败：" + e.message, "bad");
    }).then(function () {
      busy = false;
      if (btn) { btn.disabled = false; btn.innerHTML = ICO("layers", "ico-sm") + "强制双开两侧"; }
      if (smartBtn) { smartBtn.disabled = false; }
      syncControlHub();
    });
  }

  function launch() {
    if (busy || !target) return;
    busy = true;
    var btn = document.getElementById("btn-launch");
    if (btn) { btn.disabled = true; btn.textContent = "正在启动…"; }
    syncLaunchBtn(true, "正在启动…");

    api("/api/launch", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: target, native: nativeMode })
    }).then(function (pv) {
      renderPreview(pv);
      toast("已启动客户端，连接到" + pv.targetName + "后端。", "ok");
    }).catch(function (e) {
      toast("启动失败：" + e.message, "bad");
      if (btn) { btn.disabled = false; btn.textContent = "启动客户端"; }
      syncLaunchBtn(false);
    }).then(function () { busy = false; });
  }

  // restartClient 关掉正在运行的客户端再重新启动它。
  //
  // 为什么值得有个专门的按钮：压缩阈值、后端配置这类设置是客户端
  // **启动时**才读的，改完不重启就没效果。而让用户自己去托盘退出、
  // 再回来点「启动客户端」，他很容易在这一步以为"改了没用"。
  //
  // 会关掉他正在用的客户端，所以必须问一句——这可能丢掉没保存的东西。
  function restartClient() {
    if (busy || !target) return;
    if (!confirm("会先关闭正在运行的客户端，再重新启动它。\n\n" +
                 "请确认没有未保存的对话或正在跑的任务。要继续吗？")) return;

    busy = true;
    var btn = document.getElementById("btn-restart-side");
    if (btn) { btn.disabled = true; btn.innerHTML = ICO("swap", "ico-sm") + "重启中…"; }

    api("/api/restart-client", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: target, native: nativeMode })
    }).then(function (pv) {
      renderPreview(pv);
      var closed = pv.killed ? ("已关闭 " + pv.killed + " 个旧进程，") : "";
      toast(closed + "已重新启动客户端（" + pv.targetName + "）。新设置这次就生效了。", "ok");
    }).catch(function (e) {
      toast("重启失败：" + e.message, "bad");
    }).then(function () {
      busy = false;
      if (btn) { btn.disabled = false; btn.innerHTML = ICO("swap", "ico-sm") + "重启客户端"; }
    });
  }

  function runDoctor() {
    if (busy) return;
    busy = true;
    var btn = document.getElementById("btn-doctor");
    btn.disabled = true;
    btn.textContent = "检查中…";
    clear(document.getElementById("doctor-body"));
    document.getElementById("doctor-body").appendChild(
      el("div", { class: "hint" }, [el("span", { class: "spinner" }), "正在扫描安装包，这一步比较慢…"])
    );

    api("/api/doctor?target=" + encodeURIComponent(target || state.hostId || "cn"))
      .then(renderDoctor)
      .catch(function (e) {
        var box = document.getElementById("doctor-body");
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: e.message }));
      })
      .then(function () {
        busy = false;
        btn.disabled = false;
        btn.textContent = "检查当前配置";
      });
  }

  // ---------- 回收站清理 ----------

  function humanBytes(n) {
    if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + " GB";
    if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
    if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + " KB";
    return n + " B";
  }

  // busy 期间禁用全部按钮；结束后**按当前数据重新判定**，而不是一律放开。
  // 之前是 .then(function(){ recycleBusy(false); }) 直接把按钮全放开，
  // 结果把 renderRecycle 刚设好的"没有噪声就禁用清理按钮"又覆盖掉了。
  // ���里统一走 refreshRecycleButtons，由它看数据说话。
  var recycleLast = null;

  function tierCount(cat) {
    var s = (recycleLast && recycleLast.summary) || null;
    if (!s || !s.tiers) return 0;
    for (var i = 0; i < s.tiers.length; i++) {
      if (s.tiers[i].category === cat) return s.tiers[i].count || 0;
    }
    return 0;
  }

  function refreshRecycleButtons() {
    [["btn-recycle-noise", "noise"], ["btn-recycle-scratch", "scratch"],
     ["btn-recycle-nuke", "keep"]].forEach(function (p) {
      var b = document.getElementById(p[0]);
      if (b) b.disabled = tierCount(p[1]) === 0;
    });
  }

  function recycleBusy(on) {
    if (on) {
      ["btn-recycle-scan", "btn-recycle-noise", "btn-recycle-scratch",
       "btn-recycle-nuke"].forEach(function (id) {
        var b = document.getElementById(id);
        if (b) b.disabled = true;
      });
      return;
    }
    var scan = document.getElementById("btn-recycle-scan");
    if (scan) scan.disabled = false;
    refreshRecycleButtons();
  }

  // ---------- 每日签到 ----------
  // 两个按钮对应两种性质：刷新是读，领取是写。进页面只读，写必须由用户亲手点。
  var checkinLast = null;
  var checkinBusyFlag = false;
  // checkinAsked 记录"这一趟界面会话里已经问过服务端要不要签了"。
  // 与 checkinLast 的区别：后者是**结果**（失败时为 null），前者是**事实**。
  // 分开记才能做到"失败也不重发"。
  var checkinAsked = false;

  function checkinBusy(on) {
    checkinBusyFlag = on;
    ["btn-checkin-refresh", "btn-checkin-claim"].forEach(function (id) {
      var b = document.getElementById(id);
      if (b) b.disabled = on;
    });
    // 放开时交给统一判定，别在几处各写一遍 disabled（回收站那页就漏过一次）。
    if (!on) refreshCheckinButtons();
  }

  function refreshCheckinButtons() {
    var claim = document.getElementById("btn-checkin-claim");
    if (!claim) return;
    var st = checkinLast && checkinLast.status;
    // 只在"活动开着、今天还没签"时才让点。其余状态下点了也没有意义，
    // 而一个可点的按钮只会诱使用户以为还有别的事可做。
    claim.disabled = checkinBusyFlag || !st || !st.active || st.todayCheckedIn;
    if (!st) claim.title = "";
    else if (!st.active) claim.title = "这一侧没有开签到活动";
    else if (st.todayCheckedIn) claim.title = "今天已经签过了";
    else claim.title = "";
  }

  // runIntlActiveSimulation 统一调度国际端模拟活跃对话（+30 积分）生命周期
  function runIntlActiveSimulation(btnEl, resultBoxEl) {
    if (!btnEl) return;
    btnEl.disabled = true;
    var origHtml = btnEl.innerHTML;
    btnEl.innerHTML = '<span class="spinner-sm"></span> 模拟对话中…';

    if (resultBoxEl) {
      resultBoxEl.hidden = false;
      resultBoxEl.style.display = "block";
      resultBoxEl.innerHTML = '<div style="display:flex;align-items:center;gap:6px;color:var(--brand);font-size:12px;padding:8px 0">' +
        '<span class="spinner-sm"></span> 正在向国际后端发起免积分对话 (动态匹配官方限免模型) 并上报 6 项桌面事件…' +
        '</div>';
    }

    api("/api/growth/intl/simulate-active", { method: "POST" }).then(function (res) {
      btnEl.disabled = false;
      btnEl.innerHTML = '<svg class="ico ico-sm" aria-hidden="true" style="margin-right:3px"><use href="#i-check"/></svg> 今日已模拟';
      btnEl.style.background = "rgba(34,197,94,0.18)";
      btnEl.style.color = "var(--green, #22c55e)";
      toast("模拟对话成功！官方次日统一发放 +30 积分奖励", "ok");

      if (resultBoxEl) {
        resultBoxEl.hidden = false;
        resultBoxEl.style.display = "block";
        resultBoxEl.innerHTML =
          '<div style="background:rgba(59,130,246,0.06);border:1px solid rgba(59,130,246,0.25);border-radius:var(--r-sm);padding:12px 14px">' +
            '<div style="display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:8px">' +
              '<span style="font-weight:600;color:var(--green, #22c55e);display:flex;align-items:center;gap:5px;font-size:12.5px">' +
                '<svg class="ico ico-sm"><use href="#i-check"/></svg> 模拟对话闭环达成 (次日 +30 积分)' +
              '</span>' +
              '<span style="font-size:11px;color:var(--ink-3);font-family:var(--mono)">耗时 ' + res.duration_ms + 'ms (首字 ' + res.ttft_ms + 'ms)</span>' +
            '</div>' +
            '<div style="font-size:11.5px;color:var(--ink-2);margin-bottom:6px">' +
              '<strong>调用模型：</strong><code style="color:var(--brand);font-size:12px">' + (res.model || 'hy4free') + '</code> ' +
              '<span class="badge" style="background:rgba(34,197,94,0.15);color:var(--green);font-size:10.5px;padding:1px 6px;border-radius:3px">0.00x 免积分</span>' +
            '</div>' +
            '<div style="font-size:11.5px;color:var(--ink-2);background:rgba(0,0,0,0.18);padding:8px 10px;border-radius:4px;margin-bottom:8px;line-height:1.5;font-style:italic">' +
              '“' + (res.reply || '') + '”' +
            '</div>' +
            '<div style="font-size:11px;color:var(--ink-3);display:flex;align-items:center;gap:4px">' +
              '<svg class="ico ico-sm" style="color:var(--green)"><use href="#i-check"/></svg> ' + (res.report_message || '已成功闭环上报 6 项桌面事件') +
            '</div>' +
            '<div style="font-size:11px;color:var(--ink-3);margin-top:4px">' +
              '💡 <strong>积分说明：</strong>' + (res.next_day_reward_note || '模拟对话已完成，次日官方跑批自动发放 +30 积分。') +
            '</div>' +
          '</div>';
      }
      loadUsage(true, true);
    }).catch(function (err) {
      btnEl.disabled = false;
      btnEl.innerHTML = origHtml;
      if (resultBoxEl) {
        resultBoxEl.hidden = false;
        resultBoxEl.style.display = "block";
        resultBoxEl.innerHTML = '<div style="color:var(--bad, #ef4444);font-size:12px;background:rgba(239,68,68,0.1);padding:10px;border-radius:var(--r-sm)">模拟对话失败：' + (err.message || '网络或接口异常') + '</div>';
      }
      toast("模拟对话失败：" + err.message, "bad");
    });
  }

  // 进页面时**先自动签到**，再按结果渲染。
  //
  // 为什么整页只调一个接口而不是"先查再决定要不要领"：那个判断放在服务端
  // （checkin.AutoClaim）——页面随时可能被刷新、被后台标签页重载，判断放前端
  // 就会出现同一时刻多次请求。这里只管调一次、把结果如实显示出来。
  //
  // 手动重查走 loadCheckinStatus（只读），能清楚区分"我点的是刷新"与
  // "页面自己在自动签"——两者性质不同，不该共用一个入口。
  //
  // atBoot 为 true 表示是**打开界面时**自动跑的（不是用户切到这一页）。
  // updateCheckinBadge 同步更新侧边栏上的签到徽章状态
  // updateCheckinBadge 同步更新侧边栏上的签到徽章状态
  function updateCheckinBadge(data) {
    var b = document.getElementById("nav-checkin-badge");
    if (!b) return;
    if (!data) {
      b.textContent = "未连接";
      b.className = "nav-badge";
      return;
    }
    var st = data.status || {};
    if (st.todayCheckedIn || (data.skipped && /already|今日已签|已领过|已签/.test(data.skipped + (data.note || "")))) {
      b.textContent = "已签";
      b.className = "nav-badge ok";
    } else if (st.active === false) {
      b.textContent = "未开";
      b.className = "nav-badge";
    } else {
      b.textContent = "待签";
      b.className = "nav-badge";
    }
  }

  // updateUsageCheckinWidget 精准局部更新【概览与额度】卡片上的打卡状态，避免触发全量重绘竞态
  function updateUsageCheckinWidget() {
    var ckSt = (checkinLast && checkinLast.status) || {};
    var isSigned = !!(ckSt.todayCheckedIn || (checkinLast && checkinLast.skipped && /already|已签|已领/.test(checkinLast.skipped + (checkinLast.note || ""))));
    var streak = ckSt.streakDays || 1;

    // 1. 更新顶层 KPI 栏
    var kpiFoot = document.getElementById("kpi-cn-checkin");
    if (kpiFoot) {
      clear(kpiFoot);
      kpiFoot.appendChild(el("span", {
        text: isSigned ? ("✅ 今日已签 · 连签 " + streak + " 天") : "⚠️ 今日未打卡 (可领积分)"
      }));
    }

    // 2. 更新国内端卡片挂件文字
    var cardText = document.getElementById("card-cn-checkin-text");
    if (cardText) {
      clear(cardText);
      cardText.appendChild(el("svg", { class: "ico ico-sm", html: '<use href="#i-calendar"/>' }));
      cardText.appendChild(el("span", {
        text: isSigned ? ("今日已签 · 连签 " + streak + " 天") : "每日签到：顺手打卡领积分"
      }));
    }

    // 3. 更新国内端卡片挂件按钮
    var btnAction = document.getElementById("btn-cn-checkin-action");
    if (btnAction) {
      btnAction.className = isSigned ? "ghost" : "btn-hero";
      btnAction.textContent = isSigned ? "查看明细" : "一键打卡";
      btnAction.onclick = function () {
        if (isSigned) {
          showPage("checkin");
        } else {
          btnAction.textContent = "打卡中…";
          api("/api/checkin/claim", { method: "POST" }).then(function (r) {
            toast("签到成功 +" + (r.credit || 10) + " 积分", "ok");
            loadUsage(true, true);
            loadCheckin(false);
          }).catch(function (e) {
            toast(e.message, "bad");
            showPage("checkin");
          });
        }
      };
    }
  }

  // 两种情况都照常签，区别只在提示语：打开界面时用户可能压根没在看这一页，
  // 得在页面上留一句"刚才是自动签的"，免得他以为要手动补一次。
  function loadCheckin(atBoot) {
    var box = document.getElementById("checkin-body");
    if (!box) return;
    if (checkinAsked) return;
    checkinAsked = true;
    loadAutoCheckinToggle();
    clear(box);
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在检查今天的签到…"]));
    checkinBusy(true);

    api("/api/checkin/auto", { method: "POST" })
      .then(function (data) {
        var st = data.status || {};
        if (data.skipped && /already|已签|已领/.test(data.skipped + (data.note || ""))) {
          st.todayCheckedIn = true;
        }
        if (data.result && (data.result.alreadyCheckedIn || data.result.credit > 0)) {
          st.todayCheckedIn = true;
          if (data.result.streakDays > 0) st.streakDays = data.result.streakDays;
        }
        checkinLast = { side: data.side, label: data.label, status: st, skipped: data.skipped, note: data.note, result: data.result };
        updateCheckinBadge(data);
        renderAutoNote(data, atBoot);
        updateUsageCheckinWidget();
        if (data.status) {
          renderCheckin(checkinLast);
        } else {
          clear(box);
          box.appendChild(el("div", { class: "note warn",
            text: data.note || "没读到签到状态。" }));
          refreshCheckinButtons();
        }
      })
      .catch(function (e) {
        checkinLast = null;
        updateCheckinBadge(null);
        updateUsageCheckinWidget();
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "自动签到失败：" + e.message }));
      })
      .then(function () { checkinBusy(false); });
  }

  // renderAutoNote 把"这次自动签到做了什么"明说出来。
  //
  // 这一步是硬要求：自动签**会改账号状态**，用户有权知道它刚才动没动、动了什么。
  // 悄悄签掉、只显示一个"已签到"，等于把写操作藏起来。
  function renderAutoNote(data, atBoot) {
    var box = document.getElementById("checkin-auto-note");
    if (!box) return;
    clear(box);
    var note = data && data.note;
    if (!note) return;
    if (atBoot) {
      // 打开界面时就签掉了：用户这一趟可能根本没看这一页，
      // 所以除了说明做了什么，还要点明"你不用再点一次"。
      note = "打开 wbmux 时已自动签：" + note;
    }
    var cls = "note ok";
    if (data.skipped === "unavailable") cls = "note warn";
    else if (data.skipped === "disabled") cls = "note";
    box.appendChild(el("div", { class: cls, text: note }));
  }

  // renderClientUpdateState 把"当前是开还是关"写在开关旁边。
  function renderClientUpdateState(on) {
    var s = document.getElementById("client-update-state");
    if (!s) return;
    s.textContent = on ? "（已开启；压制启动静默更新，保护本地配置）"
                       : "（未开启；保持官方默认更新策略）";
  }

  // renderClientUpdateTargets 渲染探测到的具体客户端 app-config.json 物理目标列表
  function renderClientUpdateTargets(targets) {
    var box = document.getElementById("client-update-targets-list");
    if (!box) return;
    clear(box);
    if (!targets || targets.length === 0) {
      box.appendChild(el("div", {
        style: "font-size:11.5px;color:var(--ink-3);padding:4px 0",
        text: "未在默认位置检测到客户端数据目录。若您安装在非系统盘或使用便携版，可在下方指定自定义路径。"
      }));
      return;
    }
    targets.forEach(function (t) {
      var isProtected = t.status === "disabled";
      var statusPill = isProtected
        ? el("span", { class: "pill ok", style: "font-size:10px;padding:2px 7px" }, ["已受保护 · 更新已压制"])
        : (t.exists ? el("span", { class: "pill", style: "font-size:10px;padding:2px 7px;color:var(--ink-2)" }, ["官方原生策略"])
                    : el("span", { class: "pill warn", style: "font-size:10px;padding:2px 7px" }, ["尚未生成配置"]));

      var btnOpen = el("button", {
        class: "ghost",
        style: "padding:3px 9px;font-size:11px;height:26px;border-color:rgba(255,255,255,0.12);background:rgba(255,255,255,0.03);display:inline-flex;align-items:center;gap:4px;flex:none",
        title: "在 Windows 资源管理器中打开该目录"
      }, [
        el("svg", { class: "ico-sm", style: "width:13px;height:13px" }, [el("use", { href: "#i-folder" })]),
        "打开目录"
      ]);
      btnOpen.addEventListener("click", function () {
        api("/api/open-folder", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ path: t.path })
        }).catch(function (err) {
          toast("打开目录失败: " + err.message, "bad");
        });
      });

      var row = el("div", {
        style: "display:flex;align-items:center;justify-content:space-between;gap:10px;padding:8px 12px;background:rgba(255,255,255,0.025);border:1px solid rgba(255,255,255,0.06);border-radius:6px;transition:all 0.15s ease"
      }, [
        el("div", { style: "display:flex;flex-direction:column;gap:4px;min-width:0;flex:1" }, [
          el("div", { style: "display:flex;align-items:center;gap:8px" }, [
            el("span", { style: "font-size:12px;font-weight:600;color:var(--ink-1)", text: t.label }),
            statusPill
          ]),
          el("code", {
            style: "font-family:var(--mono);font-size:11px;color:var(--ink-2);background:rgba(0,0,0,0.25);padding:2px 6px;border-radius:4px;word-break:break-all;display:inline-block;max-width:fit-content",
            text: t.path
          })
        ]),
        btnOpen
      ]);
      box.appendChild(row);
    });
  }

  var lastClientUpdateData = null;

  // renderClientUpdateData 统一刷新客户端自动更新相关界面组件
  function renderClientUpdateData(d) {
    if (!d) return;
    lastClientUpdateData = d;
    var chk = document.getElementById("chk-client-update");
    if (chk) chk.checked = !!d.enabled;
    renderClientUpdateState(!!d.enabled);
    var ipt = document.getElementById("ipt-custom-client-path");
    if (ipt && d.customPath !== undefined) {
      ipt.value = d.customPath || "";
    }
    renderClientUpdateTargets(d.targets);
    var noteBox = document.getElementById("client-update-note");
    if (noteBox) {
      clear(noteBox);
      if (d.note) {
        var isWarn = d.note.indexOf("未检测到") !== -1 || d.note.indexOf("失败") !== -1;
        var noteText = d.note;
        // 如果已包含详细目标列表，提取前缀摘要，避免冗余巨型长路径
        if (!isWarn && d.note.indexOf("：") !== -1) {
          noteText = d.note.split("：")[0];
        }
        var noteItem = el("div", {
          style: "display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;font-size:12px;line-height:1.5;" +
                 (isWarn ? "background:rgba(234,179,8,0.08);border:1px solid rgba(234,179,8,0.25);color:#fde047;"
                         : "background:rgba(34,197,94,0.08);border:1px solid rgba(34,197,94,0.2);color:#86efac;")
        }, [
          el("span", { style: "font-size:14px;line-height:1" }, [isWarn ? "⚠️" : "🛡️"]),
          el("span", { style: "font-weight:500;flex:1", text: noteText })
        ]);
        noteBox.appendChild(noteItem);
      }
    }

    // 联动刷新 switch 页面上的物理防护状态卡片
    var badge = document.getElementById("client-update-status-badge");
    var desc = document.getElementById("client-update-desc");
    var btn = document.getElementById("btn-toggle-client-update");
    if (badge && desc && btn) {
      var isLocked = !!(d.enabled || d.downloadLocked);
      if (isLocked) {
        badge.textContent = "🛡️ 自动更新物理锁已启用 (5.8.3 稳定受保护)";
        badge.style.background = "rgba(34,197,94,0.15)";
        badge.style.color = "#22c55e";
        desc.textContent = "已清空更新缓存并部署只读物理占位锁，底层拦截后台静默下包与“新版本重启更新”弹窗。";
        btn.textContent = "🔓 恢复官方自动更新 (允许升级)";
        btn.className = "ghost";
      } else {
        badge.textContent = "⚡ 官方自动更新已就绪 (允许升级新版本)";
        badge.style.background = "rgba(235,178,58,0.15)";
        badge.style.color = "#ebb23a";
        desc.textContent = "当前保持官方原生更新通道，若官方推送新版将正常提示更新。";
        btn.textContent = "🛡️ 一键开启防强制更新 (物理锁死)";
        btn.className = "primary";
      }
    }
  }

  // 绑定物理锁一键开启/恢复切换按钮
  var btnToggleUpdate = document.getElementById("btn-toggle-client-update");
  if (btnToggleUpdate) {
    btnToggleUpdate.addEventListener("click", function () {
      var curEnabled = !!(lastClientUpdateData && (lastClientUpdateData.enabled || lastClientUpdateData.downloadLocked));
      var nextState = !curEnabled;
      btnToggleUpdate.disabled = true;
      toast(nextState ? "正在开启物理锁并清除残留安装包…" : "正在解除物理锁并恢复官方更新通道…", "info");
      api("/api/client-update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: nextState })
      }).then(function (res) {
        toast(nextState ? "已成功开启防强制更新物理锁！" : "已成功恢复官方自动更新通道！", "ok");
        renderClientUpdateData(res);
      }).catch(function (err) {
        toast("操作失败: " + err.message, "bad");
      }).then(function () {
        btnToggleUpdate.disabled = false;
      });
    });
  }

  // 首次进入时读一次开关状态与物理路径
  function loadClientUpdateState() {
    api("/api/client-update").then(function (d) {
      renderClientUpdateData(d);
    }).catch(function (e) {
      renderClientUpdateState(false);
    });
  }
  loadClientUpdateState();

  // loadCheckinStatus 是**只读**重查，给「刷新状态」按钮用。
  function loadCheckinStatus() {
    var box = document.getElementById("checkin-body");
    if (!box) return;
    clear(box);
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在读签到状态…"]));
    checkinBusy(true);

    api("/api/checkin")
      .then(function (data) {
        checkinLast = data;
        renderCheckin(data);
      })
      .catch(function (e) {
        // 读失败就把按钮锁上：拿不到状态时"能不能领"无从判断。
        checkinLast = null;
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "读签到状态失败：" + e.message }));
      })
      .then(function () { checkinBusy(false); });
  }

  function loadAutoCheckinToggle() {
    var cb = document.getElementById("chk-checkin-auto");
    if (!cb) return;
    api("/api/checkin/auto-toggle")
      .then(function (d) { cb.checked = !!d.enabled; })
      .catch(function () { cb.checked = true; });  // 读不到就按默认（开）
  }

  function renderCheckin(data) {
    updateCheckinBadge(data);
    var box = document.getElementById("checkin-body");
    var st = (data && data.status) || {};
    clear(box);

    if (!st.active) {
      box.appendChild(el("div", { class: "note", text: "这一侧没有开签到活动。" }));
      box.appendChild(el("div", { class: "hint", style: "margin-top:6px",
        text: "签到目前只在国内版开放；国际版接口在，但活动没开。" }));
      refreshCheckinButtons();
      return;
    }

    var dayHint = "每日 " + (st.dailyCredit || 0) + " 积分";
    if (st.todayCheckedIn) dayHint += " · 今日已得 " + (st.todayCredit || 0);

    box.appendChild(el("div", { class: "tiles" }, [
      el("div", { class: "tile" }, [
        el("div", { class: "hint", text: "连签" }),
        el("div", { class: "big", text: (st.streakDays || 0) + " 天" }),
        el("div", { class: "hint",
          text: st.weekCheckinDays ? "本周已签 " + st.weekCheckinDays + " 天" : "" })
      ]),
      el("div", { class: "tile" }, [
        el("div", { class: "hint", text: "今日" }),
        el("div", { class: "big " + (st.todayCheckedIn ? "ok" : "warn"),
                    text: st.todayCheckedIn ? "已签到" : "还没签" }),
        el("div", { class: "hint", text: dayHint })
      ])
    ]));

    if (st.streakBonusDays > 0 || st.streakBonusCredit > 0) {
      box.appendChild(el("div", { class: "note", style: "margin-top:12px",
        text: "连签奖励：还差 " + st.nextStreakDay + " 天，送 " +
              st.streakBonusDays + " 天共 " + st.streakBonusCredit + " 积分。" }));
    }

    if (st.checkinDates && st.checkinDates.length) {
      box.appendChild(el("div", { class: "hint", style: "margin:14px 0 6px",
        text: "最近签到：" }));
      box.appendChild(el("div", { class: "row" },
        st.checkinDates.slice(0, 10).map(function (d, i) {
          return pill(d, i === 0 ? "ok" : "ghost");
        })));
    }

    refreshCheckinButtons();
  }

  function claimCheckin() {
    checkinBusy(true);
    api("/api/checkin/claim", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({})
    }).then(function (data) {
      var r = (data && data.result) || {};
      if (r.alreadyCheckedIn) {
        // 已签过是**正常结果**，不是失败——别用红色弹出去吓人。
        toast(r.message || "今天已经签过了", "ok");
      } else if (r.credit > 0) {
        toast("已领取 " + r.credit + " 积分", "ok");
      } else {
        toast("后端没有回报积分数，去客户端确认一下", "bad");
      }
      // 用服务端带回的新状态重画，而不是自己改本地数字——连签天数以服务端为准。
      if (r.status) {
        checkinLast = { side: data.side, label: data.label, status: r.status };
        renderCheckin(checkinLast);
      } else {
        loadCheckin();
      }
    }).catch(function (e) {
      toast("领取失败：" + e.message, "bad");
    }).then(function () { checkinBusy(false); });
  }
