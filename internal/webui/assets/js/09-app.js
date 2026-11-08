  // ---------- 启动 ----------

  document.getElementById("btn-doctor").addEventListener("click", runDoctor);
  document.getElementById("btn-usage-scan").addEventListener("click", function () {
    loadUsage(false, true);
  });
  var btnExportCred = document.getElementById("btn-export-cred");
  if (btnExportCred) {
    btnExportCred.addEventListener("click", function () {
      showCredentialExportModal("intl");
    });
  }
  document.getElementById("btn-probe").addEventListener("click", runProbe);
  document.getElementById("btn-inject").addEventListener("click", runInject);
  var btnInjectCN = document.getElementById("btn-inject-cn-intl");
  if (btnInjectCN) btnInjectCN.addEventListener("click", runInjectCNToIntl);
  document.getElementById("btn-both").addEventListener("click", function () { launchBoth(true); });
  var btnSmartBoth = document.getElementById("btn-smart-both");
  if (btnSmartBoth) btnSmartBoth.addEventListener("click", function () { launchBoth(false); });
  var btnRefreshProc = document.getElementById("btn-refresh-proc");
  if (btnRefreshProc) {
    btnRefreshProc.addEventListener("click", function () {
      refreshClientProcStatus().then(function () {
        toast("客户端实时进程状态已刷新", "ok");
      });
    });
  }
  var btnHeroBoth = document.getElementById("btn-hero-both");
  if (btnHeroBoth) btnHeroBoth.addEventListener("click", function () { launchBoth(false); });
  var btnHeroRestart = document.getElementById("btn-hero-restart");
  if (btnHeroRestart) btnHeroRestart.addEventListener("click", restartClient);
  document.getElementById("btn-quit").addEventListener("click", quit);
  // 侧边栏的启动按钮跟页面里那个是同一个动作，只是位置常驻。
  document.getElementById("btn-launch-side").addEventListener("click", launch);
  document.getElementById("btn-restart-side").addEventListener("click", restartClient);

  // 工作区。读是 GET（进页面自动），写是两步 POST（先看计划、再确认执行）。
  document.getElementById("btn-ws-refresh").addEventListener("click", loadWorkspace);
  document.getElementById("btn-ws-plan").addEventListener("click", wsPlan);
  document.getElementById("btn-ws-apply").addEventListener("click", wsApply);

  // 成长任务与福利中心事件
  var btnGrowthSweep = document.getElementById("btn-growth-sweep");
  if (btnGrowthSweep) btnGrowthSweep.addEventListener("click", sweepDomesticGrowth);
  var btnGrowthRefresh = document.getElementById("btn-growth-refresh");
  if (btnGrowthRefresh) btnGrowthRefresh.addEventListener("click", function () { loadDomesticGrowth(false); });
  var btnGrowthCompClaim = document.getElementById("btn-growth-comp-claim");
  if (btnGrowthCompClaim) btnGrowthCompClaim.addEventListener("click", claimDomesticCompensation);
  var btnGrowthClaimAll = document.getElementById("btn-growth-claim-all");
  if (btnGrowthClaimAll) btnGrowthClaimAll.addEventListener("click", claimAllDomesticTasks);

  var btnBuddyOpen1 = document.getElementById("btn-buddy-open-1");
  if (btnBuddyOpen1) btnBuddyOpen1.addEventListener("click", function () { openBuddyBlindBox(1); });

  var btnBuddyOpenAll = document.getElementById("btn-buddy-open-all");
  if (btnBuddyOpenAll) {
    btnBuddyOpenAll.addEventListener("click", function () {
      var energyEl = document.getElementById("buddy-energy-val");
      var text = energyEl ? energyEl.textContent : "0";
      var num = parseInt(text.replace(/[^0-9]/g, ""), 10) || 0;
      var count = Math.floor(num / 10);
      if (count <= 0) {
        toast("当前能量不足 10 点，无法开启盲盒", "warn");
        return;
      }
      openBuddyBlindBox(count);
    });
  }

  var btnBuddyTravel = document.getElementById("btn-buddy-travel-action");
  if (btnBuddyTravel) btnBuddyTravel.addEventListener("click", function () { travelBuddyAction("auto"); });

  function openWorkBuddyWeb() {
    var defaultURL = "https://www.workbuddy.cn/profile/growth-center";
    var btn = document.getElementById("btn-growth-open-web");
    var headerBtn = document.getElementById("btn-growth-header-web");
    if (btn) btn.classList.add("loading");
    if (headerBtn) headerBtn.classList.add("loading");

    api("/api/auth/web-login?side=cn&target=" + encodeURIComponent("/profile/growth-center") + "&json=1")
      .then(function (res) {
        var jumpUrl = (res && res.url) ? res.url : defaultURL;
        return api("/api/open-url", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ url: jumpUrl })
        }).catch(function () {
          window.open(jumpUrl, "_blank");
        });
      })
      .catch(function () {
        return api("/api/open-url", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ url: defaultURL })
        }).catch(function () {
          window.open(defaultURL, "_blank");
        });
      })
      .finally(function () {
        if (btn) btn.classList.remove("loading");
        if (headerBtn) headerBtn.classList.remove("loading");
        toast("已免密直达官方成长计划与个人主页", "ok");
      });
  }
  var btnGrowthOpenWeb = document.getElementById("btn-growth-open-web");
  if (btnGrowthOpenWeb) btnGrowthOpenWeb.addEventListener("click", openWorkBuddyWeb);
  var btnGrowthHeaderWeb = document.getElementById("btn-growth-header-web");
  if (btnGrowthHeaderWeb) btnGrowthHeaderWeb.addEventListener("click", openWorkBuddyWeb);

  var filterTabs = document.getElementById("growth-filter-tabs");
  if (filterTabs) {
    filterTabs.addEventListener("click", function (e) {
      var btn = e.target.closest(".growth-filter-btn");
      if (!btn) return;
      var filter = btn.getAttribute("data-filter") || "all";
      growthCurrentFilter = filter;
      var allBtns = filterTabs.querySelectorAll(".growth-filter-btn");
      allBtns.forEach(function (b) { b.classList.toggle("active", b === btn); });
      if (growthLastTasks) renderDomesticTasks(growthLastTasks);
    });
  }

  var chkHideClaimed = document.getElementById("chk-growth-hide-claimed");
  if (chkHideClaimed) {
    chkHideClaimed.addEventListener("change", function () {
      growthHideClaimed = !!this.checked;
      if (growthLastTasks) renderDomesticTasks(growthLastTasks);
    });
  }

  // 签到。和回收站同理：切到这一页才去读，读一次要借客户端的运行时解凭据，
  // 没必要在启动时就跑。
  // 「刷新状态」是**只读**重查，不碰自动签到——用户点它是想看现在什么状态，
  // 不该顺手再发一次领取。要自动签的那个入口是进页面时的 loadCheckin。
  // 「刷新状态」是**只读**重查，与自动签分开（见 loadCheckin 的说明）。
  // 顺手把 checkinAsked 落回"已问过"——刷新只是看状态，不等于重新发起签到；
  // 真要重新走自动签，刷新页面即可（那时会重跑启动段）。
  document.getElementById("btn-checkin-refresh").addEventListener("click", function () {
    loadCheckinStatus();
  });
  document.getElementById("btn-checkin-claim").addEventListener("click", claimCheckin);
  var btnCheckinIntlSim = document.getElementById("btn-checkin-intl-simulate");
  if (btnCheckinIntlSim) {
    btnCheckinIntlSim.addEventListener("click", function () {
      runIntlActiveSimulation(this, document.getElementById("checkin-intl-simulate-box"));
    });
  }
  document.getElementById("chk-checkin-auto").addEventListener("change", function () {
    var box = document.getElementById("checkin-auto-note");
    var on = this.checked;
    api("/api/checkin/auto-toggle", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: on })
    }).then(function () {
      toast(on ? "已打开自动签到" : "已关闭自动签到", "ok");
      if (box) {
        clear(box);
        box.appendChild(el("div", { class: "note",
          text: on ? "下次打开这一页会自动签到。" : "已关闭：这一页不会再自动领，只显示状态。" }));
      }
    }).catch(function (e) {
      toast("保存失败：" + e.message, "bad");
      // 存失败就把勾还原，别让界面显示一个并不生效的状态。
      document.getElementById("chk-checkin-auto").checked = !on;
    });
  });

  // 客户端自动更新开关与自定义路径
  document.getElementById("chk-client-update").addEventListener("change", function () {
    var cb = this;
    var on = this.checked;
    var ipt = document.getElementById("ipt-custom-client-path");
    var customPathVal = ipt ? ipt.value.trim() : undefined;
    api("/api/client-update", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: on, customPath: customPathVal })
    }).then(function (res) {
      toast(on ? "已开启禁止客户端自动更新" : "已恢复客户端官方更新策略", "ok");
      renderClientUpdateData(res);
    }).catch(function (e) {
      toast("保存失败：" + e.message, "bad");
      cb.checked = !on;   // 存失败就还原，别显示一个并不生效的状态
    });
  });

  var btnSaveCustomPath = document.getElementById("btn-save-custom-client-path");
  if (btnSaveCustomPath) {
    btnSaveCustomPath.addEventListener("click", function () {
      var ipt = document.getElementById("ipt-custom-client-path");
      var pathVal = ipt ? ipt.value.trim() : "";
      if (!pathVal) {
        toast("请输入有效的自定义客户端路径", "warn");
        return;
      }
      var chk = document.getElementById("chk-client-update");
      var on = chk ? chk.checked : true;
      api("/api/client-update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: on, customPath: pathVal })
      }).then(function (res) {
        toast("自定义路径已保存并应用", "ok");
        renderClientUpdateData(res);
      }).catch(function (e) {
        toast("保存自定义路径失败：" + e.message, "bad");
      });
    });
  }

  var btnClearCustomPath = document.getElementById("btn-clear-custom-client-path");
  if (btnClearCustomPath) {
    btnClearCustomPath.addEventListener("click", function () {
      var ipt = document.getElementById("ipt-custom-client-path");
      if (ipt) ipt.value = "";
      var chk = document.getElementById("chk-client-update");
      var on = chk ? chk.checked : false;
      api("/api/client-update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: on, customPath: "" })
      }).then(function (res) {
        toast("已清空自定义路径，恢复自动探测", "ok");
        renderClientUpdateData(res);
      }).catch(function (e) {
        toast("清空自定义路径失败：" + e.message, "bad");
      });
    });
  }

  var iptCustomPath = document.getElementById("ipt-custom-client-path");
  if (iptCustomPath) {
    iptCustomPath.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        if (btnSaveCustomPath) btnSaveCustomPath.click();
      }
    });
  }

  // 回收站清理。切到这一页时才去读——读回收站要遍历所有条目的元数据，
  // 没必要在启动时就跑。
  document.getElementById("btn-recycle-scan").addEventListener("click", loadRecycle);
  // 三档的清理按钮由 renderRecycle 动态创建（每次重绘都是新元素），
  // 点击处理直接绑在创建处，不在这里 addEventListener——那样会找不到元素。
  document.getElementById("recycle-auto").addEventListener("change", function (ev) {
    api("/api/recycle/auto", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: ev.target.checked })
    }).then(function (auto) {
      applyAutoState(auto);
      toast(auto.enabled ? "已开启自动清理" : "已关闭自动清理", "ok");
    }).catch(function (e) {
      toast("设置失败：" + e.message, "bad");
      ev.target.checked = !ev.target.checked;
    });
  });
  document.getElementById("btn-mig-scan").addEventListener("click", loadMigrate);
  document.getElementById("btn-mig-swap").addEventListener("click", function () {
    var t = migFrom;
    migFrom = migTo;
    migTo = t;
    document.getElementById("mig-direction").textContent = migTitle();
    if (migSurvey) loadMigrate();
  });
  document.getElementById("native").addEventListener("change", function (ev) {
    nativeMode = ev.target.checked;
    loadPreview();
  });

  api("/api/state").then(function (st) {
    state = st;
    selectedIntlModels = (st && st.selectedIntlModels) || [];
    selectedCNModels = (st && st.selectedCNModels) || [];
    if (st && Array.isArray(st.curatedCNModels) && st.curatedCNModels.length) {
      curatedCNModels = st.curatedCNModels;
    }
    selectedFromState = true;
    // 默认选另一套后端——这正是用户打开界面的原因。
    target = st.backends.filter(function (b) {
      return b.id !== st.hostId && st.canRun[b.id];
    }).map(function (b) { return b.id; })[0]
      || (st.backends.filter(function (b) { return st.canRun[b.id]; })[0] || {}).id
      || null;

    renderHeader();
    renderInstalls();
    renderBackends();
    renderBanner();
    renderChatProxy(st);
    syncControlHub();
    if (target) loadPreview();

    // 搬运的默认方向是"从另一侧搬向宿主档位"：两台都装着时，用户想要的
    // 就是把不常开的那一侧的历史并进自己在用的这一侧。
    migTo = st.hostId || null;
    var other = st.backends.filter(function (b) { return b.id !== st.hostId; })[0];
    migFrom = other ? other.id : null;
    document.getElementById("mig-direction").textContent = migFrom ? migTitle() : "";

    // 状态拉取成功后，使用安全快照缓存秒级加载双端资产与额度（不强制穿透缓存）
    loadUsage(true, false);
    // 异步拉取模型连通性雷达快照
    loadModelRadar(true);

    // 每日签到同样在**打开界面时**就跑，不要求用户切到那个 tab。
    //
    // 用户的原话："最好放在第一页，这样一打开 wbmux 就签上了，而不是切换到
    // 那个 tab"。签到的价值全在"顺手"，要求人专门去点一下，多半就断了连签。
    // 与 loadUsage 的区别：这一条会**改账号状态**（领积分、改连签天数），
    // 所以只在"该领"时才发写请求（判断在服务端，见 checkin.AutoClaim），
    // 并且签完要在界面上明说"这次是自动签的"，不能悄悄做。
    loadCheckin(true);

    document.getElementById("btn-mig-scan").disabled = !(migFrom && migTo);

    // 支持 ?doctor=1 直接跑一遍体检。做成深链接是因为"先检查一下"
    // 往往是想反复做的事，可以把它存成书签而不用每次点按钮。
    var q = new URLSearchParams(location.search);
    // 二级页面：深链接先把页面切过去，再触发加载。
    // 页面里内容不长，不需要再滚动定位。
    if (q.get("doctor") === "1") {
      showPage("doctor");
      runDoctor();
    }
    // ?usage=1 同理：额度面板是常看项，存成书签直达。
    if (q.get("usage") === "1") {
      showPage("status");
      loadUsage();
    }
    // ?migrate=1 同理：想同步历史时直接点书签就能到扫描结果。
    if (q.get("migrate") === "1" && migFrom && migTo) {
      showPage("migrate");
      loadMigrate();
    }
  }).catch(function (e) {
    document.getElementById("banner").appendChild(
      el("div", { class: "note bad", text: "无法读取状态：" + e.message })
    );
  });

  // ---------- 桌面快捷方式 ----------
  // 覆盖语义：exe 挪了位置（升级换目录）后重新点一次，快捷方式自动跟上。
  document.getElementById("btn-shortcut").onclick = function () {
    var btn = document.getElementById("btn-shortcut");
    var msg = document.getElementById("side-msg");
    btn.disabled = true;
    msg.textContent = "创建中…";
    api("/api/shortcut", { method: "POST" }).then(function (r) {
      msg.textContent = "已创建";
      toast("桌面快捷方式已就绪：" + r.path, "ok");
    }).catch(function (e) {
      msg.textContent = "失败";
      toast(e.message, "bad");
    }).finally(function () { btn.disabled = false; });
  };

  // ---------- 自助升级 ----------
  // 检查：/api/update/check 对比当前版本与 GitHub 最新 release；
  // 升级：/api/update/apply-stream 流式下载+校验+解包+原位安全替换。
  // 弹窗中直观呈现目标主程序路径、临时下载缓存位置、实时动态进度条与下载速率。
  var updBusy = false;
  document.getElementById("btn-update").onclick = function () {
    if (updBusy) return;
    var btn = document.getElementById("btn-update");
    var msg = document.getElementById("side-msg");
    updBusy = true;
    btn.textContent = "检查中…";
    msg.textContent = "";
    api("/api/update/check").then(function (info) {
      updBusy = false;
      if (info.upToDate) {
        btn.textContent = "检查更新";
        msg.textContent = "已是最新 " + info.latest;
        toast("当前已是最新版本 " + info.latest, "ok");
        return;
      }
      msg.textContent = "发现新版本";
      btn.textContent = "升级到 " + info.latest;
      btn.onclick = function () {
        showUpdateModal(info);
      };
    }).catch(function (e) {
      updBusy = false;
      btn.textContent = "检查更新";
      msg.textContent = "检查失败";
      toast(e.message, "bad");
    });
  };

  // showUpdateModal 弹出可视化升级对话框，展示更新日志、版本说明、下载/替换目录并由用户确认后开始下载
  function showUpdateModal(info) {
    var existing = document.getElementById("update-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay open", id: "update-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:580px" });

    var inProgress = false;
    var isDone = false;
    var eventStream = null;

    function closeModal() {
      if (inProgress) {
        // 正在下载中：无论是点“后台运行”还是点右上角的 X，都不弹系统原生 confirm，
        // 而是直接顺滑收起弹窗，让后台流继续监听下载，并弹轻量 Toast 通知用户。
        overlay.classList.remove("open");
        toast("更新已转入后台下载，完成后会自动通知您", "ok");
        return;
      }
      if (eventStream) {
        eventStream.close();
        eventStream = null;
      }
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }

    // Head
    var head = el("div", { class: "modal-head" }, [
      el("div", { class: "modal-title" }, [
        el("svg", { class: "ico ico-sm", html: '<use href="#i-up"/>' }),
        "发现新版本 · " + info.latest
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal })
    ]);

    // Body
    var body = el("div", { class: "modal-body" });

    // 格式化更新日志 Markdown 文本
    function formatChangelog(raw) {
      if (!raw || !raw.trim()) {
        return '<div style="color:var(--ink-3);font-style:italic">官方未附带更新正文，点击下方按钮开始下载升级。</div>';
      }
      var esc = raw.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
      var lines = esc.split("\n");
      var html = [];
      var inList = false;

      lines.forEach(function (line) {
        var trimmed = line.trim();
        if (trimmed.startsWith("### ")) {
          if (inList) { html.push("</ul>"); inList = false; }
          html.push("<h3>" + trimmed.slice(4) + "</h3>");
        } else if (trimmed.startsWith("## ")) {
          if (inList) { html.push("</ul>"); inList = false; }
          html.push("<h2>" + trimmed.slice(3) + "</h2>");
        } else if (trimmed.startsWith("- ") || trimmed.startsWith("* ")) {
          if (!inList) { html.push("<ul>"); inList = true; }
          var content = trimmed.slice(2);
          content = content.replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>");
          content = content.replace(/`([^`]+)`/g, "<code>$1</code>");
          html.push("<li>" + content + "</li>");
        } else if (trimmed === "") {
          if (inList) { html.push("</ul>"); inList = false; }
        } else {
          if (inList) { html.push("</ul>"); inList = false; }
          var p = line.replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>");
          p = p.replace(/`([^`]+)`/g, "<code>$1</code>");
          html.push("<div>" + p + "</div>");
        }
      });
      if (inList) html.push("</ul>");
      return html.join("");
    }

    // 1. 更新日志卡片
    var changelogWrap = el("div", { class: "upd-changelog-wrap" });
    var pubDate = info.publishedAt ? (new Date(info.publishedAt)).toLocaleDateString() : "";
    var titleText = info.title ? info.title : (info.latest + " 发版更新说明");

    var changelogHeader = el("div", { class: "upd-changelog-header" }, [
      el("span", { class: "upd-changelog-title", text: titleText }),
      pubDate ? el("span", { class: "upd-changelog-date", text: "发布于 " + pubDate }) : null
    ]);
    var changelogBody = el("div", { class: "upd-changelog-body", html: formatChangelog(info.changelog) });

    changelogWrap.appendChild(changelogHeader);
    changelogWrap.appendChild(changelogBody);
    body.appendChild(changelogWrap);

    // 2. 路径透明化卡片
    var pathCard = el("div", { class: "upd-path-card" });
    var valTargetExe = el("span", { class: "upd-path-val", text: "读取中…" });
    var btnOpenExeDir = el("button", { class: "ghost upd-path-btn", text: "📂 打开目录" });
    var valTempDir = el("span", { class: "upd-path-val", text: "读取中…" });
    var btnOpenTempDir = el("button", { class: "ghost upd-path-btn", text: "📂 打开缓存" });

    pathCard.appendChild(el("div", { class: "upd-path-row" }, [
      el("span", { class: "upd-path-label", text: "目标主程序" }),
      valTargetExe,
      btnOpenExeDir
    ]));
    pathCard.appendChild(el("div", { class: "upd-path-row" }, [
      el("span", { class: "upd-path-label", text: "临时下载" }),
      valTempDir,
      btnOpenTempDir
    ]));
    body.appendChild(pathCard);

    // 3. 进度条卡片（未点击下载前隐藏）
    var progressWrap = el("div", { class: "upd-progress-wrap", style: "display:none" });
    var stageLabel = el("div", { class: "upd-progress-stage", text: "正在准备下载环境…" });
    var statsLabel = el("div", { class: "upd-progress-stats", text: "0% · 0 KB/s" });
    var progressHeader = el("div", { class: "upd-progress-header" }, [stageLabel, statsLabel]);

    var progressBar = el("div", { class: "upd-progress-bar" });
    var progressTrack = el("div", { class: "upd-progress-track" }, [progressBar]);

    var detailText = el("div", {
      class: "upd-detail-text",
      text: "采用安全自替换机制：直接原位写入新版 exe，下次启动即生效，期间不影响客户端会话。"
    });

    progressWrap.appendChild(progressHeader);
    progressWrap.appendChild(progressTrack);
    progressWrap.appendChild(detailText);
    body.appendChild(progressWrap);

    // 4. 底部操作区
    var foot = el("div", { class: "modal-foot", style: "justify-content:space-between" });
    var footLeft = el("div", { style: "display:flex;align-items:center;gap:6px" }, [
      info.url ? el("a", {
        class: "upd-release-link",
        href: info.url,
        target: "_blank",
        text: "官方 Release 说明 ↗"
      }) : null
    ]);
    var footRight = el("div", { style: "display:flex;gap:8px" });

    // 确认阶段操作按钮
    var btnDismiss = el("button", { class: "ghost", text: "稍后再说", onclick: closeModal });
    var btnStartDownload = el("button", {
      class: "primary",
      style: "font-weight:600",
      text: "⚡ 立即开始下载并更新"
    });

    // 下载中/下载后操作按钮
    var btnCancel = el("button", { class: "ghost", style: "display:none", text: "后台运行", onclick: closeModal });
    var btnQuitRestart = el("button", {
      class: "primary ok",
      style: "display:none;font-weight:600",
      text: "⚡ 退出以完成更新",
      onclick: function () {
        btnQuitRestart.disabled = true;
        btnQuitRestart.textContent = "正在退出…";
        api("/api/quit", { method: "POST" }).then(function () {
          bye();
        }).catch(function (e) {
          toast("退出请求失败: " + e.message, "bad");
        });
      }
    });

    footRight.appendChild(btnDismiss);
    footRight.appendChild(btnStartDownload);
    footRight.appendChild(btnCancel);
    footRight.appendChild(btnQuitRestart);
    foot.appendChild(footLeft);
    foot.appendChild(footRight);

    card.appendChild(head);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);
    document.body.appendChild(overlay);

    // 读取目标主程序路径与临时目录
    var curTargetExe = "";
    var curTempDir = "";
    api("/api/update/target-info").then(function (ti) {
      curTargetExe = ti.targetExe || "";
      curTempDir = ti.tempDir || "";
      valTargetExe.textContent = curTargetExe;
      valTargetExe.title = curTargetExe;
      valTempDir.textContent = curTempDir;
      valTempDir.title = curTempDir;
    }).catch(function () {
      valTargetExe.textContent = "获取失败";
      valTempDir.textContent = "获取失败";
    });

    btnOpenExeDir.onclick = function () {
      if (!curTargetExe) return;
      btnOpenExeDir.disabled = true;
      api("/api/open-folder", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: curTargetExe })
      }).then(function () {
        toast("已在系统资源管理器中定位主程序", "ok");
      }).catch(function (e) {
        toast("打开失败: " + e.message, "bad");
      }).finally(function () { btnOpenExeDir.disabled = false; });
    };

    btnOpenTempDir.onclick = function () {
      if (!curTempDir) return;
      btnOpenTempDir.disabled = true;
      api("/api/open-folder", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: curTempDir })
      }).then(function () {
        toast("已在系统资源管理器中打开临时缓存", "ok");
      }).catch(function (e) {
        toast("打开失败: " + e.message, "bad");
      }).finally(function () { btnOpenTempDir.disabled = false; });
    };

    function formatBytes(bytes) {
      if (!bytes || bytes <= 0) return "0 B";
      var units = ["B", "KB", "MB", "GB"];
      var i = Math.floor(Math.log(bytes) / Math.log(1024));
      if (i >= units.length) i = units.length - 1;
      return (bytes / Math.pow(1024, i)).toFixed(1) + " " + units[i];
    }

    // 用户在查阅更新日志后，确认开始下载流
    btnStartDownload.onclick = function () {
      btnStartDownload.style.display = "none";
      btnDismiss.style.display = "none";
      btnCancel.style.display = "inline-flex";
      progressWrap.style.display = "flex";
      // 适度缩小更新日志卡片高度，给进度条留出视觉重心
      changelogWrap.style.maxHeight = "150px";

      startDownloadStream();
    };

    function startDownloadStream() {
      inProgress = true;
      progressBar.className = "upd-progress-bar";
      stageLabel.innerHTML = "正在准备连接更新源…";
      statsLabel.textContent = "0%";
      detailText.textContent = "采用安全自替换机制：直接原位写入新版 exe，下次启动即生效，期间不影响客户端会话。";

      var streamUrl = "/api/update/apply-stream?assetURL=" + encodeURIComponent(info.assetURL) +
                      "&assetName=" + encodeURIComponent(info.assetName) +
                      "&sumsURL=" + encodeURIComponent(info.sumsURL) +
                      "&t=" + encodeURIComponent(TOKEN);

      eventStream = new EventSource(streamUrl);

      eventStream.addEventListener("progress", function (e) {
        try {
          var ev = JSON.parse(e.data);
          var pct = Math.max(0, Math.min(100, Math.round(ev.percent || 0)));
          progressBar.style.width = pct + "%";

          if (ev.targetExe && !curTargetExe) {
            curTargetExe = ev.targetExe;
            valTargetExe.textContent = curTargetExe;
            valTargetExe.title = curTargetExe;
          }
          if (ev.tempDir && !curTempDir) {
            curTempDir = ev.tempDir;
            valTempDir.textContent = curTempDir;
            valTempDir.title = curTempDir;
          }

          switch (ev.phase) {
          case "init":
            stageLabel.textContent = "准备连接更新源…";
            statsLabel.textContent = "0%";
            break;
          case "downloading":
            var dStr = formatBytes(ev.downloaded);
            var tStr = ev.total > 0 ? formatBytes(ev.total) : "未知";
            var spd = ev.speed || "0 KB/s";
            stageLabel.textContent = "正在下载更新包 (" + dStr + " / " + tStr + ")…";
            statsLabel.textContent = pct + "% · " + spd;
            break;
          case "verifying":
            stageLabel.textContent = "正在校验 SHA256 官方指纹…";
            statsLabel.textContent = pct + "%";
            break;
          case "extracting":
            stageLabel.textContent = "正在解压更新包…";
            statsLabel.textContent = pct + "%";
            break;
          case "replacing":
            stageLabel.textContent = "正在执行原位安全替换…";
            statsLabel.textContent = pct + "%";
            break;
          default:
            if (ev.message) stageLabel.textContent = ev.message;
          }
        } catch (err) {
          console.error("解析进度失败:", err);
        }
      });

      eventStream.addEventListener("done", function (e) {
        inProgress = false;
        isDone = true;
        if (eventStream) {
          eventStream.close();
          eventStream = null;
        }
        progressBar.style.width = "100%";
        progressBar.className = "upd-progress-bar done";
        stageLabel.innerHTML = '<span style="color:#10b981">🎉 升级已就位 (' + info.latest + ')</span>';
        statsLabel.textContent = "100%";
        detailText.innerHTML = '新版本程序已成功原位替换。从托盘退出或点击右下角按钮退出 wbmux，<b>再次启动</b>即可运行新版。<br>运行中的国内客户端无需重启。';

        btnCancel.textContent = "稍后自行重启";
        btnQuitRestart.style.display = "inline-flex";
        toast("🎉 新版本 " + info.latest + " 已更新就绪！重启即可生效", "ok");
        if (!overlay.classList.contains("open")) {
          overlay.classList.add("open");
        }

        var sideBtn = document.getElementById("btn-update");
        if (sideBtn) {
          sideBtn.textContent = "已就位 " + info.latest;
          sideBtn.disabled = true;
        }
        var sideMsg = document.getElementById("side-msg");
        if (sideMsg) sideMsg.textContent = "重启生效";
        toast("升级已就位，重启即可生效", "ok");
      });

      eventStream.addEventListener("error", function (e) {
        var errMsg = "升级失败，请检查网络";
        if (e.data) {
          try {
            var data = JSON.parse(e.data);
            if (data.error) errMsg = data.error;
          } catch (_) {}
        }
        handleFail(errMsg);
      });

      eventStream.onerror = function () {
        if (!isDone) {
          handleFail("网络连接中断或服务异常");
        }
      };
    }

    function handleFail(msg) {
      inProgress = false;
      if (eventStream) {
        eventStream.close();
        eventStream = null;
      }
      progressBar.className = "upd-progress-bar error";
      stageLabel.innerHTML = '<span style="color:#ef4444">❌ 升级失败</span>';
      statsLabel.textContent = "失败";
      detailText.textContent = "错误详情: " + msg;
      btnCancel.textContent = "关闭";
      var btnRetry = el("button", {
        class: "primary",
        text: "🔄 重试",
        onclick: function () {
          btnRetry.remove();
          startDownloadStream();
        }
      });
      footRight.innerHTML = "";
      footRight.appendChild(btnCancel);
      footRight.appendChild(btnRetry);
      toast(msg, "bad");
    }
  }

  // 生命周期事件流：服务端一退（托盘退出/关闭界面/进程被杀），连接立刻断，
  // 页面先谢幕再尝试自关——--app 模式的窗口允许 window.close()；普通标签页
  // 浏览器不让脚本关，走下面 60 秒心跳的提示页兜底。
  //
  // onerror 不立刻认定退出：EventSource 会按 retry:2000 自动重连，3.5 秒
  // 内连回来（比如服务端或网络抖了一下）就当没事发生。
  var byeOnce = false;
  var esRetry = null;
  function bye() {
    if (byeOnce) return;
    byeOnce = true;
    document.body.innerHTML =
      '<div class="gone-wrap"><div class="note ok" id="gone-note">' +
      '<b>界面已退出。</b>' +
      '<div>重新双击 wbmux.exe 或运行 <code>wbmux gui</code> 可再次打开，地址与令牌保持不变。</div>' +
      '</div></div>';
    setTimeout(function () { window.close(); }, 400);
  }
  try {
    var es = new EventSource("/api/events?t=" + encodeURIComponent(TOKEN));
    es.addEventListener("bye", function () { bye(); });
    es.onopen = function () {
      if (esRetry) { clearTimeout(esRetry); esRetry = null; }
    };
    es.onerror = function () {
      if (byeOnce || esRetry) return;
      esRetry = setTimeout(function () { esRetry = null; bye(); }, 3500);
    };
  } catch (e) { /* EventSource 不可用就走 60 秒心跳兜底 */ }

  // 心跳：服务端默认**不会**自己退出（空闲退出默认关，见 cmd/wbmux/cmd_gui.go），
  // 心跳现在只用来判断"服务端还在不在"，好把话说清楚。
  //
  // 之前只是静默忽略，结果服务端一退出，用户下次刷新看到的就是浏览器那句
  // ERR_CONNECTION_REFUSED——他不知道这是"界面已关闭，重新打开即可"。
  // 这里直接把页面换掉。
  setInterval(function () {
    api("/api/ping").catch(function () {
      if (document.getElementById("gone-note")) return;
      // 这一屏会替换掉整个 body，所以不能依赖任何页面内的容器——
      // 自己带一个居中的壳（.gone-wrap），并且只用在新样式里有的类。
      document.body.innerHTML =
        '<div class="gone-wrap"><div class="note warn" id="gone-note">' +
        '<b>界面已经关闭了。</b>' +
        '<div>wbmux 不会自己退出，所以这一屏多半是：进程被手动结束了、' +
        '或者电脑刚才休眠/重启过。重新双击 wbmux.exe，或运行 ' +
        '<code>wbmux gui</code> 即可恢复，地址与令牌保持不变。</div>' +
        '<div style="margin-top:6px">国内客户端不用跟着重启，' +
        'wbmux 起来之后它就能继续用。</div>' +
        '</div></div>';
    });
  }, 60000);
