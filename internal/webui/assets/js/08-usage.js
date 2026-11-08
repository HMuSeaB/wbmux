  // ---------- 额度与计费 ----------

  var usageData = null;
  var usageHistoryData = null;
  var usageHistoryLoading = false;
  try {
    var rawUsageCache = localStorage.getItem("wbmux_usage_cache");
    if (rawUsageCache) {
      usageData = JSON.parse(rawUsageCache);
    }
  } catch (e) {}

  // showTokenImportModal 弹出免装国际客户端录入凭据的模态窗口（支持官方拟真一键登录与手动导入）。
  function showTokenImportModal(side) {
    side = side || "intl";
    var existing = document.getElementById("token-import-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay", id: "token-import-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:540px" });

    // Head
    var head = el("div", { class: "modal-head" }, [
      el("div", { class: "modal-title" }, [
        el("svg", { class: "ico ico-sm", html: '<use href="#i-layers"/>' }),
        "国际版凭据录入 · 多方式获取"
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal })
    ]);

    // Tab 导航栏
    var currentTab = "auto"; // "auto" | "manual"
    var tabAuto = el("button", {
      class: "pill ok",
      style: "cursor:pointer;font-size:12px;padding:6px 14px;border:1px solid rgba(16,185,129,0.35);background:rgba(16,185,129,0.12)",
      text: "⚡ 官方拟真一键获取 (推荐)"
    });
    var tabManual = el("button", {
      class: "pill",
      style: "cursor:pointer;font-size:12px;padding:6px 14px;border:1px solid var(--g4);background:transparent",
      text: "📋 手动粘贴 Token (备用)"
    });
    var tabNav = el("div", { style: "display:flex;gap:10px;margin-bottom:14px" }, [tabAuto, tabManual]);

    // Body
    var body = el("div", { class: "modal-body" });
    body.appendChild(tabNav);

    // ================== Pane 1: 拟真自动登录 ==================
    var paneAuto = el("div", { style: "display:flex;flex-direction:column;gap:12px" });
    var introCard = el("div", {
      style: "background:rgba(99,102,241,0.08);border:1px solid rgba(99,102,241,0.22);border-radius:var(--r-sm);padding:12px 14px;font-size:12.5px;line-height:1.6;color:var(--ink-2)"
    }, [
      el("div", { style: "font-weight:600;color:var(--ink-1);margin-bottom:6px;display:flex;align-items:center;gap:6px" }, [
        el("span", { style: "font-size:14px", text: "🌐" }),
        "全拟真协议流程（免抓包 · 免开 F12 · 免装客户端）"
      ]),
      el("div", { text: "1. 点击下方按钮，wbmux 将向官方申请原生授权并在默认浏览器中唤起登录页；" }),
      el("div", { text: "2. 在官方页面完成登录（支持微信扫码、邮箱、密码等任意方式）；" }),
      el("div", { text: "3. wbmux 后台安全捕获真实 AccessToken 与 RefreshToken，自动纳管入库并激活！" })
    ]);
    paneAuto.appendChild(introCard);

    // 动作与状态区
    var authActionBox = el("div", { style: "display:flex;flex-direction:column;align-items:center;gap:12px;padding:16px;background:rgba(255,255,255,0.02);border:1px solid var(--g5);border-radius:var(--r-sm)" });

    var btnLaunchAuth = el("button", {
      class: "btn-hero",
      style: "width:100%;justify-content:center;padding:10px 18px;font-size:13.5px;font-weight:600",
      text: "🚀 启动官方正规登录获取"
    });
    authActionBox.appendChild(btnLaunchAuth);

    // 等待中状态盒子
    var waitingBox = el("div", {
      style: "display:none;width:100%;flex-direction:column;align-items:center;gap:10px;text-align:center"
    });
    var spinner = el("div", {
      style: "width:28px;height:28px;border:3px solid rgba(99,102,241,0.2);border-top-color:var(--brand);border-radius:50%;animation:spin 1s linear infinite"
    });
    var waitingText = el("div", {
      style: "font-size:13px;font-weight:600;color:var(--ink-1)",
      text: "正在等待您在浏览器中完成登录…"
    });
    var countdownText = el("div", {
      style: "font-size:11.5px;color:var(--ink-3);font-family:var(--mono)",
      text: "有效等待时间：05:00"
    });
    var btnReopen = el("button", {
      class: "ghost",
      style: "font-size:11.5px;padding:4px 10px",
      text: "再次打开官方登录页 ↗"
    });
    var btnCopyLink = el("button", {
      class: "ghost",
      style: "font-size:11.5px;padding:4px 10px",
      text: "复制登录链接"
    });
    var linksRow = el("div", { style: "display:flex;gap:8px;margin-top:4px" }, [btnReopen, btnCopyLink]);

    waitingBox.appendChild(spinner);
    waitingBox.appendChild(waitingText);
    waitingBox.appendChild(countdownText);
    waitingBox.appendChild(linksRow);
    authActionBox.appendChild(waitingBox);

    // 成功/失败反馈盒子
    var resultBox = el("div", { style: "display:none;width:100%;font-size:12.5px;text-align:center;padding:8px 0" });
    authActionBox.appendChild(resultBox);

    paneAuto.appendChild(authActionBox);

    // ================== Pane 2: 手动输入 Token ==================
    var paneManual = el("div", { style: "display:none;flex-direction:column;gap:12px" });
    var guideBox = el("div", {
      style: "background:rgba(99,102,241,0.08);border:1px solid rgba(99,102,241,0.22);border-radius:var(--r-sm);padding:10px 12px;font-size:12px;line-height:1.5;color:var(--ink-2)"
    }, [
      el("div", { style: "font-weight:600;color:var(--ink-1);margin-bottom:4px", text: "💡 手动提取 Token 指引：" }),
      el("div", { text: "1. 浏览器打开并登录国际官网 codebuddy.ai；" }),
      el("div", { text: "2. 按 F12 打开开发者工具 → 切换到「网络 (Network)」标签；" }),
      el("div", { text: "3. 复制任意 API 请求头中的 Bearer Token。" })
    ]);
    paneManual.appendChild(guideBox);

    var tokenLabel = el("label", { style: "font-size:12px;font-weight:600;color:var(--ink-2);display:flex;flex-direction:column;gap:5px" }, [
      "AccessToken (必填)：",
      el("textarea", {
        id: "inp-import-token",
        rows: "4",
        placeholder: "粘贴以 eyJ... 开头的长文本 Token（或完整 Bearer eyJ...）",
        style: "width:100%;box-sizing:border-box;font-family:var(--mono);font-size:11.5px;padding:8px;background:rgba(0,0,0,0.3);border:1px solid var(--g4);border-radius:var(--r-sm);color:var(--ink-1);resize:vertical"
      })
    ]);
    paneManual.appendChild(tokenLabel);

    var uidLabel = el("label", { style: "font-size:12px;font-weight:600;color:var(--ink-2);display:flex;flex-direction:column;gap:5px" }, [
      "UID (可选，留空将自动从 Token 提取)：",
      el("input", {
        id: "inp-import-uid",
        type: "text",
        placeholder: "如：fd46b846-da8f-426b-8af2-...",
        style: "width:100%;box-sizing:border-box;font-family:var(--mono);font-size:12px;padding:6px 8px;background:rgba(0,0,0,0.3);border:1px solid var(--g4);border-radius:var(--r-sm);color:var(--ink-1)"
      })
    ]);
    paneManual.appendChild(uidLabel);

    var btnSubmitManual = el("button", {
      class: "primary",
      style: "width:100%;justify-content:center;padding:8px;margin-top:4px",
      text: "验证并保存凭据"
    });
    var manualStatusBox = el("div", { style: "display:none;font-size:12px;margin-top:2px" });
    paneManual.appendChild(btnSubmitManual);
    paneManual.appendChild(manualStatusBox);

    body.appendChild(paneAuto);
    body.appendChild(paneManual);

    // 防风控标识提示
    var securityNote = el("div", {
      style: "font-size:11.5px;color:var(--ink-3);display:flex;align-items:center;gap:6px;margin-top:12px"
    }, [
      el("svg", { class: "ico ico-sm", html: '<use href="#i-check"/>' }),
      "已启用 Telemetry Guardian：自动生成稳定机器指纹与高拟真 UA，确保账号安全。"
    ]);
    body.appendChild(securityNote);

    // Tab 切换逻辑
    tabAuto.onclick = function () {
      currentTab = "auto";
      tabAuto.style.background = "rgba(16,185,129,0.12)";
      tabAuto.style.borderColor = "rgba(16,185,129,0.35)";
      tabAuto.className = "pill ok";
      tabManual.style.background = "transparent";
      tabManual.style.borderColor = "var(--g4)";
      tabManual.className = "pill";
      paneAuto.style.display = "flex";
      paneManual.style.display = "none";
    };
    tabManual.onclick = function () {
      currentTab = "manual";
      tabManual.style.background = "rgba(99,102,241,0.12)";
      tabManual.style.borderColor = "rgba(99,102,241,0.35)";
      tabManual.className = "pill ok";
      tabAuto.style.background = "transparent";
      tabAuto.style.borderColor = "var(--g4)";
      tabAuto.className = "pill";
      paneAuto.style.display = "none";
      paneManual.style.display = "flex";
    };

    // ================== 拟真登录流程状态管理 ==================
    var activePollTimer = null;
    var activeCountTimer = null;
    var currentAuthURL = "";

    function stopAuthTimers() {
      if (activePollTimer) {
        clearInterval(activePollTimer);
        activePollTimer = null;
      }
      if (activeCountTimer) {
        clearInterval(activeCountTimer);
        activeCountTimer = null;
      }
    }

    btnLaunchAuth.onclick = function () {
      btnLaunchAuth.disabled = true;
      btnLaunchAuth.textContent = "正在向官方申请授权…";
      resultBox.style.display = "none";

      api("/api/auth/start", { method: "POST" }).then(function (res) {
        if (!res.ok || !res.state || !res.authUrl) {
          throw new Error("官方返回异常授权数据");
        }
        currentAuthURL = res.authUrl;
        btnLaunchAuth.style.display = "none";
        waitingBox.style.display = "flex";

        // 尝试自动弹出浏览器打开官方登录页
        try {
          window.open(res.authUrl, "_blank");
        } catch (e) {
          console.warn("未能自动唤起新窗口，请手动点击按钮打开", e);
        }

        // 绑定辅助按钮
        btnReopen.onclick = function () {
          window.open(currentAuthURL, "_blank");
        };
        btnCopyLink.onclick = function () {
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(currentAuthURL).then(function () {
              toast("登录链接已复制到剪贴板", "ok");
            }).catch(function () {
              prompt("请手动复制以下登录链接：", currentAuthURL);
            });
          } else {
            prompt("请手动复制以下登录链接：", currentAuthURL);
          }
        };


        // 倒计时
        var remainingSec = 300;
        function updateCount() {
          var m = Math.floor(remainingSec / 60);
          var s = remainingSec % 60;
          countdownText.textContent = "有效等待时间：" + (m < 10 ? "0" + m : m) + ":" + (s < 10 ? "0" + s : s);
          if (remainingSec <= 0) {
            stopAuthTimers();
            waitingBox.style.display = "none";
            btnLaunchAuth.style.display = "block";
            btnLaunchAuth.disabled = false;
            btnLaunchAuth.textContent = "重新启动官方登录获取";
            resultBox.style.display = "block";
            resultBox.className = "note bad";
            resultBox.textContent = "等待超时（5分钟），请重新点击发起。";
          }
          remainingSec--;
        }
        updateCount();
        activeCountTimer = setInterval(updateCount, 1000);

        // 轮询官方登录态
        activePollTimer = setInterval(function () {
          api("/api/auth/poll?state=" + encodeURIComponent(res.state)).then(function (pollRes) {
            if (pollRes.status === "success") {
              stopAuthTimers();
              waitingBox.style.display = "none";
              resultBox.style.display = "block";
              resultBox.className = "note ok";
              resultBox.textContent = "🎉 官方授权成功！已纳管并激活账号：" + (pollRes.nickname || pollRes.uid);
              toast("官方拟真授权成功！已设为当前活跃账号", "ok");
              setTimeout(function () {
                closeModal();
                loadUsage(true, true);
              }, 1200);
            } else if (pollRes.status === "failed" || pollRes.status === "expired") {
              stopAuthTimers();
              waitingBox.style.display = "none";
              btnLaunchAuth.style.display = "block";
              btnLaunchAuth.disabled = false;
              btnLaunchAuth.textContent = "重新启动官方登录获取";
              resultBox.style.display = "block";
              resultBox.className = "note bad";
              resultBox.textContent = pollRes.message || "登录流程已结束，请重新发起";
            }
          }).catch(function (err) {
            console.warn("轮询暂无响应:", err);
          });
        }, 2000);

      }).catch(function (err) {
        btnLaunchAuth.disabled = false;
        btnLaunchAuth.textContent = "重新启动官方登录获取";
        resultBox.style.display = "block";
        resultBox.className = "note bad";
        resultBox.textContent = "启动登录失败：" + err.message;
      });
    };

    // 手动验证并保存
    btnSubmitManual.onclick = function () {
      var tok = (document.getElementById("inp-import-token").value || "").trim();
      var uid = (document.getElementById("inp-import-uid").value || "").trim();
      if (!tok) {
        toast("请先粘贴 AccessToken", "bad");
        return;
      }
      btnSubmitManual.disabled = true;
      btnSubmitManual.textContent = "正在官方链路验证…";
      manualStatusBox.style.display = "block";
      manualStatusBox.className = "hint";
      manualStatusBox.textContent = "正在校验 Token 有效性并初始化机器指纹…";

      api("/api/credentials/import", {
        method: "POST",
        body: JSON.stringify({ side: side, token: tok, uid: uid })
      }).then(function (r) {
        toast("凭据导入成功！欢迎 " + (r.nickname || r.uid), "ok");
        closeModal();
        loadUsage(true, true);
      }).catch(function (e) {
        manualStatusBox.className = "note bad";
        manualStatusBox.textContent = "验证失败：" + e.message;
        btnSubmitManual.disabled = false;
        btnSubmitManual.textContent = "重新验证并保存";
      });
    };

    // Foot
    var btnClose = el("button", { class: "ghost", text: "关闭", onclick: closeModal });
    var foot = el("div", { class: "modal-foot", style: "justify-content:flex-end" }, [btnClose]);

    card.appendChild(head);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);

    overlay.onclick = function (e) {
      if (e.target === overlay) closeModal();
    };

    function closeModal() {
      stopAuthTimers();
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }

    document.body.appendChild(overlay);
    overlay.offsetHeight;
    overlay.classList.add("open");
  }


  // showAccountsManagerModal 弹出国际版多账号管理与一键切换窗口。
  function showAccountsManagerModal(side) {
    side = side || "intl";
    var existing = document.getElementById("accounts-manager-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay", id: "accounts-manager-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:560px" });

    // Head
    var head = el("div", { class: "modal-head" }, [
      el("div", { class: "modal-title" }, [
        el("svg", { class: "ico ico-sm", html: '<use href="#i-layers"/>' }),
        "国际版多账号管理 (Multi-Account Vault)"
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal })
    ]);

    // Body
    var body = el("div", { class: "modal-body" });
    var listBox = el("div", { style: "display:flex;flex-direction:column;gap:8px;max-height:320px;overflow-y:auto" });
    body.appendChild(listBox);

    listBox.appendChild(el("div", { class: "hint", text: "正在读取已保存账号…" }));

    function renderList() {
      clear(listBox);
      api("/api/accounts/list?side=" + side).then(function (res) {
        var accs = res.accounts || [];
        if (!accs.length) {
          listBox.appendChild(el("div", { class: "hint", text: "当前暂无已保存账号，可点击下方「+ 添加新账号」录入。" }));
          return;
        }
        accs.forEach(function (a) {
          var row = el("div", {
            style: "background:rgba(255,255,255,0.025);border:1px solid " + (a.active ? "rgba(16,185,129,0.35)" : "var(--g5)") +
                   ";border-radius:var(--r-sm);padding:10px 14px;display:flex;align-items:center;justify-content:space-between;gap:12px;transition:all .15s"
          });
          var tagPills = [];
          if (a.active) {
            tagPills.push(el("span", { class: "pill ok", text: "当前活跃" }));
          }
          if (a.hasRefreshToken) {
            tagPills.push(el("span", { class: "pill", style: "background:rgba(99,102,241,0.18);border:1px solid rgba(99,102,241,0.38);color:#a5b4fc", text: "⚡ 自动无感保鲜" }));
          }
          if (a.cooldownSec > 0) {
            var min = Math.ceil(a.cooldownSec / 60);
            tagPills.push(el("span", { class: "pill warn", style: "font-size:10.5px;padding:1px 6px", text: "⏳ 限流冷却中 (" + min + "m)" }));
          } else {
            tagPills.push(el("span", { class: "pill ok", style: "font-size:10.5px;padding:1px 6px", text: "✅ 就绪" }));
          }
          if (a.expiresAt > 0) {
            var rem = a.expiresAt - Date.now();
            if (rem > 0) {
              var days = Math.ceil(rem / 86400000);
              tagPills.push(el("span", { class: "pill ok", style: "font-size:10.5px;padding:1px 6px", text: "剩 " + days + " 天" }));
            } else {
              tagPills.push(el("span", { class: "pill bad", style: "font-size:10.5px;padding:1px 6px", text: "已到期" }));
            }
          }

          var left = el("div", { style: "display:flex;flex-direction:column;gap:4px;overflow:hidden" }, [
            el("div", { style: "display:flex;align-items:center;gap:6px;flex-wrap:wrap" }, [
              el("span", { style: "font-weight:600;font-size:13px;color:var(--ink-1)", text: a.nickname || a.uid })
            ].concat(tagPills)),
            el("div", { style: "font-size:11.5px;color:var(--ink-3);font-family:var(--mono)" }, [
              "UID: " + (a.uid ? (a.uid.length > 20 ? a.uid.slice(0, 18) + "…" : a.uid) : "未知"),
              " · Token: " + (a.tokenMask || "******")
            ])
          ]);

          var right = el("div", { style: "display:flex;align-items:center;gap:6px;flex-shrink:0" });
          if (a.hasRefreshToken) {
            var btnRefresh = el("button", {
              class: "ghost",
              style: "padding:3px 8px;font-size:11px;color:#a5b4fc",
              text: "保鲜",
              title: "使用 RefreshToken 立即无感换新 AccessToken",
              onclick: function () {
                btnRefresh.disabled = true;
                btnRefresh.textContent = "保鲜中…";
                api("/api/accounts/refresh", {
                  method: "POST",
                  body: JSON.stringify({ side: side, id: a.id })
                }).then(function () {
                  toast("已成功为「" + (a.nickname || a.uid) + "」保鲜换新 Token", "ok");
                  renderList();
                  loadUsage(true, true);
                }).catch(function (e) {
                  toast("保鲜失败：" + e.message, "bad");
                  btnRefresh.disabled = false;
                  btnRefresh.textContent = "保鲜";
                });
              }
            });
            right.appendChild(btnRefresh);
          }
          if (!a.active) {
            var btnSwitch = el("button", {
              class: "ghost",
              style: "padding:3px 10px;font-size:11px",
              text: "设为当前",
              onclick: function () {
                btnSwitch.disabled = true;
                btnSwitch.textContent = "切换中…";
                api("/api/accounts/switch", {
                  method: "POST",
                  body: JSON.stringify({ side: side, id: a.id })
                }).then(function () {
                  toast("已切换至 " + (a.nickname || a.uid), "ok");
                  renderList();
                  loadUsage(true, true);
                }).catch(function (e) {
                  toast("切换失败：" + e.message, "bad");
                  btnSwitch.disabled = false;
                  btnSwitch.textContent = "设为当前";
                });
              }
            });
            right.appendChild(btnSwitch);
          }

          var btnDel = el("button", {
            class: "ghost",
            style: "padding:3px 8px;font-size:11px;color:var(--bad)",
            text: "删除",
            onclick: function () {
              if (!confirm("确定从本地移除账号「" + (a.nickname || a.uid) + "」吗？")) return;
              api("/api/accounts/delete", {
                method: "POST",
                body: JSON.stringify({ side: side, id: a.id })
              }).then(function () {
                toast("账号已移除", "ok");
                renderList();
                loadUsage(true, true);
              }).catch(function (e) {
                toast("删除失败：" + e.message, "bad");
              });
            }
          });
          right.appendChild(btnDel);

          row.appendChild(left);
          row.appendChild(right);
          listBox.appendChild(row);
        });
      }).catch(function (e) {
        clear(listBox);
        listBox.appendChild(el("div", { class: "note bad", text: "读取账号失败：" + e.message }));
      });
    }

    renderList();

    // Foot
    var btnAdd = el("button", {
      class: "btn-hero",
      style: "padding:5px 14px;font-size:12px;height:auto",
      text: "+ 添加新账号",
      onclick: function () {
        closeModal();
        showTokenImportModal(side);
      }
    });
    var btnClose = el("button", { class: "ghost", text: "关闭", onclick: closeModal });
    var foot = el("div", { class: "modal-foot", style: "justify-content:space-between" }, [
      el("div", { style: "font-size:11.5px;color:var(--ink-3);display:flex;align-items:center;gap:5px" }, [
        el("svg", { class: "ico ico-sm", html: '<use href="#i-check"/>' }),
        "切换后，无缝模型注入与遥测心跳将即刻自动生效。"
      ]),
      el("div", { style: "display:flex;gap:8px" }, [
        el("button", {
          class: "ghost",
          style: "padding:5px 12px;font-size:12px;color:var(--accent);border-color:rgba(var(--accent-rgb),0.35)",
          text: "🛡️ 导出/备份凭据",
          onclick: function () {
            showCredentialExportModal(side);
          }
        }),
        btnAdd,
        btnClose
      ])
    ]);

    card.appendChild(head);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);

    overlay.onclick = function (e) {
      if (e.target === overlay) closeModal();
    };

    function closeModal() {
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }

    document.body.appendChild(overlay);
    overlay.offsetHeight;
    overlay.classList.add("open");
  }

  // showCredentialExportModal 弹出国际版凭据安全导出与备份窗口
  function showCredentialExportModal(side) {
    side = side || "intl";
    var existing = document.getElementById("cred-export-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay", id: "cred-export-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:620px" });

    function closeModal() {
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }

    var head = el("div", { class: "modal-head" }, [
      el("div", { class: "modal-title" }, [
        el("svg", { class: "ico ico-sm", html: '<use href="#i-shield"/>' }),
        side === "intl" ? "国际版凭据安全导出与灾备备份" : "国内版凭据安全导出"
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal })
    ]);

    var body = el("div", { class: "modal-body" });
    body.appendChild(el("div", { class: "hint", text: "正在安全提取当前登录凭据…" }));

    card.appendChild(head);
    card.appendChild(body);
    overlay.appendChild(card);
    overlay.onclick = function (e) {
      if (e.target === overlay) closeModal();
    };
    document.body.appendChild(overlay);
    overlay.offsetHeight;
    overlay.classList.add("open");

    function loadAndRender(targetAccId, isAll) {
      clear(body);
      body.appendChild(el("div", { class: "hint", text: "正在安全提取凭据…" }));

      var url = "/api/credentials/export?side=" + side;
      if (isAll) url += "&all=true";
      else if (targetAccId) url += "&account_id=" + encodeURIComponent(targetAccId);

      api(url).then(function (res) {
        clear(body);
        if (!res) {
          body.appendChild(el("div", { class: "bad", text: "未获取到有效凭据" }));
          return;
        }

        // 1. 账号选择与全选工具条
        var selectorRow = el("div", {
          style: "display:flex;align-items:center;gap:10px;margin-bottom:12px;padding:8px 12px;background:rgba(255,255,255,0.02);border:1px solid rgba(255,255,255,0.06);border-radius:var(--r-sm)"
        });
        selectorRow.appendChild(el("span", { style: "font-size:12px;color:var(--g8);white-space:nowrap", text: "导出目标：" }));

        var selectEl = el("select", {
          style: "flex:1;background:rgba(0,0,0,0.3);border:1px solid rgba(255,255,255,0.15);color:var(--text);border-radius:var(--r-sm);padding:4px 8px;font-size:12px"
        });

        var opts = res.options || [];
        opts.forEach(function (o) {
          var opt = el("option", {
            value: o.id,
            text: (o.active ? "⭐ [当前活跃] " : "👤 ") + (o.nickname || o.uid) + (o.uid ? " (" + o.uid.slice(0, 8) + ")" : "")
          });
          if (!isAll && (o.id === res.selectedId || (res.selectedId === "native_current" && o.id === "native_current"))) {
            opt.selected = true;
          }
          selectEl.appendChild(opt);
        });

        var allOpt = el("option", {
          value: "__ALL__",
          text: "📦 [全选] 导出全部账号完整备份 (合辑 " + (opts.length > 0 ? opts.length : 1) + " 个)"
        });
        if (isAll) allOpt.selected = true;
        selectEl.appendChild(allOpt);

        selectEl.onchange = function () {
          if (selectEl.value === "__ALL__") {
            loadAndRender(null, true);
          } else {
            loadAndRender(selectEl.value, false);
          }
        };
        selectorRow.appendChild(selectEl);

        var btnQuickAll = el("button", {
          class: isAll ? "btn-hero" : "ghost",
          style: "padding:4px 10px;font-size:11.5px;white-space:nowrap",
          text: isAll ? "✓ 当前为全选" : "全选全部账号"
        });
        btnQuickAll.onclick = function () {
          loadAndRender(null, !isAll);
        };
        selectorRow.appendChild(btnQuickAll);

        body.appendChild(selectorRow);

        // 2. 账号概要卡片
        var acc = res.account || {};
        var isAllMode = !!isAll;
        var rawJson = isAllMode
          ? JSON.stringify(res.allBackup || res, null, 2)
          : JSON.stringify(res.officialFormat || res, null, 2);

        var banner = el("div", {
          style: "background:rgba(255,255,255,0.03);border:1px solid rgba(255,255,255,0.08);border-radius:var(--r-sm);padding:10px 14px;margin-bottom:12px"
        }, [
          el("div", { style: "display:flex;justify-content:space-between;align-items:center;margin-bottom:4px" }, [
            el("div", { style: "font-weight:600;font-size:13px;color:var(--text)" }, [
              isAllMode ? "全量灾备合辑：" : "凭据账号：",
              el("span", { style: "color:var(--accent)" }, isAllMode ? ("包含 " + (opts.length || 1) + " 个账号配置") : (acc.nickname || acc.uid || "未知"))
            ]),
            el("span", {
              class: "pill " + (isAllMode ? "neutral" : "ok"),
              text: isAllMode ? "完整备份包" : (acc.expiresAtHuman ? "到期：" + acc.expiresAtHuman : "长期有效")
            })
          ]),
          el("div", { style: "font-size:11px;color:var(--g8);word-break:break-all",
            text: isAllMode ? "可用于多设备一键整体迁移与完整账号库还原" : ("UID: " + (acc.uid || "未知") + " · 域名: " + (acc.domain || "www.workbuddy.ai"))
          })
        ]);
        body.appendChild(banner);

        // 3. 工具栏：下载备份 / 复制 Token / 复制 JSON / 脱敏开关
        var actions = el("div", { style: "display:flex;gap:8px;flex-wrap:wrap;margin-bottom:12px" });

        var btnDownload = el("button", { class: "btn-hero", style: "padding:6px 14px;font-size:12px" }, [
          el("svg", { class: "ico ico-sm", html: '<use href="#i-down"/>' }),
          isAllMode ? "下载全量备份包 (.json)" : "下载单账号备份 (.json)"
        ]);
        btnDownload.onclick = function () {
          var blob = new Blob([rawJson], { type: "application/json;charset=utf-8" });
          var url = URL.createObjectURL(blob);
          var a = document.createElement("a");
          a.href = url;
          a.download = isAllMode
            ? ("workbuddy-" + side + "-all-accounts-backup.json")
            : ("workbuddy-" + side + "-credential-" + (acc.uid ? acc.uid.slice(0, 8) : "backup") + ".json");
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);
          URL.revokeObjectURL(url);
          toast("凭据备份已开始下载！", "ok");
        };
        actions.appendChild(btnDownload);

        if (!isAllMode && acc.token) {
          var btnCopyToken = el("button", { class: "ghost", style: "padding:6px 12px;font-size:12px", text: "📋 复制 Access Token" });
          btnCopyToken.onclick = function () {
            navigator.clipboard.writeText(acc.token).then(function () {
              btnCopyToken.textContent = "✓ 已复制 Token!";
              toast("Access Token 已复制到剪贴板", "ok");
              setTimeout(function () { btnCopyToken.textContent = "📋 复制 Access Token"; }, 2000);
            });
          };
          actions.appendChild(btnCopyToken);
        }

        var btnCopyAll = el("button", {
          class: "ghost",
          style: "padding:6px 12px;font-size:12px",
          text: isAllMode ? "📄 复制全量 JSON" : "📄 复制官方格式 JSON"
        });
        btnCopyAll.onclick = function () {
          navigator.clipboard.writeText(rawJson).then(function () {
            btnCopyAll.textContent = "✓ 已复制 JSON!";
            toast("凭据 JSON 已复制到剪贴板", "ok");
            setTimeout(function () { btnCopyAll.textContent = isAllMode ? "📄 复制全量 JSON" : "📄 复制官方格式 JSON"; }, 2000);
          });
        };
        actions.appendChild(btnCopyAll);

        var isMasked = true;
        var btnToggleMask = el("button", { class: "ghost", style: "padding:6px 10px;font-size:12px;margin-left:auto", text: "👁️ 显示明文" });
        actions.appendChild(btnToggleMask);
        body.appendChild(actions);

        // 4. 预览窗
        function getDisplayText() {
          if (!isMasked) return rawJson;
          var t = rawJson;
          if (acc.token && acc.token.length > 20) {
            var m = acc.token.slice(0, 10) + "••••••••••••••••••••••••••••••••••••" + acc.token.slice(-8);
            t = t.replace(acc.token, m);
          }
          if (acc.refreshToken && acc.refreshToken.length > 20) {
            var mr = acc.refreshToken.slice(0, 8) + "••••••••••••••••" + acc.refreshToken.slice(-6);
            t = t.replace(acc.refreshToken, mr);
          }
          return t;
        }

        var pre = el("pre", {
          style: "background:#0d1117;color:#c9d1d9;padding:12px;border-radius:var(--r-sm);font-size:11px;max-height:200px;overflow-y:auto;user-select:all;border:1px solid rgba(255,255,255,0.06);white-space:pre-wrap;word-break:break-all",
          text: getDisplayText()
        });
        body.appendChild(pre);

        btnToggleMask.onclick = function () {
          isMasked = !isMasked;
          btnToggleMask.textContent = isMasked ? "👁️ 显示明文" : "🔒 隐藏脱敏";
          pre.textContent = getDisplayText();
        };

        // 5. 底部指引
        var tip = el("div", {
          class: "hint",
          style: "margin-top:10px;font-size:11px;color:var(--g8);line-height:1.5",
          html: "💡 <b>用法指引：</b><br>" +
                "• <b>单账号</b>：复制 Access Token 直接接入 Cherry Studio / NextChat / Cursor 等第三方客户端；<br>" +
                "• <b>全选备份</b>：保存全量账号配置，可在任意设备上一键恢复全部多账号与登录态。"
        });
        body.appendChild(tip);

      }).catch(function (err) {
        clear(body);
        body.appendChild(el("div", { class: "bad", style: "padding:12px;background:rgba(239,68,68,0.1);border-radius:var(--r-sm)", text: "获取凭据失败：" + err.message }));
      });
    }

    loadAndRender(null, false);
  }

  // 档位 id → 显示名。注意 /api/state 里那个字段叫 displayName，不叫 name。
  // 优先用它（客户端自己的叫法），拿不到再退回固定说法。
  function sideLabel(id) {
    var b = backendById(id);
    if (b && b.displayName) return b.displayName;
    return id === "intl" ? "国际版" : "国内版";
  }

  // prettyModel 把模型 id 翻译成人话。id 来自日志，是给机器看的；
  // 界面展示用这个，原始 id 放 title 里悬停可查——翻译只是换行大小写
  // 和空格，不发明新名字。
  var MODEL_ALIAS = {
    "auto": "Auto（自动路由）",
    "balanced-model": "Balanced（均衡）",
    "fast-model": "Fast（快速）",
    "primary-model": "Primary（主力）",
    "deep-model": "Deep（深度）",
    "default": "默认",
    "default-model": "Default（默认）",
    "default-model-lite": "Default-Lite（轻量）"
  };
  var MODEL_WORD = {
    deepseek: "DeepSeek", glm: "GLM", gpt: "GPT", kimi: "Kimi",
    minimax: "MiniMax", hunyuan: "Hunyuan", gemini: "Gemini"
  };
  function prettyModel(id) {
    if (!id) return "未识别模型";
    if (MODEL_ALIAS[id]) return MODEL_ALIAS[id];
    return id.split("-").map(function (seg) {
      if (MODEL_WORD[seg]) return MODEL_WORD[seg];
      if (/^v?[0-9]/.test(seg)) return seg; // v4.1、4.6、3b 这类版本号原样
      return seg.charAt(0).toUpperCase() + seg.slice(1);
    }).join(" ");
  }

  // countdown 把"重置时间戳"翻译成"还有多久"，比一串时间戳直观。
  function countdown(ms) {
    if (!ms) return "";
    var d = ms - Date.now();
    if (d <= 0) return "";
    var h = Math.floor(d / 3600000), m = Math.round((d % 3600000) / 60000);
    if (m >= 60) { h++; m = 0; }
    return h > 0 ? h + " 小时 " + m + " 分" : m + " 分钟";
  }

  function formatCreditNum(n) {
    if (typeof n !== 'number') {
      var parsed = parseFloat(n);
      if (isNaN(parsed)) return n || "0";
      n = parsed;
    }
    if (n % 1 === 0) return n.toLocaleString('en-US');
    return Number(n.toFixed(2)).toLocaleString('en-US');
  }

  // showAssetDetailsModal 弹出当前账号的原生资产与用量明细窗口，无需跳转外部网页登录。
  function showAssetDetailsModal(side, lv) {
    side = side || "intl";
    lv = lv || {};
    var existing = document.getElementById("asset-details-modal");
    if (existing) existing.remove();

    var overlay = el("div", { class: "modal-overlay", id: "asset-details-modal" });
    var card = el("div", { class: "modal-card", style: "max-width:620px" });

    function closeModal() {
      overlay.classList.remove("open");
      setTimeout(function () { overlay.remove(); }, 200);
    }
    overlay.onclick = function (e) {
      if (e.target === overlay) closeModal();
    };

    var isIntl = side === "intl";
    var titleText = isIntl ? "国际端账户资产与积分明细 (WorkBuddy AI)" : "国内端账户资产与积分明细 (CodeBuddy)";
    var officialUrl = "/api/auth/web-login?side=" + (isIntl ? "intl" : "cn") + "&target=" + encodeURIComponent(isIntl ? "/profile/usage" : "/profile/plan") + (TOKEN ? "&t=" + encodeURIComponent(TOKEN) : "");

    // Head
    var head = el("div", { class: "modal-head" }, [
      el("div", { class: "modal-title" }, [
        el("svg", { class: "ico ico-sm", html: isIntl ? '<use href="#i-layers"/>' : '<use href="#i-server"/>' }),
        titleText
      ]),
      el("button", { class: "modal-close", html: '<svg class="ico ico-sm"><use href="#i-x"/></svg>', onclick: closeModal })
    ]);

    // Body
    var body = el("div", { class: "modal-body", style: "display:flex;flex-direction:column;gap:14px" });

    // 账号概况条
    var accName = lv.nickname || (lv.uid ? lv.uid.slice(0, 16) + "…" : "未命名");
    var refreshBtn = el("button", {
      class: "ghost",
      style: "font-size:11px;padding:3px 8px;display:inline-flex;align-items:center;gap:4px;cursor:pointer;color:var(--ink-2);border-radius:var(--r-sm)",
      html: '<svg class="ico ico-xs"><use href="#i-reload"/></svg><span>即时同步</span>',
      onclick: function () {
        refreshBtn.classList.add("loading");
        refreshBtn.disabled = true;
        loadUsage(true, true).then(function (u) {
          refreshBtn.classList.remove("loading");
          refreshBtn.disabled = false;
          var newLv = (u && u.Survey && u.Survey.live && u.Survey.live[side]) || lv;
          toast("已从腾讯官方获取最新实时资产与到期时间", "ok");
          showAssetDetailsModal(side, newLv);
        }).catch(function (err) {
          refreshBtn.classList.remove("loading");
          refreshBtn.disabled = false;
          toast("同步失败：" + (err.message || "网络异常"), "bad");
        });
      }
    });

    var overviewRow = el("div", {
      style: "background:rgba(255,255,255,0.025);border:1px solid var(--g4);border-radius:var(--r-sm);padding:10px 14px;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:8px"
    }, [
      el("div", { style: "display:flex;align-items:center;gap:8px" }, [
        el("div", { style: "font-weight:600;font-size:13.5px;color:var(--ink)" }, [
          (isIntl ? "国际账号：" : "国内账号：") + accName
        ]),
        el("span", { class: "pill " + (lv.ok ? "ok" : "ghost"), text: lv.ok ? "实时通路正常" : "未就绪" }),
        (isIntl && lv.ok && lv.hasRefreshToken) ? el("span", { class: "pill ok", style: "background:rgba(99,102,241,0.18);border:1px solid rgba(99,102,241,0.38);color:#a5b4fc", text: "⚡ 自动无感保鲜" }) : null
      ]),
      el("div", { style: "display:flex;align-items:center;gap:10px" }, [
        el("div", { style: "font-size:11.5px;color:var(--ink-3);font-family:var(--mono)" }, [
          "UID: " + (lv.uid || "未知")
        ]),
        refreshBtn
      ])
    ]);
    body.appendChild(overviewRow);

    // 积分包详单区
    var pkgs = lv.packages || [];
    var pkgsSec = el("div", { style: "display:flex;flex-direction:column;gap:8px" });
    pkgsSec.appendChild(el("div", { style: "font-size:12px;font-weight:600;color:var(--ink-2);display:flex;align-items:center;justify-content:space-between" }, [
      el("span", { text: "📦 积分包用量清单 (" + pkgs.length + " 个)" }),
      el("span", { style: "font-weight:normal;font-size:11px;color:var(--ink-3)", text: "本地 API 实时直读" })
    ]));
    pkgsSec.appendChild(el("div", {
      style: "font-size:11px;color:var(--ink-3);background:rgba(255,255,255,0.02);padding:6px 10px;border-radius:var(--r-sm);border:1px dashed var(--g4)",
      text: "💡 官方扣费策略：问答时优先消耗月度基础包；用尽后按到期时间优先扣除临期的活动/加量包。"
    }));

    if (!pkgs.length) {
      pkgsSec.appendChild(el("div", { class: "hint", text: isIntl ? "当前暂无有效积分包数据" : "当前为包月/无额度限制套餐或无独立积分包" }));
    } else {
      pkgs.forEach(function (p, idx) {
        var pct = p.total > 0 ? Math.round(p.remain / p.total * 100) : 0;
        var expBadge = null;
        if (p.expiresAtMs > 0) {
          var leftMs = p.expiresAtMs - Date.now();
          if (leftMs <= 0) {
            expBadge = el("span", {
              class: "pill",
              style: "font-size:10px;padding:1px 6px;background:rgba(239,68,68,0.15);border:1px solid rgba(239,68,68,0.35);color:#f87171",
              text: "已到期失效"
            });
          } else if (leftMs < 48 * 3600 * 1000) {
            var leftH = Math.max(1, Math.round(leftMs / 3600000));
            expBadge = el("span", {
              class: "pill warn",
              style: "font-size:10px;padding:1px 6px;background:rgba(245,158,11,0.18);border:1px solid rgba(245,158,11,0.45);color:#fbbf24;font-weight:600",
              text: "⏳ 剩 " + leftH + " 小时 (临期优先消耗)"
            });
          } else {
            var leftD = Math.round(leftMs / 86400000);
            expBadge = el("span", {
              class: "pill",
              style: "font-size:10px;padding:1px 5px;background:rgba(255,255,255,0.04);border:1px solid var(--g4);color:var(--ink-2)",
              text: "⏳ 剩 " + leftD + " 天"
            });
          }
        }
        var pCard = el("div", {
          style: "background:var(--g2);border:1px solid var(--g4);border-radius:var(--r-sm);padding:10px 12px;display:flex;flex-direction:column;gap:6px"
        }, [
          el("div", { style: "display:flex;align-items:center;justify-content:space-between;font-size:12px;flex-wrap:wrap;gap:4px" }, [
            el("div", { style: "display:flex;align-items:center;gap:6px;flex-wrap:wrap" }, [
              el("span", { class: "pill", style: "font-size:10px;padding:0 5px", text: "#" + (idx + 1) }),
              el("span", { style: "font-family:var(--mono);color:var(--ink);font-weight:600", text: p.code }),
              p.name ? el("span", { class: "pill", style: "font-size:10px;background:rgba(255,255,255,0.05);border:1px solid var(--g4);color:var(--ink-2)", text: p.name }) : null,
              p.kindZh ? el("span", { class: "pill " + (p.kind === "subscription" ? "ok" : "warn"), style: "font-size:10px;padding:1px 6px", text: p.kindZh }) : null,
              p.deductPriority === 1 ? el("span", { class: "pill", style: "background:rgba(16,185,129,0.15);border:1px solid rgba(16,185,129,0.3);color:#34d399;font-size:10px;padding:1px 5px", text: "优先消耗" }) : null,
              expBadge
            ]),
            el("div", { style: "font-family:var(--mono);font-weight:600;color:var(--ink)" }, [
              formatCreditNum(p.remain) + " / " + formatCreditNum(p.total) + " " + (p.unit || "credits"),
              el("span", { style: "font-size:11px;color:var(--ink-3);margin-left:6px;font-weight:normal", text: "(" + pct + "% 可用)" })
            ])
          ]),
          el("div", { class: "bar-track", style: "height:6px;margin:0" }, [
            el("span", { class: "bar-fill " + (isIntl ? "intl" : "cn"), style: "width:" + Math.max(1, pct) + "%" })
          ]),
          (function () {
            var timeTxt = "";
            if (p.cycleEndTime) {
              timeTxt = "官方到期时间：" + p.cycleEndTime;
            } else if (p.expiresAtMs) {
              timeTxt = "预估到期/重置：" + shortDate(new Date(p.expiresAtMs).toISOString());
            }
            if (!p.expiryRuleZh && !timeTxt) return null;
            return el("div", { style: "font-size:11px;color:var(--ink-3);display:flex;align-items:center;justify-content:space-between;margin-top:2px" }, [
              p.expiryRuleZh ? el("span", { text: "⏳ 有效期规则：" + p.expiryRuleZh }) : el("span"),
              timeTxt ? el("span", { style: "font-family:var(--mono);color:var(--ink-2);font-weight:600", text: timeTxt }) : null
            ]);
          })()
        ]);
        pkgsSec.appendChild(pCard);
      });
    }
    body.appendChild(pkgsSec);

    // 国际端：限免模型特权清单
    if (isIntl && lv.promos && lv.promos.length) {
      var promoSec = el("div", { style: "display:flex;flex-direction:column;gap:8px" });
      promoSec.appendChild(el("div", { style: "font-size:12px;font-weight:600;color:var(--ink-2)", text: "🔥 当前有效限时免费大模型 (0.00x)" }));
      lv.promos.forEach(function (p) {
        var d = daysLeft(p.validUntil);
        var countdownTxt = d !== null && d >= 0 ? ("⏳ 剩 " + d + " 天") : (p.validUntil ? freeUntil(p.validUntil) : "限时免费");
        var mNames = (p.modelIds || []).map(function (m) { return prettyModel(m); }).join("、") || (p.label || "特惠模型");
        promoSec.appendChild(el("div", {
          style: "background:rgba(99,179,196,0.06);border:1px solid rgba(99,179,196,0.22);border-radius:var(--r-sm);padding:8px 12px;display:flex;align-items:center;justify-content:space-between;font-size:12px"
        }, [
          el("div", { style: "display:flex;align-items:center;gap:6px" }, [
            el("span", { class: "pill ok", style: "font-size:10px", text: "0.00x 免费" }),
            el("span", { style: "color:var(--ink-1);font-weight:500", text: mNames })
          ]),
          el("span", { style: "font-family:var(--mono);color:var(--brand);font-size:11.5px", text: countdownTxt })
        ]));
      });
      body.appendChild(promoSec);
    }

    // 底部：关于官方网页登录的免密 SSO 说明与直达
    var footNotice = el("div", {
      style: "background:rgba(255,255,255,0.015);border:1px dashed var(--g4);border-radius:var(--r-sm);padding:10px 12px;display:flex;flex-direction:column;gap:6px;font-size:11.5px;color:var(--ink-3);line-height:1.5"
    }, [
      el("div", { style: "display:flex;align-items:center;justify-content:space-between;gap:8px" }, [
        el("div", { style: "color:var(--ink-2);font-weight:600", text: "🔗 腾讯官方 Web 控制台（历史每笔对话消费账单）" }),
        (function () {
          var targetPath = isIntl ? "/profile/usage" : "/profile/plan";
          var sideCode = isIntl ? "intl" : "cn";
          var btn = el("a", {
            class: "chip",
            href: officialUrl,
            target: "_blank",
            rel: "noreferrer",
            style: "margin:0;font-size:11px;white-space:nowrap;cursor:pointer",
            text: isIntl ? "免密进入官网账单 ↗" : "打开官网账单 ↗",
            onclick: function (e) {
              e.preventDefault();
              btn.classList.add("loading");
              api("/api/auth/web-login?side=" + sideCode + "&target=" + encodeURIComponent(targetPath) + "&json=1")
                .then(function (res) {
                  btn.classList.remove("loading");
                  if (res && res.url) {
                    window.open(res.url, "_blank");
                  } else {
                    window.open(officialUrl, "_blank");
                  }
                })
                .catch(function () {
                  btn.classList.remove("loading");
                  window.open(officialUrl, "_blank");
                });
            }
          });
          return btn;
        })()
      ]),
      el("div", { text: isIntl ? "💡 提示：本客户端已支持官方免密单点登录（SSO）桥接。点击上方按钮将自动生成临时授权票据，在浏览器中一键免密进入官方 Web 账单控制台，无需再次登录。" : "💡 提示：本客户端已通过本地专用 API 直接拉取您的积分与限免资产（无需网页登录即可在上方查看全部数据）。若需前往国内端官网网页，需在浏览器中进行一次官方扫码/验证码登录。" })
    ]);
    body.appendChild(footNotice);

    // Foot
    var foot = el("div", { class: "modal-foot" }, [
      el("button", { class: "ghost", text: "关闭", onclick: closeModal })
    ]);

    card.appendChild(head);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);
    document.body.appendChild(overlay);
    overlay.offsetHeight;
    overlay.classList.add("open");
  }

  var lastUsageScanAt = 0;
  function loadUsage(silent, force) {
    var now = Date.now();
    if (!silent && !force && (now - lastUsageScanAt < 15000)) {
      toast("资产数据已是最新（15s 防刷保护）", "ok");
      return Promise.resolve();
    }
    lastUsageScanAt = now;
    var box = document.getElementById("usage-body");
    var btn = document.getElementById("btn-usage-scan");
    if (!silent) {
      if (btn) btn.classList.add("loading");
      if (!usageData && box) {
        clear(box);
        box.appendChild(el("div", { class: "hint", text: "正在读取双端资产与额度数据…" }));
      }
    }
    if (force) {
      usageHistoryData = null;
    }
    return api("/api/usage" + (force ? "?force=1" : "")).then(function (u) {
      usageData = u;
      try {
        localStorage.setItem("wbmux_usage_cache", JSON.stringify(u));
      } catch (e) {}
      // 国际侧付费模型的选择以 /api/state 为准：它是本地读配置，秒回；
      // 而 /api/usage 要拉双端账单，实测能拖到 20s+。两者之间任何一次
      // "顺手重画"（签到回话、测速回话）都会把界面刷回陈旧数据，
      // 表现为「已自选付费 0 个」——2026-10-26 实测。
      //
      // 只有 state 还没回来（首屏竞态）时，才用 usage 里那份兜底；
      // 而且只在它非空时覆盖，避免同一个空数组把 state 的正确值盖掉。
      if (!selectedFromState && u && Array.isArray(u.selectedIntlModels) && u.selectedIntlModels.length) {
        selectedIntlModels = u.selectedIntlModels;
      }
      if (!selectedFromState && u && Array.isArray(u.selectedCNModels) && u.selectedCNModels.length) {
        selectedCNModels = u.selectedCNModels;
      }
      if (u && Array.isArray(u.curatedCNModels) && u.curatedCNModels.length) {
        curatedCNModels = u.curatedCNModels;
      }
      renderUsage();
      return u;
    }).catch(function (e) {
      if (!silent && box) {
        clear(box);
        box.appendChild(el("div", { class: "note bad", text: "读取失败：" + e.message }));
      }
      throw e;
    }).finally(function () {
      if (btn) btn.classList.remove("loading");
    });
  }

  // quotaNowCard 回答用户唯一真正关心的那个问题：**我现在能不能用**。
  //
  // # 为什么要单独做这张卡（2026-09-29 用户反馈"完全看不了"）
  //
  // 原来的面板读的是客户端会话文件里的历史错误记录，于是它展示的是
  // "过去 7 天被限过几次"。用户看到的是"限制已过，可以继续用（09-27 重置）"
  // ——而此刻他正被 09-29 的 429 挡着。数据和结论都是过期的。
  //
  // 现在以 **代理的观察**（proxyQuota）为准：只有代理真的跟上游说过话，
  // 知道哪个模型此刻被拒、上游说什么时候恢复（成功一次就清掉状态）。
  // 客户端记录退到下面的"历史"里，不再参与结论。
  function quotaNowCard() {
    // 两侧的实时数据。国际侧一直是明文凭据，国内侧靠 internal/credential
    // 借客户端运行时解开信封，所以两边现在都读得到。
    var liveAll = usageData.live || {};
    var live = liveAll.intl || {};
    var proxy = usageData.proxyQuota || [];
    var byModel = {};
    proxy.forEach(function (q) { byModel[q.model] = q; });
    // proxyOk 是"代理亲眼看它成功过"的模型。缺了它就只能把"从没发过请求"
    // 也写成"可用"——那是没有根据的断言，用户一试就 429。
    var okSet = {};
    (usageData.proxyOk || []).forEach(function (m) { okSet[m] = true; });

    // 免费模型清单：**判据是 freeNow（被"限时免费"活动覆盖），不是 rate===0**。
    //
    // 实测（2026-09-29）为什么不能用 rate===0：**国内侧 37 个模型的 rate 全是 0**
    // ——国内是包月制，接口根本不返回计费倍率。拿 rate===0 当"免费"会把国内
    // 全部 37 个都列进来，那个判据在国内侧毫无区分度。国际侧的 rate===0 里
    // 还混着 default-model（路由别名）与 gpt-image / seedance（图像、视频生成）。
    // freeNow 来自官方的 modelPromotions（折扣因子为 0 且在有效期内），只有国际侧有。
    //
    // **两侧都收**：列表原本写死读 intl，所以国内侧自己的活动（假如将来有）看不见。
    // 标签写"计费侧"而不是"国际/国内"——它指的是**这个模型算哪个账号的钱**，
    // 不是"它显示在哪个客户端里"。用户很容易把这层意思搞混：wbmux 注入到国内
    // 客户端的那两个模型，计费侧其实是国际（名字里就带"（国际免费）"）。
    var free = [], seen = {};
    var sideNames = { intl: "算国际", cn: "算国内" };
    [["intl", sideNames.intl], ["cn", sideNames.cn]].forEach(function (pair) {
      var side = pair[0], sideName = pair[1];
      var lv = liveAll[side] || {};
      // 该侧的实时数据没读到，就没法说它的免费模型——如实跳过而不是列出来再标"未知"。
      if (!lv.ok) return;
      (lv.models || []).forEach(function (m) {
        if (!m.freeNow) return;
        var key = side + "/" + m.id;
        if (seen[key]) return;
        seen[key] = true;
        // 活动截止日从 promos 里找（模型条目本身没有）。
        var until = "";
        (lv.promos || []).forEach(function (p) {
          if ((p.modelIds || []).indexOf(m.id) >= 0 && p.validUntil) {
            until = String(p.validUntil).slice(0, 10);
          }
        });
        free.push({ id: m.id, side: side, sideName: sideName, until: until,
          label: m.freeLabel || "限时免费" });
      });
      // 兜底：活动的覆盖关系偶尔与模型标记不同步，promos 里点名的也要算上。
      (lv.promos || []).forEach(function (p) {
        (p.modelIds || []).forEach(function (id) {
          var key = side + "/" + id;
          if (seen[key]) return;
          seen[key] = true;
          free.push({ id: id, side: side, sideName: sideName,
            until: String(p.validUntil || "").slice(0, 10),
            label: p.label || "限时免费" });
        });
      });
    });
    // 国内侧的"可用模型数"单独算，与"限时免费"是两回事——
    // 国内包月制，全部模型都可用，没有"免费/收费"之分。
    var cnModels = ((liveAll.cn || {}).models || []).length;
    var cnOK = !!(liveAll.cn || {}).ok;

    var card = el("div", { class: "card" });
    card.appendChild(el("div", { class: "card-head" }, [
      el("span", { class: "card-title", html: ICO("gauge", "ico-sm") + "限时免费模型" }),
      el("span", { class: "card-sub",
        text: "国际 " + free.length + " 个" + (cnOK ? " · 国内 " + cnModels + " 个可用（包月）" : "") })
    ]));

    if (!free.length) {
      card.appendChild(el("div", { class: "hint",
        text: "国际侧当前没有「限时免费」模型（活动可能已到期）。" +
          "点下面的「一键测试」可以现场验一次。" +
          (cnOK ? "国内侧 " + cnModels + " 个模型是包月制、照常可用。" : "") }));
      return card;
    }

    // 先算总账，写在最上面——用户第一眼要的是"能用还是不能用"。
    //
    // **只统计有观测源的那一侧（国际）**。本机代理只转发国际模型，国内侧
    // 没有观测源、永远显示"未验证"——把它计入"N 个未验证"会让这个数字
    // 恒等于国内侧的数量，既没信息量又像是出了问题。
    var usable = 0, blocked = 0, unknown = 0, soonest = 0, cnUnverified = 0, faulted = 0;
    free.forEach(function (f) {
      var q = byModel[f.id];
      if (q && q.state === "exhausted") { blocked++; return; }
      if (q && q.state === "rate_limited") {
        blocked++;
        if (q.untilMs && (!soonest || q.untilMs < soonest)) soonest = q.untilMs;
        return;
      }
      // 上游故障（5xx）：不算"被限"——限流是等/充值，这是对方挂了、重试即可。
      if (q && q.state === "upstream_error") { faulted++; return; }
      if (f.side !== "intl") { cnUnverified++; return; }
      if (okSet[f.id]) { usable++; return; }
      unknown++;   // 国际侧但代理还没见它成功过
    });

    var verdict, cls;
    if (blocked || faulted) {
      cls = usable || unknown ? "warn" : "bad";
      verdict = (usable ? usable + " 个可用 · " : "") +
        (unknown ? unknown + " 个待验证 · " : "") +
        (blocked ? blocked + " 个被限" : "") +
        (blocked && faulted ? " · " : "") +
        (faulted ? faulted + " 个上游故障" : "");
    } else if (unknown) {
      cls = "info";
      verdict = usable ? usable + " 个可用 · " + unknown + " 个待验证"
                       : unknown + " 个待验证";
    } else if (usable) {
      cls = "ok";
      verdict = "都能用";
    } else {
      // 只有国内侧模型时，别写"都能用"（那是断言），如实说没有观测源。
      cls = "info";
      verdict = "国际侧没有免费模型";
    }
    var head = el("div", { class: "q-verdict" }, [
      el("span", { class: "pill " + cls, text: verdict })
    ]);
    if (soonest) {
      head.appendChild(el("span", { class: "hint", style: "margin:0 0 0 8px",
        text: "最早 " + fmtClock(soonest) + " 恢复（还有 " + countdown(soonest) + "）" }));
    } else if (blocked) {
      head.appendChild(el("span", { class: "hint", style: "margin:0 0 0 8px",
        text: "额度用尽不会自己恢复，要充值" }));
    }
    card.appendChild(head);

    // 一行一个模型：来源 / 名字 / 状态。倒计时用人话，原始时刻放 title 里。
    free.forEach(function (f) {
      var q = byModel[f.id];
      var icon, icoCls, stateText, title = "";
      if (q && q.state === "exhausted") {
        icon = "cross"; icoCls = "bad"; stateText = "额度用尽";
        title = q.message || "";
      } else if (q && q.state === "rate_limited") {
        icon = "clock"; icoCls = "warn";
        stateText = q.untilMs
          ? countdown(q.untilMs) + "后可恢复（" + fmtClock(q.untilMs) + "）"
          : "被限流中，等重置";
        title = q.message || "";
      } else if (q && q.state === "upstream_error") {
        // 上游 5xx：**不是用户的错**，重试或换模型即可。文案要说清这一点，
        // 否则看到"故障"第一反应还是"我哪儿配错了"。
        icon = "pulse"; icoCls = "warn"; stateText = "上游故障，可重试";
        title = (q.message || "") + "\n（上游服务端的问题，与你的配置/额度无关；重试或换一个模型）";
      } else if (f.side === "intl" && okSet[f.id]) {
        icon = "check"; icoCls = "ok"; stateText = "可用";
      } else if (f.side !== "intl") {
        // 国内侧不经本机代理，永远没有观测。写"未验证"没错，但得说清
        // **不是没验过、而是这里没有观测源**，否则看起来像坏了。
        icon = "check"; icoCls = "ok"; stateText = "可用";
        title = "这一侧不经过本机代理，所以这里没有观测记录；它在客户端里可以正常选用。";
      } else {
        // 国际侧且没观察过：如实说"未验证"——写"可用"是无根据的断言。
        icon = "pulse"; icoCls = "dim"; stateText = "未验证";
      }
      var meta = [];
      var expiring = f.until && expiringSoon(f.until);
      if (f.until) meta.push(freeUntil(f.until));
      if (f.label && !f.until) meta.push(f.label);
      card.appendChild(el("div", { class: "q-row", title: title }, [
        // 图标 + 来源标签同在第一列：`.q-row` 是写死的三列 grid，
        // 来源**不能**单独占一列（会把名字与状态挤到错位的列上）。
        el("span", { class: "q-lead" }, [
          el("span", { class: "q-ico " + icoCls, html: ICO(icon, "ico-sm") }),
          el("span", { class: "q-side " + (f.side === "intl" ? "intl" : "cn"),
            text: f.sideName })
        ]),
        el("div", { class: "q-main" }, [
          el("div", { class: "q-name", text: prettyModel(f.id), title: f.id }),
          // 快到期的那条标黄：活动是整批换的，看漏一个就突然少一个能用
          // 的模型，而这类"还有几天"的信息不主动说出来没人会去算。
          meta.length ? el("div", {
            class: "q-meta" + (expiring ? " warn-text" : ""),
            text: meta.join(" · ")
          }) : null
        ]),
        el("span", { class: "q-state " + icoCls, text: stateText })
      ]));
    });

    if (blocked) {
      card.appendChild(el("div", { class: "hint", style: "margin-top:8px",
        text: "「被限」是免费额度用完了，等它恢复即可（不影响账号），期间换上面可用的模型。" +
          (faulted ? "「上游故障」是对方服务端的问题（5xx），重试或换模型即可，你这边不用改任何东西。" : "") +
          (unknown ? "「未验证」表示这些模型最近没被用过，点「一键测试」可以现场验一次。" : "") }));
    } else if (faulted) {
      card.appendChild(el("div", { class: "hint", style: "margin-top:8px",
        text: "「上游故障」是对方服务端的问题（HTTP 5xx），与你的配置和额度都无关——" +
          "重试一次通常就好，或者先换上面标「可用」的模型。" }));
    } else if (unknown) {
      card.appendChild(el("div", { class: "hint", style: "margin-top:8px",
        text: "「未验证」表示本机代理最近没见它成功过（刚重启也会这样）；" +
          "点「一键测试」可以现场验一次。" }));
    }
    if (cnUnverified) {
      // 单独说一句，别让它混进国际侧那套"可用 / 被限"里——两边判据不同。
      card.appendChild(el("div", { class: "hint", style: "margin-top:6px",
        text: "标记为「国内」的模型不经过本机代理，这里没有它们的观测记录，" +
          "所以不参与上面的统计；它们在客户端里可以正常选用。" }));
    }
    return card;
  }

  // fmtClock 把时间戳变成 "22:18"——精确到分够了，秒是噪音。
  function fmtClock(ms) {
    if (!ms) return "";
    var d = new Date(ms);
    return String(d.getHours()).padStart(2, "0") + ":" +
           String(d.getMinutes()).padStart(2, "0");
  }

  // shortDate 把 "2026-10-10" 压成 "10-10"——年份在当前年份时是纯噪音，
  // 而这个面板每一行都有日期，省下 5 个字符差别很直观。
  function shortDate(iso) {
    var s = String(iso || "");
    if (s.length < 10) return s;
    var now = new Date().getFullYear() + "-";
    return s.slice(0, 5) === now ? s.slice(5) : s;
  }

  // daysLeft 算"还有几天到期"，返回 null 表示算不出来或已经过了。
  //
  // 为什么单独写一个、不复用 countdown：`countdown` 是**分钟/小时**级的
  // （给限流恢复用的），拿它算一个 10 天后的活动会得到"239 小时 30 分"，
  // 反而看不出紧迫感。这里要的是"还剩 10 天"这种人话。
  //
  // 按**自然日**算而不是 24 小时整倍数：用户关心的是"到那天就没得用了"，
  // 不是"还有 239 小时"。所以先把两个时刻都归到当天零点再相减。
  function daysLeft(iso) {
    var s = String(iso || "");
    if (s.length < 10) return null;
    // 只取日期部分（时区偏移那个 "+08:00" 在这里没意义，见下）。
    var parts = s.slice(0, 10).split("-");
    if (parts.length !== 3) return null;
    var end = new Date(Number(parts[0]), Number(parts[1]) - 1, Number(parts[2]));
    if (isNaN(end.getTime())) return null;
    var now = new Date();
    var today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    var days = Math.round((end - today) / 86400000);
    return days;
  }

  // freeUntil 把到期日渲染成"还剩 N 天"或"今天最后一天"。
  //
  // 为什么值得专门渲染：这几个免费模型是按活动批次换的（2026-10-16 实测
  // step-5-preview 只剩 10 天，另外两个到 11-01）。只写"免费至 10-26"
  // 要用户自己心算还剩几天——而"还剩 3 天"和"还剩 30 天"是完全不同的决策
  // 信息（前者该考虑换模型了）。所以把剩余天数直接算出来。
  function freeUntil(iso) {
    var d = daysLeft(iso);
    if (d === null) return "免费至 " + shortDate(iso);
    if (d < 0) return "已到期（" + shortDate(iso) + "）";
    if (d === 0) return "今天最后一天";
    if (d === 1) return "明天到期";
    return shortDate(iso) + " 到期 · 还剩 " + d + " 天";
  }

  // 剩不到 7 天时标黄——够提前换模型，又不至于天天示警导致麻木。
  function expiringSoon(iso) {
    var d = daysLeft(iso);
    return d !== null && d >= 0 && d < 7;
  }

  function renderModelDetailsFold(side, list, summaryTitle) {
    if (!list || !list.length) return el("div", {});
    var isIntl = (side === "intl");
    var grid = el("div", { class: "mcards", style: "margin-top:8px" });
    var details = el("details", { class: "fold", style: "margin:4px 0 0" });
    var summaryEl = el("summary", { style: "font-size:12px;color:var(--ink-3)", text: summaryTitle });
    details.appendChild(summaryEl);

    // 针对国际端与国内端模型，提供顶部控制条（搜索、过滤标签、统计）
    var filterMode = "all";
    var searchKeyword = "";
    var statLabel = null;
    var filterBtns = {};
    var cards = [];

    function updateStatsAndFilters() {
      var total = list.length;
      var freeCount = 0;
      var injectedCount = 0;
      var unaddedCount = 0;

      list.forEach(function (r) {
        var isFree = !!r.free;
        var isSel = isIntlModelSelected(r.id);
        if (isFree) freeCount++;
        if (isFree || isSel) injectedCount++;
        else unaddedCount++;
      });

      if (filterBtns["all"]) filterBtns["all"].textContent = "全部 (" + total + ")";
      if (filterBtns["free"]) filterBtns["free"].textContent = "限免 (" + freeCount + ")";
      if (filterBtns["injected"]) filterBtns["injected"].textContent = "已入国内端 (" + injectedCount + ")";
      if (filterBtns["unadded"]) filterBtns["unadded"].textContent = "未入付费 (" + unaddedCount + ")";

      var paidCount = injectedCount - freeCount;
      if (statLabel) {
        statLabel.textContent = "已选 " + paidCount + " 个付费注入";
      }
      summaryEl.textContent = "国际模型库与快捷注入（" + total + " 个 · 已自选付费 " + paidCount + " 个）";
    }

    function applyFilter() {
      var kw = searchKeyword.toLowerCase().trim();
      cards.forEach(function (item) {
        var card = item.card;
        var r = item.data;
        var isFree = !!r.free;
        var isSel = isIntlModelSelected(r.id);
        var isInjected = isFree || isSel;

        var matchKw = true;
        if (kw) {
          var nameStr = (r.name || "").toLowerCase();
          var idStr = (r.id || "").toLowerCase();
          var descStr = (r.desc || "").toLowerCase();
          matchKw = (nameStr.indexOf(kw) >= 0 || idStr.indexOf(kw) >= 0 || descStr.indexOf(kw) >= 0);
        }

        var matchFilter = true;
        if (filterMode === "free") matchFilter = isFree;
        else if (filterMode === "injected") matchFilter = isInjected;
        else if (filterMode === "unadded") matchFilter = (!isFree && !isSel);

        card.style.display = (matchKw && matchFilter) ? "" : "none";
      });
    }

    function updateCNStatsAndFilters() {
      var total = list.length;
      var curatedCount = 0;
      var injectedCount = 0;
      var unaddedCount = 0;

      list.forEach(function (r) {
        var isCur = isCNModelCurated(r.id);
        var isSel = isCNModelSelected(r.id);
        if (isCur) curatedCount++;
        if (isSel) injectedCount++;
        else unaddedCount++;
      });

      if (filterBtns["all"]) filterBtns["all"].textContent = "全部 (" + total + ")";
      if (filterBtns["curated"]) filterBtns["curated"].textContent = "精选 (" + curatedCount + ")";
      if (filterBtns["injected"]) filterBtns["injected"].textContent = "已入国际端 (" + injectedCount + ")";
      if (filterBtns["unadded"]) filterBtns["unadded"].textContent = "未入国际端 (" + unaddedCount + ")";

      var isCustom = (selectedCNModels && selectedCNModels.length > 0);
      var subHint = isCustom ? ("已自选 " + injectedCount + " 款注入") : ("精选 " + injectedCount + " 款注入 (默认)");
      if (statLabel) {
        statLabel.textContent = subHint;
      }
      summaryEl.textContent = "国内模型库与反向注入（" + total + " 个 · " + subHint + "）";
    }

    function applyCNFilter() {
      var kw = searchKeyword.toLowerCase().trim();
      cards.forEach(function (item) {
        var card = item.card;
        var r = item.data;
        var isCur = isCNModelCurated(r.id);
        var isSel = isCNModelSelected(r.id);

        var matchKw = true;
        if (kw) {
          var nameStr = (r.name || "").toLowerCase();
          var idStr = (r.id || "").toLowerCase();
          var descStr = (r.desc || "").toLowerCase();
          matchKw = (nameStr.indexOf(kw) >= 0 || idStr.indexOf(kw) >= 0 || descStr.indexOf(kw) >= 0);
        }

        var matchFilter = true;
        if (filterMode === "curated") matchFilter = isCur;
        else if (filterMode === "injected") matchFilter = isSel;
        else if (filterMode === "unadded") matchFilter = !isSel;

        card.style.display = (matchKw && matchFilter) ? "" : "none";
      });
    }

    if (isIntl) {
      var toolbar = el("div", { class: "mfold-toolbar" });
      var searchWrap = el("div", { class: "mfold-search-wrap" });
      var searchIcon = el("span", { html: ICO("search", "ico-sm"), style: "color:var(--ink-3);display:flex" });
      var searchInput = el("input", {
        type: "text",
        class: "mfold-search",
        placeholder: "搜索模型名称、ID 或描述（如 GPT / Claude / Kimi）…"
      });
      searchInput.addEventListener("input", function (e) {
        searchKeyword = e.target.value || "";
        applyFilter();
      });
      searchWrap.appendChild(searchIcon);
      searchWrap.appendChild(searchInput);
      toolbar.appendChild(searchWrap);

      var filtersWrap = el("div", { class: "mfold-filters" });
      var fTypes = [
        { key: "all", label: "全部" },
        { key: "free", label: "限免" },
        { key: "injected", label: "已入国内端" },
        { key: "unadded", label: "未入付费" }
      ];
      fTypes.forEach(function (ft) {
        var btn = el("button", {
          type: "button",
          class: "mfold-filter-btn" + (filterMode === ft.key ? " active" : ""),
          text: ft.label
        });
        btn.addEventListener("click", function () {
          filterMode = ft.key;
          Object.keys(filterBtns).forEach(function (k) {
            filterBtns[k].classList.remove("active");
          });
          btn.classList.add("active");
          applyFilter();
        });
        filterBtns[ft.key] = btn;
        filtersWrap.appendChild(btn);
      });
      toolbar.appendChild(filtersWrap);

      statLabel = el("div", { class: "mfold-stats" });
      toolbar.appendChild(statLabel);
      details.appendChild(toolbar);

      // 📡 连通性测速雷达控制条
      var radarBar = el("div", { class: "mfold-radar-bar" });
      var radarInfo = el("div", { class: "mfold-radar-info" });
      radarInfo.appendChild(el("span", { class: "pulse-dot" }));
      var fastestLabel = fastestFreeModelId
        ? ("⚡ 当前实测最速限免: " + prettyModel(fastestFreeModelId) + (radarRecordsMap[fastestFreeModelId.toLowerCase()] ? " (" + radarRecordsMap[fastestFreeModelId.toLowerCase()].ttftMs + "ms)" : ""))
        : "⚡ 连通性雷达已就绪 · 点击一键测速";
      radarInfo.appendChild(el("span", { style: "font-weight:600;color:var(--ink-1)", text: fastestLabel }));
      radarBar.appendChild(radarInfo);

      var btnPingAll = el("button", {
        type: "button",
        class: "btn-radar-ping",
        html: ICO("scan", "ico-sm") + " 一键测速全部",
        title: "并发向所有限免大模型发送 1-Token 测试探针，毫秒级测算首字延迟 (TTFT)"
      });
      btnPingAll.addEventListener("click", function () {
        pingAllFreeRadar(btnPingAll);
      });
      radarBar.appendChild(btnPingAll);
      details.appendChild(radarBar);
    } else if (side === "cn") {
      var toolbar = el("div", { class: "mfold-toolbar" });
      var searchWrap = el("div", { class: "mfold-search-wrap" });
      var searchIcon = el("span", { html: ICO("search", "ico-sm"), style: "color:var(--ink-3);display:flex" });
      var searchInput = el("input", {
        type: "text",
        class: "mfold-search",
        placeholder: "搜索国内模型（如 DeepSeek / Kimi / MiniMax / GLM / 混元）…"
      });
      searchInput.addEventListener("input", function (e) {
        searchKeyword = e.target.value || "";
        applyCNFilter();
      });
      searchWrap.appendChild(searchIcon);
      searchWrap.appendChild(searchInput);
      toolbar.appendChild(searchWrap);

      var filtersWrap = el("div", { class: "mfold-filters" });
      var fTypes = [
        { key: "all", label: "全部" },
        { key: "curated", label: "精选推荐" },
        { key: "injected", label: "已入国际端" },
        { key: "unadded", label: "未入国际端" }
      ];
      fTypes.forEach(function (ft) {
        var btn = el("button", {
          type: "button",
          class: "mfold-filter-btn" + (filterMode === ft.key ? " active" : ""),
          text: ft.label
        });
        btn.addEventListener("click", function () {
          filterMode = ft.key;
          Object.keys(filterBtns).forEach(function (k) {
            filterBtns[k].classList.remove("active");
          });
          btn.classList.add("active");
          applyCNFilter();
        });
        filterBtns[ft.key] = btn;
        filtersWrap.appendChild(btn);
      });
      toolbar.appendChild(filtersWrap);

      statLabel = el("div", { class: "mfold-stats" });
      toolbar.appendChild(statLabel);
      details.appendChild(toolbar);

      // 顶部快捷批处理操作条
      var batchBar = el("div", { class: "mfold-radar-bar", style: "justify-content:space-between;padding:5px 8px" });
      var batchInfo = el("div", { style: "font-size:11.5px;color:var(--ink-2);display:flex;align-items:center;gap:6px" }, [
        el("span", { class: "pulse-dot" }),
        el("span", { text: "支持精选推荐 (6款) 快速注入或自主勾选，杜绝 36+ 全量灌入导致国际端模型列表臃肿" })
      ]);
      batchBar.appendChild(batchInfo);

      var batchActions = el("div", { style: "display:flex;gap:6px;align-items:center" });
      var btnResetCurated = el("button", {
        type: "button",
        class: "btn-m-action",
        style: "padding:2px 8px;font-size:11px",
        text: "恢复精选 (6款)",
        title: "重置为默认官方核心精选推荐模型（Hy4, Hy3, DeepSeek-V4.1-Flash, GLM-5.3, MiniMax, Kimi 等）"
      });
      btnResetCurated.addEventListener("click", function () {
        if (btnResetCurated.disabled) return;
        btnResetCurated.disabled = true;
        api("/api/custom-models/cn-selection", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ action: "reset" })
        }).then(function (res) {
          btnResetCurated.disabled = false;
          selectedCNModels = res.selected || [];
          toast("已恢复为默认 6 款精选推荐模型并同步到国际端！", "ok");
          renderUsage();
        }).catch(function (err) {
          btnResetCurated.disabled = false;
          toast("恢复精选失败：" + err.message, "bad");
        });
      });
      batchActions.appendChild(btnResetCurated);

      var btnSelectAll = el("button", {
        type: "button",
        class: "btn-m-action",
        style: "padding:2px 8px;font-size:11px",
        text: "一键全选全部",
        title: "将所有国内可用模型全部勾选并反向注入国际版客户端"
      });
      btnSelectAll.addEventListener("click", function () {
        if (btnSelectAll.disabled) return;
        btnSelectAll.disabled = true;
        api("/api/custom-models/cn-selection", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ action: "all" })
        }).then(function (res) {
          btnSelectAll.disabled = false;
          selectedCNModels = res.selected || [];
          toast("已将全部国内模型全选并同步到国际端！", "ok");
          renderUsage();
        }).catch(function (err) {
          btnSelectAll.disabled = false;
          toast("全选失败：" + err.message, "bad");
        });
      });
      batchActions.appendChild(btnSelectAll);

      var btnSyncNow = el("button", {
        type: "button",
        class: "btn-m-action",
        style: "padding:2px 8px;font-size:11px;color:var(--brand);border-color:rgba(99,179,196,0.3)",
        html: ICO("refresh", "ico-sm") + " 立即同步",
        title: "立即将勾选的国内模型写入国际端 models.json"
      });
      btnSyncNow.addEventListener("click", function () {
        btnSyncNow.disabled = true;
        api("/api/custom-models/sync-cn-to-intl", { method: "POST" })
          .then(function (res) {
            btnSyncNow.disabled = false;
            toast("同步成功：" + (res.note || "已更新国际端 models.json"), "ok");
          })
          .catch(function (err) {
            btnSyncNow.disabled = false;
            toast("同步失败：" + err.message, "bad");
          });
      });
      batchActions.appendChild(btnSyncNow);

      batchBar.appendChild(batchActions);
      details.appendChild(batchBar);
    }

    list.forEach(function (r) {
      var isFree = !!r.free;
      var isCur = isCNModelCurated(r.id);
      var isSel = isIntl ? isIntlModelSelected(r.id) : isCNModelSelected(r.id);
      var isInjected = isIntl ? (isFree || isSel) : isSel;

      var meta = ["速度 " + (r.rate !== undefined ? r.rate.toFixed(2) : "0.00") + "x"];
      if (r.ctx) meta.push((r.ctx >= 1000000 ? (r.ctx / 1000000) + "M" : Math.round(r.ctx / 1000) + "K") + " 窗口");

      var cardKids = [
        el("div", { class: "mrow" }, [
          el("span", { class: "mname", title: (r.id || "") + " · " + (r.raw || ""), text: r.name || prettyModel(r.id) }),
          (!isIntl && isCur) ? el("span", {
            class: "pill",
            style: "font-size:10px;padding:1px 6px;background:rgba(99,179,196,0.15);border:1px solid rgba(99,179,196,0.38);color:var(--brand);font-weight:600",
            text: "精选推荐",
            title: "默认推荐反向注入国际版的核心模型"
          }) : null,
          r.locked ? el("span", {
            class: "pill warn",
            style: "font-size:10px;padding:1px 6px;background:rgba(245,158,11,0.15);border:1px solid rgba(245,158,11,0.38);color:#fbbf24;cursor:help;font-weight:600;display:inline-flex;align-items:center;gap:3px",
            title: (r.tierHint || "该模型官方标注需要更高订阅档位（如 Pro）。当前普通/Free账号直接调用可能受限，但您依然可以自选注入到国内客户端中试用。"),
            html: '<svg class="ico ico-xs" style="width:11px;height:11px"><use href="#i-shield"/></svg><span>' + (r.tierBadge || "需更高档位") + '</span>'
          }) : null,
          r.free ? el("span", { class: "fbadge", text: r.freeLabel || "限时免费" }) : null,
          el("span", { class: "mrate" + (r.free ? " free" : ""), text: (r.rate !== undefined ? r.rate.toFixed(2) : "0.00") + "x" })
        ]),
        r.desc ? el("div", { class: "mdesc", text: r.desc }) : null,
        el("div", { class: "mmeta", text: meta.join(" · ") })
      ];

      var card = el("div", { class: "mcard" + (isInjected ? " injected" : "") }, cardKids);

      if (isIntl) {
        var foot = el("div", { class: "mcard-foot" });
        var capsWrap = el("div", { class: "mcard-caps" });
        if (r.tools) capsWrap.appendChild(el("span", { class: "mcard-cap-tag", text: "Agent工具" }));
        if (r.images) capsWrap.appendChild(el("span", { class: "mcard-cap-tag", text: "多模态" }));
        if (r.maxOutput) capsWrap.appendChild(el("span", { class: "mcard-cap-tag", text: "单次" + Math.round(r.maxOutput / 1000) + "K" }));
        if (r.locked) {
          capsWrap.appendChild(el("span", {
            class: "mcard-cap-tag",
            style: "color:#fbbf24;border-color:rgba(245,158,11,0.3);background:rgba(245,158,11,0.08)",
            text: r.tierBadge || "需更高档位",
            title: r.tierHint || "该模型官方标注需更高订阅档位"
          }));
        }
        // 挂接单模型实时雷达测速胶囊
        capsWrap.appendChild(renderModelRadarPill(r.id));
        foot.appendChild(capsWrap);

        if (isFree) {
          var freeTag = el("span", {
            class: "btn-m-action free-default",
            html: ICO("check", "ico-sm") + " 默认免积分注入",
            title: "限时免费模型已被系统默认全自动注入到国内客户端，零成本使用"
          });
          foot.appendChild(freeTag);
        } else {
          var actBtnHtml = isSel
            ? '<span class="act-added">' + ICO("check", "ico-sm") + ' 已加入国内端</span>' +
              '<span class="act-remove">' + ICO("x", "ico-sm") + ' 移出国内端</span>'
            : ICO("plus", "ico-sm") + ' 加入国内端';

          var btnTitle = isSel
            ? "已加入国内客户端自定义模型（鼠标移上点击即可从国内端移出）"
            : (r.locked ? "加入国内客户端自定义模型（官方标注 " + (r.tierBadge || "需更高档位") + "，调用时消耗国际账号积分）" : "点击一键加入国内客户端自定义模型（调用时消耗国际账号积分）");

          var actBtn = el("button", {
            type: "button",
            class: "btn-m-action" + (isSel ? " added" : ""),
            html: actBtnHtml,
            title: btnTitle
          });

          (function (modelItem, buttonEl, cardEl) {
            buttonEl.addEventListener("click", function () {
              if (buttonEl.disabled) return;
              buttonEl.disabled = true;
              buttonEl.textContent = "同步中…";

              api("/api/custom-models/intl-selection", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ model: modelItem.id, action: "toggle" })
              }).then(function (res) {
                if (res && res.selected) {
                  selectedIntlModels = res.selected;
                }
                var nowIn = isIntlModelSelected(modelItem.id);
                buttonEl.disabled = false;
                if (nowIn) {
                  buttonEl.className = "btn-m-action added";
                  buttonEl.innerHTML = '<span class="act-added">' + ICO("check", "ico-sm") + ' 已加入国内端</span>' +
                                       '<span class="act-remove">' + ICO("x", "ico-sm") + ' 移出国内端</span>';
                  buttonEl.title = "已加入国内客户端自定义模型（鼠标移上点击即可从国内端移出）";
                  cardEl.classList.add("injected");
                  toast("已加入国内客户端：" + (modelItem.name || modelItem.id) + "（费率 " + (modelItem.rate ? modelItem.rate.toFixed(2) + "x" : "标准") + "，重启客户端生效）", "ok");
                } else {
                  buttonEl.className = "btn-m-action";
                  buttonEl.innerHTML = ICO("plus", "ico-sm") + " 加入国内端";
                  buttonEl.title = "点击一键加入国内客户端自定义模型（调用时消耗国际账号积分）";
                  cardEl.classList.remove("injected");
                  toast("已从国内客户端移出：" + (modelItem.name || modelItem.id) + "（重启客户端生效）", "warn");
                }
                updateStatsAndFilters();
                applyFilter();
              }).catch(function (err) {
                buttonEl.disabled = false;
                toast("操作失败：" + err.message, "bad");
              });
            });
          })(r, actBtn, card);

          foot.appendChild(actBtn);
        }

        card.appendChild(foot);
        cards.push({ card: card, data: r });
      } else if (side === "cn") {
        var foot = el("div", { class: "mcard-foot" });
        var capsWrap = el("div", { class: "mcard-caps" });
        if (r.tools) capsWrap.appendChild(el("span", { class: "mcard-cap-tag", text: "Agent工具" }));
        if (r.images) capsWrap.appendChild(el("span", { class: "mcard-cap-tag", text: "多模态" }));
        if (r.maxOutput) capsWrap.appendChild(el("span", { class: "mcard-cap-tag", text: "单次" + Math.round(r.maxOutput / 1000) + "K" }));
        if (isCur) {
          capsWrap.appendChild(el("span", {
            class: "mcard-cap-tag",
            style: "color:var(--brand);border-color:rgba(99,179,196,0.3);background:rgba(99,179,196,0.08)",
            text: "核心精选"
          }));
        }
        foot.appendChild(capsWrap);

        var actBtnHtml = isSel
          ? '<span class="act-added">' + ICO("check", "ico-sm") + ' 已入国际端</span>' +
            '<span class="act-remove">' + ICO("x", "ico-sm") + ' 移出国际端</span>'
          : ICO("plus", "ico-sm") + ' 加入国际端';

        var btnTitle = isSel
          ? "已加入国际客户端自定义模型（点击移出，重启国际端生效）"
          : "点击加入国际客户端自定义模型（通过本地网关反向代理国内后端）";

        var actBtn = el("button", {
          type: "button",
          class: "btn-m-action" + (isSel ? " added" : ""),
          html: actBtnHtml,
          title: btnTitle
        });

        (function (modelItem, buttonEl, cardEl) {
          buttonEl.addEventListener("click", function () {
            if (buttonEl.disabled) return;
            buttonEl.disabled = true;
            buttonEl.textContent = "同步中…";

            api("/api/custom-models/cn-selection", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ model: modelItem.id, action: "toggle" })
            }).then(function (res) {
              if (res && res.selected !== undefined) {
                selectedCNModels = res.selected || [];
              }
              var nowIn = isCNModelSelected(modelItem.id);
              buttonEl.disabled = false;
              if (nowIn) {
                buttonEl.className = "btn-m-action added";
                buttonEl.innerHTML = '<span class="act-added">' + ICO("check", "ico-sm") + ' 已入国际端</span>' +
                                     '<span class="act-remove">' + ICO("x", "ico-sm") + ' 移出国际端</span>';
                buttonEl.title = "已加入国际客户端自定义模型（点击移出，重启国际端生效）";
                cardEl.classList.add("injected");
                toast("已加入国际客户端：" + (modelItem.name || modelItem.id) + "（重启国际版生效）", "ok");
              } else {
                buttonEl.className = "btn-m-action";
                buttonEl.innerHTML = ICO("plus", "ico-sm") + " 加入国际端";
                buttonEl.title = "点击加入国际客户端自定义模型（通过本地网关反向代理国内后端）";
                cardEl.classList.remove("injected");
                toast("已从国际客户端移出：" + (modelItem.name || modelItem.id) + "（重启国际版生效）", "warn");
              }
              updateCNStatsAndFilters();
              applyCNFilter();
            }).catch(function (err) {
              buttonEl.disabled = false;
              toast("操作失败：" + err.message, "bad");
            });
          });
        })(r, actBtn, card);

        foot.appendChild(actBtn);
        card.appendChild(foot);
        cards.push({ card: card, data: r });
      }

      grid.appendChild(card);
    });

    details.appendChild(grid);
    if (isIntl) {
      updateStatsAndFilters();
    } else if (side === "cn") {
      updateCNStatsAndFilters();
    }
    return details;
  }

  function renderUsage() {
    var box = document.getElementById("usage-body");
    var note = document.getElementById("usage-note");
    clear(box);
    if (!usageData) return;

    var live = usageData.live || {};
    var rates = usageData.rates || {};
    if (usageHistoryData) {
      var hLimits = usageHistoryData.limits || [];
      var hCosts = usageHistoryData.costs || [];
      note.textContent = "限流记录 " + hLimits.length + " · 会话消耗 " + hCosts.length +
        (hCosts.length >= 300 ? "（取前 300）" : "");
    } else {
      note.textContent = "实时双端资产与模型就绪 · 历史记录按需展开";
    }

    var cnLv = live["cn"] || {};
    var intlLv = live["intl"] || {};
    var ckSt = (checkinLast && checkinLast.status) || {};

    // 统计国内积分与包
    var cnPkgs = cnLv.packages || [];
    var cnRemainSum = 0, cnTotalSum = 0;
    cnPkgs.forEach(function (p) {
      cnRemainSum += (p.remain || 0);
      cnTotalSum += (p.total || 0);
    });

    // 统计国际积分与包
    var intlPkgs = intlLv.packages || [];
    var intlRemainSum = 0, intlTotalSum = 0;
    intlPkgs.forEach(function (p) {
      intlRemainSum += (p.remain || 0);
      intlTotalSum += (p.total || 0);
    });
    var intlPct = intlTotalSum > 0 ? Math.round(intlRemainSum / intlTotalSum * 100) : (intlRemainSum > 0 ? 100 : 0);

    // 统计限免模型与最早到期
    var promoItems = intlLv.promos || [];
    var minDays = null;
    var promoModelNames = [];
    promoItems.forEach(function (p) {
      (p.modelIds || []).forEach(function (m) {
        if (promoModelNames.indexOf(m) < 0) promoModelNames.push(m);
      });
      var d = daysLeft(p.validUntil);
      if (d !== null && d >= 0) {
        if (minDays === null || d < minDays) minDays = d;
      }
    });

    // ① 顶层三联 KPI 资产透视栏
    var kpiBar = el("div", { class: "kpi-bar" });

    // KPI 1: 国内资产 (CodeBuddy)
    var cnKpiTitle = el("div", { class: "kpi-head" }, [
      el("span", { text: "🇨🇳 国内端额度" }),
      el("span", { class: "pill " + (cnLv.ok ? "ok" : "ghost"), text: cnLv.ok ? (cnLv.credKind === "unsealed" ? "凭据已解密" : "在线") : "待探测" })
    ]);
    var cnKpiVal = el("div", { class: "kpi-val-row" }, [
      el("span", { class: "kpi-num", text: cnTotalSum > 0 ? cnRemainSum.toLocaleString() : (cnLv.ok ? "包月尊享" : "未登录") }),
      el("span", { class: "kpi-unit", text: cnTotalSum > 0 ? "积分" : "无限使用" })
    ]);
    var isSigned = !!(ckSt.todayCheckedIn || (checkinLast && checkinLast.skipped && /already|已签|已领/.test(checkinLast.skipped + (checkinLast.note || ""))));
    var cnKpiFoot = el("div", { class: "kpi-foot", id: "kpi-cn-checkin" }, [
      el("span", { text: isSigned ? ("✅ 今日已签 · 连签 " + (ckSt.streakDays || 1) + " 天") : "⚠️ 今日未打卡 (可领积分)" })
    ]);
    kpiBar.appendChild(el("div", { class: "kpi-card" }, [cnKpiTitle, cnKpiVal, cnKpiFoot]));

    // KPI 2: 国际资产 (WorkBuddy AI)
    var intlKpiTitle = el("div", { class: "kpi-head" }, [
      el("span", { text: "🌐 国际端积分" }),
      el("span", { class: "pill " + (intlLv.ok ? "ok" : "ghost"), text: intlLv.ok ? "实时链路" : "待探测" })
    ]);
    var intlKpiVal = el("div", { class: "kpi-val-row" }, [
      el("span", { class: "kpi-num", text: intlTotalSum > 0 ? intlRemainSum.toFixed(1) : (intlLv.ok ? "活跃通道" : "未探测") }),
      el("span", { class: "kpi-unit", text: intlTotalSum > 0 ? ("点 · " + intlPct + "%") : "" })
    ]);
    var intlKpiFoot = el("div", { class: "kpi-foot" }, [
      el("span", { text: intlTotalSum > 0 ? ("周期总额 " + intlTotalSum.toFixed(1) + " · 额度正常") : (intlLv.ok ? "海外账户已就绪" : "客户端登录后就绪") })
    ]);
    kpiBar.appendChild(el("div", { class: "kpi-card" }, [intlKpiTitle, intlKpiVal, intlKpiFoot]));

    // KPI 3: 限免模型与健康度
    var promoKpiTitle = el("div", { class: "kpi-head" }, [
      el("span", { text: "🔥 限免大模型矩阵" }),
      el("span", { class: "pill ok", text: "0.00x 费率" })
    ]);
    var promoKpiVal = el("div", { class: "kpi-val-row" }, [
      el("span", { class: "kpi-num", text: promoModelNames.length ? (promoModelNames.length + " 款在保") : (promoItems.length ? "官方限免中" : "常规计费") }),
      el("span", { class: "kpi-unit", text: "全天免费" })
    ]);
    var promoKpiFoot = el("div", { class: "kpi-foot" }, [
      el("span", { text: minDays !== null ? ("⏳ 最近模型剩余 " + minDays + " 天到期") : "支持 DeepSeek / Hy4 等模型" })
    ]);
    kpiBar.appendChild(el("div", { class: "kpi-card" }, [promoKpiTitle, promoKpiVal, promoKpiFoot]));

    box.appendChild(kpiBar);

    // ② 双端资产与限免仪表盘 (Dashboard Grid: 左右双卡片并排)
    var grid = el("div", { class: "dash-grid" });

    // --- 左卡片：国内端 CodeBuddy ---
    var cnCard = el("div", { class: "dash-card" });
    var cnAcc = cnLv.nickname || (cnLv.uid ? cnLv.uid.slice(0, 10) + "…" : "未登录/未探测");
    var cnCredPill = cnLv.credKind === "unsealed"
      ? el("span", { class: "pill ok", text: "凭据正常" })
      : cnLv.credKind === "envelope"
        ? el("span", { class: "pill warn", text: "信封加密" })
        : el("span", { class: "pill ghost", text: "无凭据" });

    cnCard.appendChild(el("div", { class: "dash-card-head" }, [
      el("div", { class: "dash-side-info" }, [
        el("div", { class: "dash-side-title" }, [
          el("svg", { class: "ico ico-sm", html: '<use href="#i-server"/>' }),
          "国内版 (CodeBuddy)"
        ]),
        el("div", { class: "dash-user-badge" }, [
          cnAcc,
          cnCredPill
        ])
      ]),
      el("div", { class: "dash-head-actions" }, [
        el("button", {
          class: "ghost",
          style: "padding:3px 10px;font-size:11.5px;white-space:nowrap",
          text: "资产明细",
          title: "在站内直接查看国内版积分包与额度详单",
          onclick: function () { showAssetDetailsModal("cn", cnLv); }
        })
      ])
    ]));

    // 国内积分包
    var cnSecPackages = el("div", {});
    cnSecPackages.appendChild(el("div", { class: "dash-sec-title", text: "积分包余量" }));
    if (!cnPkgs.length) {
      cnSecPackages.appendChild(el("div", { class: "hint", text: "包月制无独立积分包消耗限制，全功能畅用。" }));
    } else {
      cnPkgs.slice(0, 5).forEach(function (p) {
        var pct = p.total > 0 ? Math.round(p.remain / p.total * 100) : 0;
        cnSecPackages.appendChild(el("div", { class: "bar-row" }, [
          el("span", { class: "bar-name", title: p.code, text: "积分包 …" + p.code.slice(-6) }),
          el("span", { class: "bar-track" }, [
            el("span", { class: "bar-fill cn", style: "width:" + Math.max(1.5, pct) + "%" })
          ]),
          el("span", { class: "bar-num", text: formatCreditNum(p.remain) + " / " + formatCreditNum(p.total) + " " + (p.unit || "") })
        ]));
      });
    }
    cnCard.appendChild(cnSecPackages);

    // 国内每日签到状态挂件（带就地打卡）
    var cnCheckinSec = el("div", {
      id: "card-cn-checkin",
      style: "background:rgba(255,255,255,0.02);border:1px solid var(--g4);border-radius:var(--r-sm);padding:9px 12px;display:flex;align-items:center;justify-content:space-between;"
    });
    cnCheckinSec.appendChild(el("div", { id: "card-cn-checkin-text", style: "display:flex;align-items:center;gap:7px;font-size:12px;color:var(--ink-2)" }, [
      el("svg", { class: "ico ico-sm", html: '<use href="#i-calendar"/>' }),
      el("span", { text: isSigned ? ("今日已签 · 连签 " + (ckSt.streakDays || 1) + " 天") : "每日签到：顺手打卡领积分" })
    ]));
    var btnCheckinAction = el("button", {
      id: "btn-cn-checkin-action",
      class: isSigned ? "ghost" : "btn-hero",
      style: "padding:3px 10px;font-size:11px;height:auto",
      text: isSigned ? "查看明细" : "一键打卡"
    });
    btnCheckinAction.onclick = function () {
      if (isSigned) {
        showPage("checkin");
      } else {
        btnCheckinAction.textContent = "打卡中…";
        api("/api/checkin/claim", { method: "POST" }).then(function (r) {
          toast("签到成功 +" + (r.credit || 10) + " 积分", "ok");
          loadUsage(true, true);
        }).catch(function (e) {
          toast(e.message, "bad");
          showPage("checkin");
        });
      }
    };
    cnCheckinSec.appendChild(btnCheckinAction);
    cnCard.appendChild(cnCheckinSec);

    // 国内模型库概览折叠
    var cnRawModels = (cnLv.ok && cnLv.models && cnLv.models.length) ? cnLv.models : (rates["cn"] || []);
    if (cnRawModels.length) {
      var cnInjectedCount = 0;
      cnRawModels.forEach(function (m) {
        if (isCNModelSelected(m.id)) cnInjectedCount++;
      });
      var isCustom = (selectedCNModels && selectedCNModels.length > 0);
      var cnSubTitle = isCustom ? ("已自选 " + cnInjectedCount + " 款入国际端") : ("精选 " + cnInjectedCount + " 款入国际端 (默认)");
      cnCard.appendChild(renderModelDetailsFold("cn", cnRawModels, "国内模型库与反向注入（" + cnRawModels.length + " 个 · " + cnSubTitle + "）"));
    }
    grid.appendChild(cnCard);

    // --- 右卡片：国际端 WorkBuddy AI ---
    var intlCard = el("div", { class: "dash-card" });
    var intlAcc = intlLv.nickname || (intlLv.uid ? intlLv.uid.slice(0, 10) + "…" : "未登录/未探测");
    intlCard.appendChild(el("div", { class: "dash-card-head" }, [
      el("div", { class: "dash-side-info" }, [
        el("div", { class: "dash-side-title" }, [
          el("svg", { class: "ico ico-sm", html: '<use href="#i-layers"/>' }),
          "国际版 (WorkBuddy AI)"
        ]),
        el("div", { class: "dash-user-badge" }, [
          intlAcc,
          el("span", { class: "pill " + (intlLv.ok ? "ok" : "ghost"), text: intlLv.ok ? "实时通路" : "未录入" }),
          (intlLv.ok && intlLv.hasRefreshToken) ? el("span", { class: "pill ok", style: "background:rgba(99,102,241,0.18);border:1px solid rgba(99,102,241,0.38);color:#a5b4fc", text: "⚡ 自动无感保鲜" }) : null
        ])
      ]),
      el("div", { class: "dash-head-actions" }, [
        el("button", {
          class: "ghost",
          style: "padding:3px 10px;font-size:11.5px;white-space:nowrap",
          text: "切换账号 ▾",
          title: "管理并切换已保存的多个国际账号",
          onclick: function () { showAccountsManagerModal("intl"); }
        }),
        intlLv.ok ? el("button", {
          class: "ghost",
          style: "padding:3px 10px;font-size:11.5px;white-space:nowrap",
          text: "资产明细",
          title: "在站内直接查看当前账号积分包与限免模型详单",
          onclick: function () { showAssetDetailsModal("intl", intlLv); }
        }) : el("button", {
          class: "btn-hero",
          style: "padding:3px 10px;font-size:11.5px;white-space:nowrap;height:auto",
          text: "⚡ 登录/录入",
          onclick: function () { showTokenImportModal("intl"); }
        })
      ])
    ]));

    // 未检测到国际版客户端或凭据时的免装指引
    if (!intlLv.ok) {
      var noCredNotice = el("div", {
        style: "background:rgba(99,102,241,0.06);border:1px solid rgba(99,102,241,0.22);border-radius:var(--r-sm);padding:9px 12px;margin:8px 0;display:flex;align-items:center;justify-content:space-between;gap:8px"
      }, [
        el("div", { style: "font-size:12px;color:var(--ink-2);line-height:1.4" }, [
          el("div", { style: "font-weight:600;color:var(--ink-1);margin-bottom:2px", text: "🚀 未安装国际版？无需下载几百MB安装包！" }),
          el("div", { text: "支持官方拟真一键登录（免抓包），自动捕获 Token 并开启机器指纹遥测。" })
        ]),
        el("button", {
          class: "btn-hero",
          style: "padding:4px 12px;font-size:12px;white-space:nowrap;height:auto",
          text: "⚡ 官方拟真登录",
          onclick: function () { showTokenImportModal("intl"); }
        })
      ]);
      intlCard.appendChild(noCredNotice);
    }


    // 国际积分包
    var intlSecPackages = el("div", {});
    intlSecPackages.appendChild(el("div", { class: "dash-sec-title", text: "国际积分包余量" }));
    if (!intlPkgs.length) {
      intlSecPackages.appendChild(el("div", { class: "hint", text: "暂无有效积分包数据" }));
    } else {
      intlPkgs.slice(0, 5).forEach(function (p) {
        var pct = p.total > 0 ? Math.round(p.remain / p.total * 100) : 0;
        intlSecPackages.appendChild(el("div", { class: "bar-row" }, [
          el("span", { class: "bar-name", title: p.code, text: "积分包 …" + p.code.slice(-6) }),
          el("span", { class: "bar-track" }, [
            el("span", { class: "bar-fill intl", style: "width:" + Math.max(1.5, pct) + "%" })
          ]),
          el("span", { class: "bar-num", text: formatCreditNum(p.remain) + " / " + formatCreditNum(p.total) + " " + (p.unit || "") })
        ]));
      });
    }
    intlCard.appendChild(intlSecPackages);

    // 国际端成长中心连续活跃里程碑挂件 (Growth Center Streak)
    if (intlLv.ok) {
      var intlStreakSec = el("div", {
        style: "background:rgba(255,255,255,0.02);border:1px solid var(--g4);border-radius:var(--r-sm);padding:9px 12px;display:flex;align-items:center;justify-content:space-between;gap:8px"
      });
      var streakInfoBox = el("div", { style: "display:flex;align-items:center;gap:7px;font-size:12px;color:var(--ink-2);min-width:0" }, [
        el("svg", { class: "ico ico-sm", html: '<use href="#i-zap"/>' }),
        el("span", { id: "intl-streak-text", text: "连续活跃：读取中…" })
      ]);
      var btnStreakAction = el("button", {
        class: "ghost",
        style: "padding:3px 10px;font-size:11px;height:auto;white-space:nowrap",
        text: "刷新",
        title: "连续活跃可阶梯领取 50/150 积分奖励"
      });
      var streakBtnGroup = el("div", { style: "display:flex;align-items:center;gap:6px;flex-shrink:0" });
      var btnSimulateAction = el("button", {
        class: "primary",
        style: "padding:3px 10px;font-size:11px;height:auto;white-space:nowrap;background:linear-gradient(135deg,#3b82f6,#2563eb);color:#fff;border:none",
        html: '<svg class="ico ico-sm" aria-hidden="true" style="margin-right:3px"><use href="#i-zap"/></svg>模拟活跃 (+30c)',
        title: "向国际后端发起免积分模拟对话并上报桌面链路事件，锁定次日 +30 积分每日活跃"
      });
      var intlSimulateResultCard = el("div", {
        id: "intl-simulate-result-card",
        style: "display:none;margin-top:8px"
      });
      btnSimulateAction.onclick = function () {
        runIntlActiveSimulation(btnSimulateAction, intlSimulateResultCard);
      };
      streakBtnGroup.appendChild(btnSimulateAction);
      streakBtnGroup.appendChild(btnStreakAction);
      intlStreakSec.appendChild(streakInfoBox);
      intlStreakSec.appendChild(streakBtnGroup);
      intlCard.appendChild(intlStreakSec);
      intlCard.appendChild(intlSimulateResultCard);

      // 异步拉取 Streak 状态
      api("/api/growth/streak").then(function (st) {
        var days = st.days || 0;
        var nextTierRemaining = st.nextTierRemaining || 0;
        var claimableTier = null;
        if (st.tiers && st.tiers.length) {
          st.tiers.forEach(function (t) {
            if (t.status === "claimable" && t.credit > 0) {
              claimableTier = t;
            }
          });
        }

        var textEl = document.getElementById("intl-streak-text");
        if (claimableTier) {
          if (textEl) textEl.textContent = "🎉 连续活跃 " + days + " 天 · 可领 " + claimableTier.credit + " 积分！";
          btnStreakAction.className = "btn-hero";
          btnStreakAction.textContent = "一键领 " + claimableTier.credit + " 积分";
          btnStreakAction.onclick = function () {
            btnStreakAction.disabled = true;
            btnStreakAction.textContent = "领取中…";
            api("/api/growth/redeem", {
              method: "POST",
              body: JSON.stringify({ tier: claimableTier.tier })
            }).then(function (r) {
              toast("成功领取 " + r.granted + " 积分！", "ok");
              loadUsage(true, true);
            }).catch(function (e) {
              toast(e.message, "bad");
              btnStreakAction.disabled = false;
              btnStreakAction.textContent = "重试领取";
            });
          };
        } else {
          var desc = days > 0 ? ("已连签 " + days + " 天") : "日常问答自动累积活跃";
          if (nextTierRemaining > 0) {
            desc += " · 距下级奖励剩 " + nextTierRemaining + " 天";
          }
          if (textEl) textEl.textContent = desc;
          btnStreakAction.textContent = "查看明细";
          btnStreakAction.onclick = function () {
            showAssetDetailsModal("intl", intlLv);
          };
        }
      }).catch(function () {
        var textEl = document.getElementById("intl-streak-text");
        if (textEl) textEl.textContent = "日常对话自动点亮活跃";
        btnStreakAction.textContent = "明细";
        btnStreakAction.onclick = function () { showAssetDetailsModal("intl", intlLv); };
      });
    }

    // 🔥 限时免费大模型挂件专区 (Featured Promos)
    var promoSec = el("div", {});
    var promoTitleRow = el("div", { style: "display:flex;align-items:center;justify-content:space-between;margin-bottom:6px" }, [
      el("div", { class: "dash-sec-title", style: "margin:0", text: "限时免费大模型 (0.00x)" }),
      el("button", {
        type: "button",
        class: "btn-radar-ping",
        style: "padding:2px 8px;font-size:10.5px",
        html: ICO("scan", "ico-sm") + " 测速雷达",
        title: "轻量测试当前所有限免模型的真实连通性与响应速度",
        onclick: function (e) {
          e.preventDefault();
          pingAllFreeRadar(this);
        }
      })
    ]);
    promoSec.appendChild(promoTitleRow);

    var promoList = el("div", { class: "promo-list" });
    if (!promoItems.length) {
      promoList.appendChild(el("div", { class: "hint", text: "当前无官方限免公告" }));
    } else {
      promoItems.slice(0, 4).forEach(function (p) {
        var d = daysLeft(p.validUntil);
        var isUrgent = d !== null && d >= 0 && d < 7;
        var countdownTxt = d !== null && d >= 0 ? ("⏳ 剩 " + d + " 天") : (p.validUntil ? freeUntil(p.validUntil) : "限时免费");
        var modelNames = (p.modelIds || []).map(function (m) { return prettyModel(m); }).join(" / ") || (p.label || "特惠模型");

        var primaryId = (p.modelIds && p.modelIds[0]) || "";
        var itemRight = el("div", { style: "display:flex;align-items:center;gap:6px" });
        if (primaryId) {
          itemRight.appendChild(renderModelRadarPill(primaryId));
        }
        itemRight.appendChild(el("span", { class: "promo-countdown" + (isUrgent ? " urgent" : ""), text: countdownTxt }));

        promoList.appendChild(el("div", { class: "promo-item", title: p.textZh || "" }, [
          el("div", { class: "promo-model" }, [
            el("span", { class: "promo-free-tag", text: "0.00x" }),
            el("span", { text: modelNames })
          ]),
          itemRight
        ]));
      });
    }
    promoSec.appendChild(promoList);
    intlCard.appendChild(promoSec);

    // 国际模型库概览折叠
    //
    // 这里必须把接口字段**映射**成渲染层用的名字：接口给的是 freeNow，
    // 而卡片渲染读的是 r.free。直接传原始数组的话 r.free 恒为 undefined，
    // 结果连限免模型都会显示成"未加入的付费模型"，计数与徽章全错。
    var intlSrcModels = (intlLv.ok && intlLv.models && intlLv.models.length) ? intlLv.models : (rates["intl"] || []);
    var intlRawModels = intlSrcModels.map(function (m) {
      return {
        id: m.id,
        name: m.name,
        rate: m.rate,
        raw: m.rateRaw,
        desc: m.desc,
        ctx: m.ctx,
        maxOutput: m.maxOutput,
        tools: m.tools,
        images: m.images,
        free: !!m.freeNow,
        freeLabel: m.freeLabel,
        // 官方 modelTiers 标注的更高档位（Pro Only）：
        // 当前账号调用会被 400 拒绝，界面上不该给勾选入口。
        locked: !!m.locked,
        tierBadge: m.tierBadge,
        tierHint: m.tierHint
      };
    });
    if (intlRawModels.length) {
      var paidCount = (selectedIntlModels && selectedIntlModels.length) || 0;
      intlCard.appendChild(renderModelDetailsFold("intl", intlRawModels, "国际模型库与快捷注入（" + intlRawModels.length + " 个 · 已自选付费 " + paidCount + " 个）"));
    }
    grid.appendChild(intlCard);

    box.appendChild(grid);

    // ③ 本地 OpenAI 兼容接口卡片 (API Card)
    var apiCard = el("div", { class: "api-card" });
    apiCard.appendChild(el("div", { class: "api-card-title" }, [
      el("svg", { class: "ico ico-sm", html: '<use href="#i-term"/>' }),
      "本地 OpenAI 兼容代理接口（无需开启客户端，全工具直连）"
    ]));
    var apiGrid = el("div", { class: "api-grid" });
    apiGrid.appendChild(el("div", { class: "api-label", text: "Base URL" }));
    apiGrid.appendChild(el("div", { class: "api-val", text: location.origin + "/v1" }));
    apiGrid.appendChild(el("div", { class: "api-label", text: "API Key" }));
    apiGrid.appendChild(el("div", { class: "api-val", text: TOKEN }));
    apiGrid.appendChild(el("div", { class: "api-label", text: "Completions" }));
    apiGrid.appendChild(el("div", { class: "api-val", text: "POST " + location.origin + "/v1/chat/completions" }));
    apiGrid.appendChild(el("div", { class: "api-label", text: "Models List" }));
    apiGrid.appendChild(el("div", { class: "api-val", text: "GET " + location.origin + "/v1/models" }));
    apiCard.appendChild(apiGrid);
    apiCard.appendChild(el("div", { class: "hint", style: "margin-top:8px",
      text: "仅转发国际侧模型；限时免费：DeepSeek-V4.1-Flash / Step-5-Preview / Hy3，零成本直接接入任意 AI 客户端。" }));

    var apiActions = el("div", { style: "display:flex;gap:8px;align-items:center;margin-top:10px;flex-wrap:wrap" }, [
      el("button", {
        class: "btn-hero",
        style: "font-size:12px;padding:4px 12px;height:auto",
        html: ICO("upload", "ico-sm") + " ⚡ 一键导入配置至 cc-switch",
        title: "将 WorkBuddy 智能免积分、国内/国际核心大模型一键写入 cc-switch 数据库，可在 Claude Code、OpenCode 或 Codex 中秒级切换使用",
        onclick: function () { showCCSwitchExportModal(); }
      }),
      el("span", { class: "hint", style: "margin:0;font-size:11.5px", text: "一键写入本地 cc-switch (Claude / OpenCode / Codex 客户端)" })
    ]);
    apiCard.appendChild(apiActions);
    box.appendChild(apiCard);

    // ④ 排障参考与历史记录：收进底部折叠（按需异步懒加载，彻底消除首屏扫盘等待）
    var histBody = el("div", { style: "margin-top:10px" });
    var histSummaryText = usageHistoryData
      ? ("排障参考：历史限流记录与会话消耗（" + (usageHistoryData.limits || []).length + " 条限流 · " + (usageHistoryData.costs || []).length + " 条消耗）")
      : "排障参考：历史限流记录与会话消耗（点击展开分析）";
    var histSummary = el("summary", { text: histSummaryText });
    var histDetails = el("details", { class: "fold", style: "margin-top:16px" }, [
      histSummary,
      histBody
    ]);

    function fillHistoryContent(target, h) {
      clear(target);
      var limits = h.limits || [];
      var costs = h.costs || [];
      var byModel = h.byModel || [];

      // 历史瓷砖与模型记录
      var histTiles = el("div", { class: "tiles" });
      (state ? state.backends : []).forEach(function (b) {
        var sum = 0, n = 0;
        costs.forEach(function (c) {
          if (c.side === b.id) { sum += (c.credits || 0); n++; }
        });
        var mine = byModel.filter(function (x) { return x.side === b.id; });
        var rows = mine.slice(0, 6).map(function (x) {
          var cls, icon, txt;
          if (x.kind === "exhausted") {
            cls = "bad"; icon = "cross";
            txt = prettyModel(x.model) + "：额度用尽，需充值";
          } else if (x.resetMs && x.resetMs < Date.now()) {
            cls = "ok"; icon = "check";
            txt = prettyModel(x.model) + "：上次限流已过期";
          } else {
            cls = "warn"; icon = "clock";
            txt = prettyModel(x.model) + "：" + countdown(x.resetMs) + "后到期";
          }
          return el("div", { class: "q-hist", title: x.resetAt || "" }, [
            el("span", { class: "q-ico " + cls, html: ICO(icon, "ico-sm") }),
            el("span", { text: txt })
          ]);
        });
        histTiles.appendChild(el("div", { class: "tile" }, [
          el("div", { class: "desc", text: sideLabel(b.id) + " 历史统计" }),
          el("div", { class: "big " + (!mine.length ? "ok" : "warn"),
            text: !mine.length ? "近 7 天无限流记录" : "近 7 天 " + mine.length + " 个模型被限过" }),
          !mine.length ? el("div", { class: "desc", text: "其间一直可用。" }) : rows,
          el("div", { class: "desc", text: "期间消耗合计 " + sum.toFixed(2) + " 积分（" + n + " 条会话）" })
        ]));
      });
      target.appendChild(histTiles);

      // 消耗榜
      if (costs.length) {
        target.appendChild(el("div", { class: "hint", style: "margin:10px 0 4px", text: "消耗榜 · 前 10（积分，蓝=国内 绿=国际）" }));
        var maxCost = 0;
        costs.forEach(function (c) { if ((c.credits || 0) > maxCost) maxCost = c.credits; });
        costs.slice(0, 10).forEach(function (c) {
          var title = c.title || "会话 " + String(c.sessionId || "").slice(0, 8);
          var w = maxCost > 0 ? Math.max(1.5, (c.credits || 0) / maxCost * 100) : 0;
          target.appendChild(el("div", { class: "bar-row" }, [
            el("span", { class: "bar-name", title: title, text: title }),
            el("span", { class: "bar-track" }, [
              el("span", { class: "bar-fill " + c.side, style: "width:" + w + "%" })
            ]),
            el("span", { class: "bar-num", text: (c.credits || 0).toFixed(2) })
          ]));
        });
      }

      // 历史警告信息
      (h.warnings || []).forEach(function (w) {
        target.appendChild(el("div", { class: "note warn", style: "margin-top:10px", text: w }));
      });
      target.appendChild(el("div", { class: "hint", style: "margin-top:8px",
        text: "数据来自客户端本地记录，升级后可能失效；credit 不是钱。" }));
    }

    if (usageHistoryData) {
      fillHistoryContent(histBody, usageHistoryData);
    } else {
      histBody.appendChild(el("div", { class: "hint", style: "padding:10px 0", text: "点击展开后将后台异步读取历史限流与会话消耗记录…" }));
    }

    histDetails.addEventListener("toggle", function () {
      if (!histDetails.open) return;
      if (usageHistoryData || usageHistoryLoading) return;
      usageHistoryLoading = true;
      clear(histBody);
      histBody.appendChild(el("div", { class: "hint", style: "padding:10px 0", text: "正在深入分析历史会话记录…" }));
      api("/api/usage/history").then(function (h) {
        usageHistoryData = h;
        usageHistoryLoading = false;
        var hLimits = h.limits || [];
        var hCosts = h.costs || [];
        histSummary.textContent = "排障参考：历史限流记录与会话消耗（" + hLimits.length + " 条限流 · " + hCosts.length + " 条消耗）";
        fillHistoryContent(histBody, h);
        if (note) {
          note.textContent = "限流记录 " + hLimits.length + " · 会话消耗 " + hCosts.length +
            (hCosts.length >= 300 ? "（取前 300）" : "");
        }
      }).catch(function (e) {
        usageHistoryLoading = false;
        clear(histBody);
        histBody.appendChild(el("div", { class: "note bad", text: "读取历史会话记录失败：" + e.message }));
      });
    });

    box.appendChild(histDetails);
  }
