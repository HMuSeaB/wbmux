  // ---------- 成长任务与福利中心 (Growth Hub) ----------
  var growthLastTasks = null;
  var growthBusyFlag = false;
  var growthCurrentFilter = "all";
  var growthHideClaimed = false;

  function inferJsTaskPeriod(code) {
    if (!code) return { period: "once", label: "一次性福利" };
    if (code.indexOf("Sequential_Tasks_") === 0 || code === "school_season") {
      return { period: "milestone", label: "连续阶段" };
    }
    if (code === "chat_5" || code === "Model_chat_GLM5.2" || code === "RichMeow_Chat" ||
        code === "Buddy_App" || code === "Buddy_App_QQ" || code === "automation_1" ||
        code === "expert_5" || code === "Expert_team_use_3") {
      return { period: "daily", label: "每日刷新" };
    }
    return { period: "once", label: "一次性福利" };
  }

  function growthBusy(on) {
    growthBusyFlag = on;
    ["btn-growth-sweep", "btn-growth-refresh", "btn-growth-comp-claim"].forEach(function (id) {
      var b = document.getElementById(id);
      if (b) b.disabled = on;
    });
  }

  function updateGrowthSidebarBadge(data) {
    var b = document.getElementById("nav-growth-badge");
    if (!b) return;
    var items = (data && (data.items || data.tasks)) || [];
    if (!data || items.length === 0) {
      b.textContent = "+1950c";
      b.style.background = "rgba(235,178,58,0.2)";
      b.style.color = "#ebb23a";
      return;
    }
    var pendingCredit = (data.totalCreditPending !== undefined) ? data.totalCreditPending : (data.potential_credit || 0);
    var allClaimed = true;
    items.forEach(function (t) {
      var code = t.code || t.task_code || "";
      var canAuto = (t.can_automate !== undefined) ? t.can_automate : ((t.canAutomate !== undefined) ? t.canAutomate : (code !== "donate_author"));
      var isClaimed = !!t.claimed || t.accept_status === "claimed";
      if (canAuto && !isClaimed) allClaimed = false;
    });
    if (allClaimed) {
      b.textContent = "已达成";
      b.style.background = "rgba(46,196,182,0.18)";
      b.style.color = "var(--ok)";
    } else if (pendingCredit > 0) {
      b.textContent = "+" + pendingCredit + "c";
      b.style.background = "rgba(235,178,58,0.2)";
      b.style.color = "#ebb23a";
    } else {
      b.textContent = "待领奖";
      b.style.background = "rgba(255,107,107,0.2)";
      b.style.color = "#ff6b6b";
    }
  }

  function loadDomesticGrowth(quiet) {
    var list = document.getElementById("growth-tasks-list");
    if (!quiet && list && (!growthLastTasks || (!growthLastTasks.items && !growthLastTasks.tasks))) {
      clear(list);
      list.appendChild(el("div", { class: "hint", style: "grid-column:1/-1" }, [
        el("span", { class: "spinner" }),
        " 正在拉取国内侧最新任务列表与补偿状态…"
      ]));
    }
    if (!quiet) growthBusy(true);

    Promise.all([
      api("/api/growth/domestic/tasks"),
      api("/api/growth/domestic/compensation").catch(function () { return { hasPending: false }; }),
      api("/api/growth/domestic/buddy").catch(function () { return null; }),
      api("/api/growth/domestic/guardian").catch(function () { return null; })
    ]).then(function (results) {
      var taskData = results[0];
      var compData = results[1];
      var buddyData = results[2];
      var guardianData = results[3];
      growthLastTasks = taskData;

      // 更新官方补偿横幅
      var banner = document.getElementById("growth-comp-banner");
      if (banner) {
        var hasComp = (compData && compData.hasPending) ||
          (taskData && taskData.compensation && taskData.compensation.active && !taskData.compensation.claimed && taskData.compensation.credit_num > 0);
        if (hasComp) {
          banner.hidden = false;
          var titleEl = document.getElementById("growth-comp-title");
          var descEl = document.getElementById("growth-comp-desc");
          var rewards = (compData && compData.rewards) || ("+" + (taskData.compensation ? taskData.compensation.credit_num : 500) + " 积分");
          if (titleEl) titleEl.textContent = "🎁 官方服务异常故障补偿待领取 (" + rewards + ")";
          if (descEl && compData && compData.desc) descEl.textContent = compData.desc;
          else if (descEl && taskData.compensation) descEl.textContent = "官方服务异常限时补偿礼包（有效截止至：" + taskData.compensation.end_time + "）";
        } else {
          banner.hidden = true;
        }
      }

      // 更新主战果卡片与侧边栏
      var statCredit = document.getElementById("growth-stat-credit");
      var statEnergy = document.getElementById("growth-stat-energy");
      var lastSync = document.getElementById("growth-last-sync");
      var pendingCredit = (taskData.totalCreditPending !== undefined) ? taskData.totalCreditPending : (taskData.potential_credit || 0);
      var pendingEnergy = (taskData.totalEnergyPending !== undefined) ? taskData.totalEnergyPending : (taskData.potential_energy || 0);
      if (statCredit) statCredit.textContent = "+" + pendingCredit;
      if (statEnergy) statEnergy.textContent = "+" + pendingEnergy;
      if (lastSync) {
        var d = new Date();
        lastSync.textContent = "同步于 " + d.toLocaleTimeString();
      }
      updateGrowthSidebarBadge(taskData);
      if (buddyData) renderBuddyPanel(buddyData, taskData, guardianData);

      // 渲染任务矩阵
      renderDomesticTasks(taskData);
    }).catch(function (e) {
      if (!quiet) toast("拉取成长任务失败：" + e.message, "bad");
      if (list) {
        clear(list);
        list.appendChild(el("div", { class: "note bad", style: "grid-column:1/-1", text: "拉取成长任务失败：" + e.message }));
      }
    }).then(function () {
      if (!quiet) growthBusy(false);
    });
  }

  function renderDomesticTasks(data) {
    var list = document.getElementById("growth-tasks-list");
    var countLabel = document.getElementById("growth-task-count");
    if (!list) return;
    clear(list);

    var items = (data && (data.items || data.tasks)) || [];
    var total = items.length;
    var claimedCount = 0;
    var claimableCount = 0;
    var pendingCount = 0;

    var catCounts = { all: total, daily: 0, once: 0, milestone: 0 };
    items.forEach(function (t) {
      var code = t.code || t.task_code || "";
      var isClaimed = !!t.claimed || t.accept_status === "claimed";
      var isClaimable = !isClaimed && (!!t.claimable || (t.target > 0 && t.current >= t.target) || t.accept_status === "completed");
      if (isClaimed) claimedCount++;
      else if (isClaimable) claimableCount++;
      else pendingCount++;

      var periodInfo = (t.period && t.period_label) ? { period: t.period, label: t.period_label } : inferJsTaskPeriod(code);
      if (catCounts[periodInfo.period] !== undefined) {
        catCounts[periodInfo.period]++;
      }
    });

    // 动态更新分类筛选标签上的任务数量徽标
    var btnTabs = document.querySelectorAll("#growth-filter-tabs .growth-filter-btn");
    btnTabs.forEach(function (btn) {
      var f = btn.getAttribute("data-filter");
      if (f === "all") btn.textContent = "全部任务 (" + catCounts.all + ")";
      else if (f === "daily") btn.textContent = "📅 每日可刷 (" + catCounts.daily + ")";
      else if (f === "once") btn.textContent = "🎁 一次性福利 (" + catCounts.once + ")";
      else if (f === "milestone") btn.textContent = "📱 连续打卡 (" + catCounts.milestone + ")";
    });

    // 动态控制 Hero 区域「一键领取全部待领奖励」按钮
    var btnClaimAll = document.getElementById("btn-growth-claim-all");
    var btnClaimAllText = document.getElementById("btn-growth-claim-all-text");
    if (btnClaimAll) {
      if (claimableCount > 0) {
        btnClaimAll.hidden = false;
        if (btnClaimAllText) {
          btnClaimAllText.textContent = "🎁 一键领取全部待领奖励 (" + claimableCount + " 项可领)";
        }
      } else {
        btnClaimAll.hidden = true;
      }
    }

    if (items.length === 0) {
      if (countLabel) countLabel.textContent = "暂无任务";
      list.appendChild(el("div", { class: "hint", style: "grid-column:1/-1", text: "暂无成长任务数据或国内侧未登录。" }));
      return;
    }

    var filteredItems = items.filter(function (t) {
      var code = t.code || t.task_code || "";
      var isClaimed = !!t.claimed || t.accept_status === "claimed";
      if (growthHideClaimed && isClaimed) return false;
      var periodInfo = (t.period && t.period_label) ? { period: t.period, label: t.period_label } : inferJsTaskPeriod(code);
      if (growthCurrentFilter !== "all" && periodInfo.period !== growthCurrentFilter) {
        return false;
      }
      return true;
    });

    if (countLabel) {
      var filterDesc = (growthCurrentFilter === "all" ? "全部" : (growthCurrentFilter === "daily" ? "每日" : (growthCurrentFilter === "once" ? "一次性" : "连续打卡")));
      var filterNote = (filteredItems.length !== total || growthHideClaimed) ? (" (当前筛选「" + filterDesc + (growthHideClaimed ? "·隐藏已入账" : "") + "」显示 " + filteredItems.length + " 项)") : "";
      countLabel.textContent = "共 " + total + " 项 · 已入账 " + claimedCount + " 项 · 待领奖 " + claimableCount + " 项 · 待扫荡 " + pendingCount + " 项" + filterNote;
    }

    if (filteredItems.length === 0) {
      list.appendChild(el("div", {
        class: "hint",
        style: "grid-column:1/-1;padding:24px;text-align:center",
        text: "当前筛选条件下没有匹配的任务（可切换分类或取消「隐藏已入账」查看）。"
      }));
      return;
    }

    filteredItems.forEach(function (t) {
      var code = t.code || t.task_code || "";
      var name = t.name || t.title || code;
      var desc = t.desc || t.description || t.task_desc || "官方成长加速任务";
      var isClaimed = !!t.claimed || t.accept_status === "claimed";
      var isClaimable = !isClaimed && (!!t.claimable || (t.target > 0 && t.current >= t.target) || t.accept_status === "completed");
      var canAuto = (t.can_automate !== undefined) ? t.can_automate : ((t.canAutomate !== undefined) ? t.canAutomate : (code !== "donate_author"));
      var category = t.category || t.tag || "常用任务";
      var periodInfo = (t.period && t.period_label) ? { period: t.period, label: t.period_label } : inferJsTaskPeriod(code);

      var cardClass = "growth-card";
      if (isClaimed) cardClass += " done";
      else if (isClaimable) cardClass += " claimable";
      var card = el("div", { class: cardClass });

      // Header: 标题 + 周期角标 + 分类标签
      var periodBadge = el("span", {
        class: "growth-period-badge " + periodInfo.period,
        text: periodInfo.label
      });

      var titleWrap = el("div", { style: "display:flex;align-items:center;flex-wrap:wrap;gap:4px" }, [
        el("span", { class: "growth-card-title", text: name }),
        periodBadge
      ]);

      var header = el("div", { class: "growth-card-header" }, [
        titleWrap,
        el("span", { class: "growth-card-cat", text: category })
      ]);

      // Description
      var descEl = el("div", { class: "growth-card-desc", text: desc });

      // Progress bar
      var progStr = t.progress;
      if (!progStr) {
        var cur = (t.current !== undefined) ? t.current : 0;
        var tgt = (t.target !== undefined && t.target > 0) ? t.target : 1;
        progStr = cur + "/" + tgt;
      }
      var pParts = progStr.split("/");
      var curP = parseInt(pParts[0], 10) || 0;
      var maxP = parseInt(pParts[1], 10) || 1;
      var pct = Math.min(100, Math.max(0, Math.round((curP / maxP) * 100)));
      if (isClaimed) pct = 100;

      var progBox = el("div", { class: "growth-progress-box" }, [
        el("div", { class: "growth-progress-bar" }, [
          el("div", { class: "growth-progress-fill", style: "width:" + pct + "%" })
        ]),
        el("div", { class: "growth-progress-text" }, [
          el("span", { text: isClaimed ? "已达成 100%" : ("完成度 " + pct + "%") }),
          el("span", { text: progStr })
        ])
      ]);

      // Footer
      var pillClass = "growth-pill pending";
      var pillText = "待完成";
      if (isClaimed) {
        pillClass = "growth-pill claimed";
        pillText = "✓ 已入账";
      } else if (isClaimable) {
        pillClass = "growth-pill claimable";
        pillText = "🎁 待领奖";
      }

      var pill = el("span", { class: pillClass, text: pillText });

      // Reward Text
      var rewardStr = t.reward_text || t.rewardText;
      if (!rewardStr) {
        var cr = (t.reward_credit !== undefined) ? t.reward_credit : ((t.credit !== undefined) ? t.credit : 0);
        var en = (t.reward_energy !== undefined) ? t.reward_energy : ((t.energy !== undefined) ? t.energy : 0);
        if (cr > 0 && en > 0) rewardStr = "+" + cr + " 积分 · +" + en + " 能量";
        else if (cr > 0) rewardStr = "+" + cr + " 积分";
        else if (en > 0) rewardStr = "+" + en + " 能量";
        else rewardStr = "福利奖励";
      }

      // Action button
      var btnAction;
      if (isClaimed) {
        btnAction = el("button", {
          class: "ghost",
          style: "font-size:11px;padding:4px 8px;opacity:0.65",
          disabled: true,
          title: "该任务已在官方服务器结算入账，防刷机制已生效，重置周期前不会重复请求",
          text: "已完成 (防刷保护)"
        });
      } else if (isClaimable) {
        btnAction = el("button", {
          class: "primary",
          style: "font-size:11px;padding:4px 10px;background:#ebb23a;color:#111;font-weight:600;border:none;box-shadow:0 0 10px rgba(235,178,58,0.3)",
          text: "🎁 领取奖励"
        });
        btnAction.addEventListener("click", function () {
          claimDomesticTask(code, btnAction);
        });
      } else if (canAuto) {
        btnAction = el("button", { class: "ghost", style: "font-size:11px;padding:4px 8px", text: "⚡ 点亮领奖" });
        btnAction.addEventListener("click", function () {
          runDomesticTask(code, btnAction);
        });
      } else {
        btnAction = el("button", { class: "ghost", style: "font-size:11px;padding:4px 8px", text: "微信支持" });
        btnAction.addEventListener("click", function () {
          alert("此项为开源微信 0.01 元捐赠打赏专属任务，需在微信小程序端真实完成。");
        });
      }

      var footer = el("div", { class: "growth-card-footer" }, [
        el("div", { style: "display:flex;align-items:center;gap:6px" }, [
          pill,
          el("span", { class: "growth-reward-badge", text: rewardStr })
        ]),
        btnAction
      ]);

      card.appendChild(header);
      card.appendChild(descEl);
      if (category === "小程序限定" || code.indexOf("Sequential_Tasks_") === 0) {
        card.appendChild(el("div", {
          class: "hint",
          style: "color:var(--text-sub);margin:2px 0 6px 0;font-size:11px;line-height:1.4",
          text: "💡 提示：此项为微信小程序专属任务。微信搜索打开「腾讯 WorkBuddy」小程序发任意 1 句对话后，点击右下角「点亮领奖」即可秒领入账！"
        }));
      }
      card.appendChild(progBox);
      card.appendChild(footer);
      list.appendChild(card);
    });
  }

  function claimDomesticTask(taskCode, btn) {
    if (growthBusyFlag) return;
    if (btn) {
      btn.disabled = true;
      btn.textContent = "正在入账…";
    }
    growthBusy(true);

    api("/api/growth/domestic/claim", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ task_code: taskCode, code: taskCode })
    }).then(function (res) {
      var cr = res.credit_granted || 0;
      var en = res.energy_granted || 0;
      var rewardText = "";
      if (cr > 0 && en > 0) rewardText = "（+" + cr + " 积分 · +" + en + " 能量）";
      else if (cr > 0) rewardText = "（+" + cr + " 积分）";
      else if (en > 0) rewardText = "（+" + en + " 能量）";
      toast("奖励已成功入账 " + rewardText, "ok");

      if (growthLastTasks) {
        var items = growthLastTasks.items || growthLastTasks.tasks || [];
        items.forEach(function (t) {
          if (t.code === taskCode || t.task_code === taskCode) {
            t.claimed = true;
            t.claimable = false;
            t.accept_status = "claimed";
          }
        });
        renderDomesticTasks(growthLastTasks);
      }
      loadDomesticGrowth(true);
    }).catch(function (e) {
      toast("领奖失败：" + e.message, "bad");
      if (btn) {
        btn.disabled = false;
        btn.textContent = "重试领取";
      }
    }).then(function () {
      growthBusy(false);
    });
  }

  function claimAllDomesticTasks() {
    if (growthBusyFlag) return;
    var btn = document.getElementById("btn-growth-claim-all");
    if (btn) {
      btn.disabled = true;
      var textEl = document.getElementById("btn-growth-claim-all-text");
      if (textEl) textEl.textContent = "正在批量领奖…";
    }
    growthBusy(true);

    api("/api/growth/domestic/claim-all", {
      method: "POST"
    }).then(function (res) {
      var n = res.successCount || res.tasksCompleted || 0;
      var cr = res.totalCreditEarned || res.totalCreditGranted || 0;
      var en = res.totalEnergyEarned || res.totalEnergyGranted || 0;
      toast("全部已达成奖励已成功入账！共 " + n + " 项（+" + cr + " 积分 · +" + en + " 能量）", "ok");
      loadDomesticGrowth(false);
    }).catch(function (e) {
      toast("一键领奖失败：" + e.message, "bad");
      if (btn) btn.disabled = false;
    }).then(function () {
      growthBusy(false);
    });
  }

  function runDomesticTask(taskCode, btn) {
    if (growthBusyFlag) return;
    if (btn) {
      btn.disabled = true;
      btn.textContent = "执行中…";
    }
    growthBusy(true);
    api("/api/growth/domestic/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ taskCode: taskCode, task_code: taskCode, code: taskCode })
    }).then(function (res) {
      var name = res.task_name || res.taskName || res.title || res.name || taskCode;
      var msg = res.message || "执行完成";
      if (res.success || res.claimed || res.skipped) {
        toast("任务「" + name + "」" + msg, "ok");

        // 乐观状态同步：如果后端确认已领奖或达成，就地更新内存状态，消除服务端微延迟造成的 0/1 闪烁
        if (growthLastTasks) {
          var items = growthLastTasks.items || growthLastTasks.tasks || [];
          items.forEach(function (it) {
            var itCode = it.code || it.task_code;
            if (itCode === taskCode) {
              if (res.claimed || res.progress_after === "claimed") {
                it.claimed = true;
                it.accept_status = "claimed";
                it.claimable = false;
              } else if (res.progress_after) {
                it.progress = res.progress_after;
              }
            }
          });
          renderDomesticTasks(growthLastTasks);
        }

        if (res.credit > 0 || res.energy > 0 || res.credit_granted > 0) {
          if (usageData) loadUsage(true, true);
        }
      } else {
        toast("执行提示：" + msg, "warn");
      }
      loadDomesticGrowth(true);
    }).catch(function (e) {
      toast("执行失败：" + e.message, "bad");
      loadDomesticGrowth(false);
    }).then(function () {
      growthBusy(false);
    });
  }

  function sweepDomesticGrowth() {
    if (growthBusyFlag) return;
    var consoleBox = document.getElementById("growth-sweep-console");
    var logBox = document.getElementById("growth-sweep-log");
    var statusLabel = document.getElementById("growth-sweep-status");
    var progressLabel = document.getElementById("growth-sweep-progress");

    if (consoleBox) consoleBox.hidden = false;
    if (logBox) clear(logBox);
    if (statusLabel) statusLabel.textContent = "正在注入桌面 5.5.6/Web/小程序三端指纹，全自动顺序执行 17 项任务…";
    if (progressLabel) progressLabel.textContent = "运行中";

    var now = new Date();
    if (logBox) {
      logBox.appendChild(el("div", {
        style: "color:var(--brand);margin-bottom:4px",
        text: "[" + now.toLocaleTimeString() + "] 启动成长任务纯 API 自动化逆向流水引擎，启用 1.05s 真人节律防抖与 SSE 会话探针..."
      }));
    }

    growthBusy(true);

    api("/api/growth/domestic/sweep", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}"
    }).then(function (res) {
      var succCount = (res.success_count !== undefined) ? res.success_count : ((res.successCount !== undefined) ? res.successCount : (res.tasks_completed || 0));
      var creditEarned = (res.total_credit_earned !== undefined) ? res.total_credit_earned : ((res.totalCreditEarned !== undefined) ? res.totalCreditEarned : (res.total_credit_granted || 0));
      var energyEarned = (res.total_energy_earned !== undefined) ? res.total_energy_earned : ((res.totalEnergyEarned !== undefined) ? res.totalEnergyEarned : (res.total_energy_granted || 0));

      if (statusLabel) statusLabel.textContent = "一键扫荡完毕！成功执行 " + succCount + " 项，入账 " + creditEarned + " 积分！";
      if (progressLabel) progressLabel.textContent = "100%";

      var list = res.results || res.details || [];
      if (logBox && list.length > 0) {
        list.forEach(function (r) {
          var isOk = (r.success !== undefined) ? r.success : (r.claimed || r.skipped || (r.credit_granted > 0) || (r.credit > 0));
          var sign = isOk ? "✓ " : "⚠ ";
          var color = isOk ? "var(--ok)" : "var(--warn)";
          var tCode = r.task_code || r.taskCode || r.code || "";
          var tName = r.task_name || r.taskName || r.title || r.name || tCode;
          var msg = r.message || (r.claimed ? "领取成功" : "已完成");
          var cr = (r.credit !== undefined) ? r.credit : (r.credit_granted || 0);
          var en = (r.energy !== undefined) ? r.energy : (r.energy_granted || 0);

          var detail = "[" + tCode + "] " + tName + "：" + msg;
          if (cr > 0) detail += " (入账 +" + cr + " 积分)";
          if (en > 0) detail += " (入账 +" + en + " 能量)";
          logBox.appendChild(el("div", { style: "color:" + color + ";margin:2px 0", text: sign + detail }));
        });

        var summaryTime = new Date();
        logBox.appendChild(el("div", {
          style: "color:#ebb23a;font-weight:600;margin-top:6px;border-top:1px dashed var(--g4);padding-top:4px",
          text: "[" + summaryTime.toLocaleTimeString() + "] 扫荡战报：共完成 " + succCount + " 项，累积领取 " + creditEarned + " 积分、" + energyEarned + " 能量。"
        }));
        if (res.compensation_notice) {
          logBox.appendChild(el("div", {
            style: "color:#38bdf8;font-weight:600;margin-top:3px",
            text: "🎁 " + res.compensation_notice
          }));
        }
        logBox.scrollTop = logBox.scrollHeight;
      }

      toast("扫荡完成！本次已入账 " + creditEarned + " 积分、" + energyEarned + " 能量", "ok");
      if (usageData) loadUsage(true, true);
      loadDomesticGrowth(false);
    }).catch(function (e) {
      if (statusLabel) statusLabel.textContent = "扫荡中断：" + e.message;
      if (progressLabel) progressLabel.textContent = "异常";
      if (logBox) {
        logBox.appendChild(el("div", { style: "color:var(--bad);margin-top:4px", text: "× 扫荡请求异常：" + e.message }));
      }
      toast("扫荡失败：" + e.message, "bad");
      loadDomesticGrowth(false);
    }).then(function () {
      growthBusy(false);
    });
  }

  function claimDomesticCompensation() {
    if (growthBusyFlag) return;
    growthBusy(true);
    api("/api/growth/domestic/compensation/claim", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}"
    }).then(function (res) {
      toast(res.message || "官方故障补偿礼包已领取入账！", "ok");
      var banner = document.getElementById("growth-comp-banner");
      if (banner) banner.hidden = true;
      if (usageData) loadUsage(true, true);
      loadDomesticGrowth(false);
    }).catch(function (e) {
      toast("领取补偿失败：" + e.message, "bad");
    }).then(function () {
      growthBusy(false);
    });
  }

  var growthCurrentBuddyData = null;
  function renderBuddyPanel(bData, tData, gData) {
    growthCurrentBuddyData = bData;
    var b = bData ? bData.buddy : null;
    var tr = bData ? bData.travel : null;

    var nameEl = document.getElementById("buddy-name");
    var rarityEl = document.getElementById("buddy-rarity");
    var descEl = document.getElementById("buddy-desc");
    var avatarEl = document.getElementById("buddy-avatar");
    var fallbackEl = document.getElementById("buddy-avatar-fallback");
    var energyEl = document.getElementById("buddy-energy-val");
    var travelEl = document.getElementById("buddy-travel-status");
    var guardianBadge = document.getElementById("growth-guardian-badge");

    if (guardianBadge) {
      if (gData && gData.running) {
        guardianBadge.style.display = "inline-block";
        var tooltip = "后台全天候守护运行中：\n- 自动收割猫猫旅行积分并重新派遣\n- 每日首次登录自动静默扫荡日常任务";
        if (gData.totalHarvests > 0) {
          tooltip += "\n- 累计已自动收割 " + gData.totalHarvests + " 次 (+" + gData.totalHarvestCredits + " 积分)";
        }
        if (gData.nextCheckAt) {
          var nextT = new Date(gData.nextCheckAt);
          tooltip += "\n- 下次自适应巡检预计：" + nextT.toLocaleTimeString();
        }
        guardianBadge.title = tooltip;
        if (gData.totalHarvestCredits > 0) {
          guardianBadge.textContent = "🟢 自动守护中 (+" + gData.totalHarvestCredits + "分)";
        } else {
          guardianBadge.textContent = "🟢 后台自动守护中";
        }
      } else {
        guardianBadge.style.display = "none";
      }
    }

    if (b) {
      if (nameEl) nameEl.textContent = b.name || "专属 Buddy";
      if (rarityEl) {
        rarityEl.textContent = b.rarity || "UR";
        rarityEl.style.display = "inline-block";
      }
      if (descEl) descEl.textContent = b.soul_desc || "清醒务实，搞钱目标感极强";
      if (avatarEl && (b.thumbnail_url || b.base_static_url)) {
        avatarEl.src = b.thumbnail_url || b.base_static_url;
        avatarEl.style.display = "block";
        if (fallbackEl) fallbackEl.style.display = "none";
      }
    } else {
      if (nameEl) nameEl.textContent = "尚未领养 Buddy";
      if (rarityEl) rarityEl.style.display = "none";
      if (descEl) descEl.textContent = "完成首领猫咪任务即可领养专属 Buddy";
    }

    var actualEnergy = (bData && bData.energy_balance !== undefined) ? bData.energy_balance : null;
    if (energyEl) {
      if (actualEnergy !== null) {
        energyEl.textContent = actualEnergy + " 能量";
        if (actualEnergy < 10) {
          energyEl.title = "开盒需 10 能量，当前差 " + (10 - actualEnergy) + " 能量（完成下方待办任务可获取）";
        } else {
          energyEl.title = "可开启 " + Math.floor(actualEnergy / 10) + " 次盲盒";
        }
      } else {
        var pendingEnergy = (tData && tData.totalEnergyPending !== undefined) ? tData.totalEnergyPending : ((tData && tData.potential_energy) || 0);
        energyEl.textContent = pendingEnergy + " 能量";
      }
    }

    if (travelEl) {
      if (!tr || tr.state === "idle") {
        travelEl.textContent = "🐾 猫咪闲置中 (可派遣出发)";
        travelEl.style.color = "#38bdf8";
        travelEl.style.background = "rgba(56,189,248,0.15)";
      } else if (tr.state === "traveling") {
        var loc = (tr.location && tr.location.name) || "咖啡馆";
        var remainMin = Math.max(0, Math.round(((tr.arrive_at || 0) - (tr.server_now || Math.floor(Date.now() / 1000))) / 60));
        travelEl.textContent = "✈️ 「" + loc + "」旅行中 (预计还剩 " + remainMin + " 分钟)";
        travelEl.style.color = "#f59e0b";
        travelEl.style.background = "rgba(245,158,11,0.15)";
      } else if (tr.state === "arrived") {
        travelEl.textContent = "🎁 已归来！信件与积分待领取";
        travelEl.style.color = "#22c55e";
        travelEl.style.background = "rgba(34,197,94,0.15)";
      }
    }
  }

  function openBuddyBlindBox(count) {
    if (growthBusyFlag) return;
    growthBusy(true);
    toast("正在开启 " + count + " 个 Buddy 盲盒…", "info");
    api("/api/growth/domestic/buddy/open", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ count: count })
    }).then(function (res) {
      toast(res.message || "盲盒开启成功！", "ok");
      loadDomesticGrowth(false);
    }).catch(function (e) {
      toast("开启盲盒失败：" + e.message, "bad");
    }).then(function () {
      growthBusy(false);
    });
  }

  function travelBuddyAction(action) {
    if (growthBusyFlag) return;
    growthBusy(true);
    api("/api/growth/domestic/buddy/travel", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action: action || "auto", location_id: 1 })
    }).then(function (res) {
      toast(res.message || "旅行打理完成！", "ok");
      loadDomesticGrowth(false);
    }).catch(function (e) {
      toast("旅行打理异常：" + e.message, "bad");
    }).then(function () {
      growthBusy(false);
    });
  }
