  function showCCSwitchExportModal() {
    var existing = document.getElementById("ccswitch-export-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay", id: "ccswitch-export-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:680px" });

    function closeModal() {
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }

    var head = el("div", { class: "modal-head" }, [
      el("div", { style: "display:flex;flex-direction:column;gap:3px;flex:1;min-width:0;padding-right:12px" }, [
        el("div", { class: "modal-title" }, [
          el("svg", { class: "ico ico-sm", html: '<use href="#i-upload"/>' }),
          "一键导入配置至 cc-switch"
        ]),
        el("div", { class: "modal-sub", style: "font-size:11.5px;color:var(--ink-3);line-height:1.45", text: "将 WorkBuddy 免积分、国内核心专享以及自备模型一键写入本地 cc-switch 数据库，让 Claude Code、OpenCode 或 Codex 客户端随心一键切换" })
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal, title: "关闭" })
    ]);

    var body = el("div", { class: "modal-body", style: "display:flex;flex-direction:column;gap:14px" });
    body.appendChild(el("div", { class: "hint", text: "正在探测本地 cc-switch 数据库…" }));

    var btnSubmit = el("button", {
      class: "btn-hero",
      disabled: true,
      text: "写入中…",
      style: "padding:6px 16px;font-size:12.5px"
    });
    var btnClose = el("button", { class: "ghost", text: "取消", onclick: closeModal });

    var foot = el("div", { class: "modal-foot", style: "justify-content:space-between" }, [
      el("span", { style: "font-size:11.5px;color:var(--ink-3)", text: "写入后，直接打开 cc-switch 即可看到新增的 WorkBuddy 提供方。" }),
      el("div", { style: "display:flex;gap:8px" }, [btnSubmit, btnClose])
    ]);

    card.appendChild(head);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);
    overlay.onclick = function (e) { if (e.target === overlay) closeModal(); };
    document.body.appendChild(overlay);
    overlay.offsetHeight;
    overlay.classList.add("open");

    api("/api/providers/export/cc-switch/preview").then(function (res) {
      clear(body);
      var dbPath = res.path || "";
      var found = !!res.found;

      // 1. 状态栏
      var statusBox = el("div", {
        style: "display:flex;align-items:center;justify-content:space-between;padding:8px 12px;border-radius:var(--r-sm);background:" + (found ? "rgba(16,185,129,0.08)" : "rgba(245,158,11,0.08)") + ";border:1px solid " + (found ? "rgba(16,185,129,0.3)" : "rgba(245,158,11,0.3)")
      }, [
        el("div", { style: "font-size:12px;display:flex;align-items:center;gap:6px" }, [
          el("span", { style: "font-weight:600;color:" + (found ? "var(--ok)" : "#fbbf24"), text: found ? "✔ 已就绪" : "⚠ 待确认" }),
          el("span", { style: "color:var(--ink-2)", text: found ? "已找到 cc-switch 数据库文件" : "未在默认路径发现 cc-switch.db" })
        ]),
        el("span", { style: "font-size:11px;color:var(--ink-3);font-family:var(--mono)", text: dbPath })
      ]);
      body.appendChild(statusBox);

      // 2. 目标客户端选择 (App Types)
      var appTypesWrap = el("div", {}, [
        el("div", { style: "font-size:12px;font-weight:600;color:var(--ink-1);margin-bottom:6px", text: "1. 目标客户端 (写入后可在这些客户端中一键切换)" }),
        el("div", { style: "display:flex;gap:12px;flex-wrap:wrap" }, [
          el("label", { class: "check", style: "margin:0" }, [
            el("input", { type: "checkbox", class: "cc-app-type", value: "claude", checked: true }),
            el("span", { text: "Claude Code (CLI)" })
          ]),
          el("label", { class: "check", style: "margin:0" }, [
            el("input", { type: "checkbox", class: "cc-app-type", value: "claude-desktop", checked: true }),
            el("span", { text: "Claude Desktop" })
          ]),
          el("label", { class: "check", style: "margin:0" }, [
            el("input", { type: "checkbox", class: "cc-app-type", value: "opencode", checked: true }),
            el("span", { text: "OpenCode" })
          ]),
          el("label", { class: "check", style: "margin:0" }, [
            el("input", { type: "checkbox", class: "cc-app-type", value: "codex", checked: true }),
            el("span", { text: "Codex" })
          ])
        ])
      ]);
      body.appendChild(appTypesWrap);

      // 3. 选择要导出的模型清单 (Models，支持聚合模式与分端模式可选)
      var currentMode = res.mode || "merged";
      var scrollBox = el("div", {
        style: "max-height:220px;overflow-y:auto;border:1px solid var(--g4);border-radius:var(--r-sm);padding:8px 10px;background:var(--g1);display:grid;grid-template-columns:repeat(auto-fill, minmax(260px, 1fr));gap:6px;transition:opacity 0.2s;scrollbar-width:thin;scrollbar-color:var(--g5) transparent"
      });
      var modelChecks = [];

      function renderModels(list) {
        clear(scrollBox);
        modelChecks = [];
        list.forEach(function (m) {
          var chk = el("input", { type: "checkbox", value: m.id, checked: !!m.selected });
          chk._default = !!m.selected;
          chk._item = m;
          modelChecks.push(chk);

          var tagClass = "ghost";
          var tagText = "BYOK";
          if (m.category === "auto") {
            tagClass = "ok"; tagText = "免积分";
          } else if (m.category === "free") {
            tagClass = "ok"; tagText = "官方限免";
          } else if (m.category === "intl") {
            tagClass = "warn"; tagText = "国际特惠";
          } else if (m.category === "cn") {
            tagClass = "info"; tagText = "国内专享";
          }

          var tagPill = el("span", {
            class: "pill " + tagClass,
            style: "font-size:10px;padding:1.5px 6px;white-space:nowrap;flex-shrink:0;line-height:1.2",
            text: tagText
          });

          var lbl = el("label", {
            class: "check",
            style: "margin:0;font-size:12px;display:flex;align-items:center;justify-content:space-between;gap:8px;padding:4px 6px;border-radius:var(--r-sm);cursor:pointer;transition:background .12s"
          }, [
            el("div", { style: "display:flex;align-items:center;gap:6px;min-width:0;flex:1" }, [
              chk,
              el("span", { style: "overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px", text: m.name, title: m.name })
            ]),
            tagPill
          ]);
          scrollBox.appendChild(lbl);
        });
      }

      function switchMode(newMode) {
        if (currentMode === newMode) return;
        currentMode = newMode;
        btnModeMerged.className = "btn-sub " + (newMode === "merged" ? "active" : "ghost");
        btnModeSplit.className = "btn-sub " + (newMode === "split" ? "active" : "ghost");
        scrollBox.style.opacity = "0.4";
        api("/api/providers/export/cc-switch/preview?mode=" + newMode).then(function (nextRes) {
          scrollBox.style.opacity = "1";
          renderModels(nextRes.models || []);
        });
      }

      var btnModeMerged = el("button", {
        type: "button",
        class: "btn-sub active",
        style: "padding:1px 8px;font-size:11px;cursor:pointer",
        text: "✨ 聚合精选",
        onclick: function() { switchMode("merged"); }
      });
      var btnModeSplit = el("button", {
        type: "button",
        class: "btn-sub ghost",
        style: "padding:1px 8px;font-size:11px;cursor:pointer",
        text: "🌐 分端独立",
        onclick: function() { switchMode("split"); }
      });
      var modePills = el("div", {
        style: "display:inline-flex;background:var(--g3);padding:2px;border-radius:var(--r-sm);gap:2px;align-items:center"
      }, [btnModeMerged, btnModeSplit]);

      var modelsWrap = el("div", {}, [
        el("div", { style: "display:flex;justify-content:space-between;align-items:center;margin-bottom:6px" }, [
          el("div", { style: "font-size:12px;font-weight:600;color:var(--ink-1);display:flex;align-items:center;gap:8px" }, [
            "2. 要导入的模型清单",
            modePills
          ]),
          el("div", { style: "display:flex;gap:6px" }, [
            el("button", { type: "button", class: "ghost", style: "font-size:11px;padding:2px 6px", text: "全选", onclick: function() { modelChecks.forEach(function(c) { c.checked = true; }); } }),
            el("button", { type: "button", class: "ghost", style: "font-size:11px;padding:2px 6px", text: "重置精选", onclick: function() { modelChecks.forEach(function(c) { c.checked = !!c._default; }); } })
          ])
        ]),
        scrollBox
      ]);
      renderModels(res.models || []);
      body.appendChild(modelsWrap);

      // 4. 网关与授权凭据参数
      var advDetails = el("details", { class: "fold", style: "margin:0" }, [
        el("summary", { style: "font-size:11.5px;color:var(--ink-3)", text: "展开网关端点与令牌配置" }),
        el("div", { style: "display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-top:8px" }, [
          el("label", { style: "font-size:11.5px;color:var(--ink-2)" }, [
            "网关 Base URL",
            el("input", { type: "text", id: "cc-exp-baseurl", value: res.baseURL || location.origin, style: "width:100%;font-size:12px;margin-top:4px" })
          ]),
          el("label", { style: "font-size:11.5px;color:var(--ink-2)" }, [
            "访问令牌 (API Key)",
            el("input", { type: "password", id: "cc-exp-token", value: res.token || TOKEN, style: "width:100%;font-size:12px;margin-top:4px" })
          ])
        ])
      ]);
      body.appendChild(advDetails);

      btnSubmit.disabled = false;
      btnSubmit.textContent = "立即写入到 cc-switch";
      btnSubmit.onclick = function () {
        var selectedApps = [];
        document.querySelectorAll(".cc-app-type:checked").forEach(function (c) {
          selectedApps.push(c.value);
        });
        if (!selectedApps.length) {
          toast("请至少选择一个目标客户端（如 Claude Code）", "bad");
          return;
        }

        var selectedModels = [];
        modelChecks.forEach(function (c) {
          if (c.checked) selectedModels.push(c._item);
        });
        if (!selectedModels.length) {
          toast("请至少勾选一个要导入的模型", "bad");
          return;
        }

        var bUrlInput = document.getElementById("cc-exp-baseurl");
        var tokenInput = document.getElementById("cc-exp-token");
        var bUrl = (bUrlInput && bUrlInput.value) || res.baseURL || location.origin;
        var tok = (tokenInput && tokenInput.value) || res.token || TOKEN;

        btnSubmit.disabled = true;
        btnSubmit.textContent = "正在写入数据库…";

        api("/api/providers/export/cc-switch/apply", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            dbPath: dbPath,
            appTypes: selectedApps,
            models: selectedModels,
            baseURL: bUrl,
            token: tok,
            mode: currentMode
          })
        }).then(function (applyRes) {
          closeModal();
          toast(applyRes.note || "成功导入到 cc-switch！", "ok");
        }).catch(function (err) {
          btnSubmit.disabled = false;
          btnSubmit.textContent = "立即写入到 cc-switch";
          toast("写入失败：" + err.message, "bad");
        });
      };
    }).catch(function (err) {
      clear(body);
      body.appendChild(el("div", { class: "note bad", text: "读取 cc-switch 状态失败：" + err.message }));
      btnSubmit.textContent = "重试";
      btnSubmit.disabled = false;
      btnSubmit.onclick = showCCSwitchExportModal;
    });
  }

  function showCCSwitchImportModal() {
    var existing = document.getElementById("ccswitch-import-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay", id: "ccswitch-import-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:680px" });

    function closeModal() {
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }

    var head = el("div", { class: "modal-head" }, [
      el("div", { style: "display:flex;flex-direction:column;gap:3px;flex:1;min-width:0;padding-right:12px" }, [
        el("div", { class: "modal-title" }, [
          el("svg", { class: "ico ico-sm", html: '<use href="#i-download"/>' }),
          "从 cc-switch 提取提供方至 BYOK"
        ]),
        el("div", { class: "modal-sub", style: "font-size:11.5px;color:var(--ink-3);line-height:1.45", text: "从本地 ~/.cc-switch/cc-switch.db 提取第三方模型配置，一键导入为 wbmux 自备提供方" })
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal, title: "关闭" })
    ]);

    var body = el("div", { class: "modal-body", style: "display:flex;flex-direction:column;gap:12px" });
    body.appendChild(el("div", { class: "hint", text: "正在读取 cc-switch 数据库中已配置的提供方…" }));

    var btnSubmit = el("button", { class: "btn-hero", disabled: true, text: "批量导入", style: "padding:6px 16px;font-size:12.5px" });
    var btnClose = el("button", { class: "ghost", text: "取消", onclick: closeModal });

    var foot = el("div", { class: "modal-foot", style: "justify-content:flex-end;gap:8px" }, [btnSubmit, btnClose]);

    card.appendChild(head);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);
    overlay.onclick = function (e) { if (e.target === overlay) closeModal(); };
    document.body.appendChild(overlay);
    overlay.offsetHeight;
    overlay.classList.add("open");

    api("/api/providers/import/cc-switch/preview").then(function (res) {
      clear(body);
      var items = res.items || [];
      if (!items.length) {
        body.appendChild(el("div", { class: "hint", text: "cc-switch 数据库中没有找到第三方提供方配置。" }));
        return;
      }

      var note = el("div", { style: "font-size:12px;color:var(--ink-2)", text: "已发现 " + items.length + " 个可导入提供方，请勾选需要导入至 wbmux 的条目：" });
      body.appendChild(note);

      var checks = [];
      var scroll = el("div", { style: "max-height:240px;overflow-y:auto;border:1px solid var(--g4);border-radius:var(--r-sm);padding:6px 8px;background:var(--g1);display:flex;flex-direction:column;gap:6px;scrollbar-width:thin;scrollbar-color:var(--g5) transparent" });
      items.forEach(function (it) {
        var chk = el("input", { type: "checkbox", checked: true });
        chk._item = it;
        checks.push(chk);

        var row = el("label", { class: "check", style: "margin:0;display:flex;align-items:center;justify-content:space-between;gap:8px;padding:4px 6px;border-radius:4px;cursor:pointer;transition:background .12s" }, [
          el("div", { style: "display:flex;align-items:center;gap:6px;min-width:0;flex:1" }, [
            chk,
            el("span", { style: "font-weight:600;font-size:12px;white-space:nowrap", text: it.name }),
            el("span", { class: "pill ghost", style: "font-size:10px;white-space:nowrap;flex-shrink:0", text: it.appType }),
            el("span", { style: "font-size:11px;color:var(--ink-3);font-family:var(--mono);overflow:hidden;text-overflow:ellipsis;white-space:nowrap", text: it.baseURL })
          ]),
          el("span", { class: "pill " + (it.hasKey ? "ok" : "warn"), style: "font-size:10px;white-space:nowrap;flex-shrink:0", text: it.hasKey ? ("key …" + it.keyTail) : "无Key" })
        ]);
        scroll.appendChild(row);
      });
      body.appendChild(scroll);

      btnSubmit.disabled = false;
      btnSubmit.onclick = function () {
        var selected = [];
        checks.forEach(function (c) {
          if (c.checked) selected.push(c._item);
        });
        if (!selected.length) {
          toast("请至少选择一项", "bad");
          return;
        }
        btnSubmit.disabled = true;
        btnSubmit.textContent = "导入中…";
        api("/api/providers/import/cc-switch/apply", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ items: selected, overwrite: true })
        }).then(function (r) {
          closeModal();
          toast(r.note || "导入成功！", "ok");
          loadProviders(true);
        }).catch(function (err) {
          btnSubmit.disabled = false;
          btnSubmit.textContent = "批量导入";
          toast("导入失败：" + err.message, "bad");
        });
      };
    }).catch(function (err) {
      clear(body);
      body.appendChild(el("div", { class: "note bad", text: "读取 cc-switch 失败：" + err.message }));
    });
  }
