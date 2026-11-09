  // ---------- 渲染：顶部 ----------

  function renderHeader() {
    // 版本号由构建期注入，本地 go build 时是 dev。
    // 直接拼 "v" 会显示成 "vdev"，所以只在像版本号时才加前缀。
    var v = state.version || "";
    document.getElementById("ver").textContent =
      /^\d/.test(v) ? "v" + v : v;
    document.getElementById("settings-note").textContent =
      state.settings.path ? "设置文件：" + state.settings.path : "";

    // 启动器发现"你手上是另一个版本"时会带 &newver= 打开界面：说明它在
    // 复用旧实例（刻意设计——不打断正在跑的东西），得告诉你换版本的办法，
    // 否则表现成"重新编译了、双击了，界面还是旧的"（2026-09-28 反馈）。
    var note = document.getElementById("stale-note");
    var nv = new URLSearchParams(location.search).get("newver");
    if (nv && state.version && nv !== state.version) {
      note.hidden = false;
      note.textContent = "运行中的界面是 " + state.version + "，你刚打开的是 " + nv +
        "。想换成新版本：点侧边栏「关闭界面」，再双击一次 wbmux.exe（或运行 wbmux.exe --restart）。";
      note.style.color = "var(--warn)";
      toast("界面还是旧版本 " + state.version + "，换版本要先退出它", "bad");
    }
  }

  // ---------- 渲染：提示条 ----------

  function renderBanner() {
    var box = document.getElementById("banner");
    clear(box);


    if (state.hostNote) {
      box.appendChild(el("div", { class: "note bad" }, [
        el("b", { text: "找不到可用的宿主客户端。" }),
        el("div", { text: state.hostNote }),
        el("div", { class: "hint", text: "确认至少安装了一套客户端，或用 --exe 显式指定主程序路径。" })
      ]));
      return;
    }

    // 目标与宿主不同档位时，说明会被影响的已知功能。
    // 放在选择之前而不是埋在文档里：这是切换后端唯一的功能性代价。
    if (target && target !== state.hostId) {
      box.appendChild(el("div", { class: "note warn" }, [
        el("b", { text: "「远程控制」里的微信系渠道会失效。" }),
        el("div", { text: "微信小程序 / 微信客服 / 微信 bot / 企微回复都走目标后端域名下的 /v2/backgroundagent/… 代理接口，而那个域名正是被改写的 endpoint 字段。" }),
        el("div", { style: "margin-top:6px", text: "Slack / Discord / Telegram / 企微 WebSocket 模式是客户端直连服务商，不受影响。" }),
        el("div", { style: "margin-top:6px", text: "更麻烦的是入口仍然会显示——渠道开关住在 productFeatures 里，该块整块透传宿主，所以会「看得见、点不动」。" })
      ]));
    }
  }

  // ---------- 渲染：双端控制台卡片 ----------

  function renderInstalls() {
    var box = document.getElementById("installs");
    if (!box) return;
    clear(box);

    (state.installs || []).forEach(function (inst) {
      var isHost = inst.id === state.hostId;
      var card = el("div", { class: "card", style: "display:flex;flex-direction:column;justify-content:space-between;gap:12px;padding:16px;background:var(--g2);border:1px solid rgba(255,255,255,0.06);border-radius:10px;" });

      var proc = (state.clients && state.clients[inst.id]) || { running: !!inst.running, pids: inst.pids || [] };
      var isRunning = !!proc.running;

      var titleText = inst.id === "cn" ? "🇨🇳 国内版 (CodeBuddy)" : (inst.id === "intl" ? "🌐 国际版 (WorkBuddy AI)" : inst.displayName);
      var badges = [];
      if (isRunning) {
        var pidDesc = "";
        if (proc.pids && proc.pids.length) {
          if (proc.pids.length <= 2) {
            pidDesc = " · PID " + proc.pids.join(",");
          } else {
            pidDesc = " · PID " + proc.pids[0] + " (+" + (proc.pids.length - 1) + ")";
          }
        }
        var runPill = pill("🟢 运行中" + pidDesc, "ok");
        if (proc.pids && proc.pids.length) {
          runPill.title = "已探测到 " + proc.pids.length + " 个相关进程 PID: " + proc.pids.join(", ");
        }
        badges.push(runPill);
      } else {
        badges.push(pill("⚪ 未启动", "ghost"));
      }

      if (inst.found) {
        if (isHost) badges.push(pill("当前宿主", "info"));
        else badges.push(pill("已安装", "ghost"));
      } else {
        badges.push(pill("未安装", "bad"));
      }

      var head = el("div", { class: "card-head", style: "margin-bottom:2px;display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:6px;" }, [
        el("span", { class: "card-title", style: "font-size:14px;font-weight:600;white-space:nowrap;", text: titleText }),
        el("div", { style: "display:flex;gap:6px;align-items:center;flex-wrap:wrap;" }, badges)
      ]);

      var topPart = el("div", { style: "display:flex;flex-direction:column;gap:8px;" }, [head]);

      if (!inst.found) {
        topPart.appendChild(el("div", { class: "path", style: "margin-top:4px;", text: (inst.problems || []).join("；") || "未在本机找到该版本安装。" }));
        card.appendChild(topPart);
        box.appendChild(card);
        return;
      }

      var meta = [];
      if (inst.version) meta.push(inst.version + (inst.build ? " (" + inst.build + ")" : ""));
      if (inst.source) meta.push("定位：" + inst.source);
      topPart.appendChild(el("div", { class: "card-sub", text: meta.join("　") }));

      var kvDl = el("dl", { class: "kv", style: "margin-top:6px;" }, [
        el("dt", { text: "主程序" }), el("dd", { text: inst.executable, title: inst.executable }),
        el("dt", { text: "数据目录" }), el("dd", { text: inst.dataDir, title: inst.dataDir })
      ]);
      topPart.appendChild(kvDl);

      // 模型通道与代理状态提示
      var chanBox = el("div", {
        style: "background:rgba(0,0,0,0.22);border:1px solid rgba(255,255,255,0.06);border-radius:6px;padding:8px 10px;font-size:11.5px;margin-top:4px;"
      });
      if (inst.id === "cn") {
        chanBox.innerHTML =
          '<div style="font-weight:600;color:var(--accent);display:flex;align-items:center;gap:5px;margin-bottom:3px;">' +
            ICO("up", "ico-sm") + '<span>正向代理通道：国际限免大模型注入</span>' +
          '</div>' +
          '<div style="color:var(--ink-2);line-height:1.4;">' +
            '通过 ~/.workbuddy/models.json 注入国际版 0 费率模型（如 DeepSeek-V4.1-Flash），经 127.0.0.1:2239 代理路由，微信远控正常运行。' +
          '</div>';
      } else {
        chanBox.innerHTML =
          '<div style="font-weight:600;color:var(--brand);display:flex;align-items:center;gap:5px;margin-bottom:3px;">' +
            ICO("download", "ico-sm") + '<span>反向代理通道：国内专享精选模型注入</span>' +
          '</div>' +
          '<div style="color:var(--ink-2);line-height:1.4;">' +
            '通过 ~/.workbuddy-ai/models.json 精选注入 MiniMax-M2.5、GLM-5.1、Kimi 等 6 款核心模型，直连国内大模型接口。' +
          '</div>';
      }
      topPart.appendChild(chanBox);
      card.appendChild(topPart);

      // 底部操作按钮群
      var actions = el("div", { style: "display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-top:8px;padding-top:10px;border-top:1px solid rgba(255,255,255,0.06);" });

      if (isRunning) {
        var btnRestart = el("button", {
          class: "primary",
          style: "padding:5px 11px;font-size:12px;",
          title: "关闭该端后以最新环境重新启动（不影响另一侧客户端）"
        }, [
          el("svg", { class: "ico ico-sm", style: "width:12px;height:12px;" }, [el("use", { href: "#i-reload" })]),
          el("span", { text: "重启该端生效" })
        ]);
        btnRestart.addEventListener("click", function () { restartSide(inst.id); });
        actions.appendChild(btnRestart);

        var btnTerm = el("button", {
          class: "danger ghost",
          style: "padding:5px 10px;font-size:12px;",
          title: "仅退出该端客户端进程（保留另一侧）"
        }, [
          el("svg", { class: "ico ico-sm", style: "width:12px;height:12px;" }, [el("use", { href: "#i-x" })]),
          el("span", { text: "退出该端" })
        ]);
        btnTerm.addEventListener("click", function () { terminateSide(inst.id); });
        actions.appendChild(btnTerm);
      } else {
        var btnLaunch = el("button", {
          class: "primary",
          style: "padding:5px 12px;font-size:12px;",
          title: "启动该版本客户端"
        }, [
          el("svg", { class: "ico ico-sm", style: "width:12px;height:12px;" }, [el("use", { href: "#i-play" })]),
          el("span", { text: "启动此客户端" })
        ]);
        btnLaunch.addEventListener("click", function () { launchSide(inst.id); });
        actions.appendChild(btnLaunch);
      }

      var btnCfg = el("button", {
        class: "ghost",
        style: "padding:5px 9px;font-size:12px;",
        title: "前往额度与模型页配置"
      }, [
        el("svg", { class: "ico ico-sm", style: "width:12px;height:12px;" }, [el("use", { href: "#i-sliders" })]),
        el("span", { text: "模型与额度" })
      ]);
      btnCfg.addEventListener("click", function () { showPage("status"); });
      actions.appendChild(btnCfg);

      var btnFolder = el("button", {
        class: "ghost",
        style: "padding:5px 9px;font-size:12px;",
        title: "在文件资源管理器中打开数据目录"
      }, [
        el("svg", { class: "ico ico-sm", style: "width:12px;height:12px;" }, [el("use", { href: "#i-folder" })]),
        el("span", { text: "打开目录" })
      ]);
      btnFolder.addEventListener("click", function () { openClientFolder(inst.dataDir); });
      actions.appendChild(btnFolder);

      card.appendChild(actions);
      box.appendChild(card);
    });
  }

  // ---------- 渲染：后端 ----------

  function renderBackends() {
    var box = document.getElementById("backends");
    clear(box);

    state.backends.forEach(function (b) {
      var canRun = !!state.canRun[b.id];
      var selected = target === b.id;

      var cls = "card selectable";
      if (selected) cls += " selected";
      if (!canRun) cls += " disabled";

      var card = el("div", {
        class: cls,
        onclick: function () {
          if (!canRun || busy) return;
          target = b.id;
          renderBackends();
          renderBanner();
          loadPreview();
        }
      });

      var badges = [];
      if (b.isHost) badges.push(pill("宿主", "ok"));
      if (b.installed) badges.push(pill("本机已装", "info"));
      if (selected) badges.push(pill("已选中", "info"));

      card.appendChild(el("div", { class: "card-head" }, [
        el("span", { class: "card-title", text: b.displayName }),
        el("span", {}, badges)
      ]));

      card.appendChild(el("dl", { class: "kv" }, [
        el("dt", { text: "后端" }), el("dd", { text: b.endpoint }),
        el("dt", { text: "数据目录" }), el("dd", { text: "~/" + b.dataFolderName })
      ]));

      if (!canRun) {
        card.appendChild(el("div", { class: "hint", text: "宿主不可用，无法启动。" }));
      }

      box.appendChild(card);
    });

    document.getElementById("native").checked = nativeMode;
  }

  // ---------- 渲染：预览 ----------

  function splitChange(s) {
    var m = /^(.+?):\s(.*?)\s→\s(.*)$/.exec(s);
    return m ? { field: m[1], from: m[2], to: m[3] } : { raw: s };
  }

  function renderPreview(pv) {
    var sec = document.getElementById("preview-sec");
    var box = document.getElementById("preview-body");
    sec.hidden = false;
    clear(box);

    box.appendChild(el("div", { class: "row", style: "margin-bottom:12px" }, [
      pill(pv.hostName + " → " + pv.targetName, "info"),
      pv.native ? pill("原生模式：不改写配置", "warn") : null,
      pill("目标 " + pv.targetUrl, "ghost")
    ]));

    if (!pv.native && pv.changes && pv.changes.length) {
      var rows = pv.changes.map(function (c) {
        var p = splitChange(c);
        if (p.raw) {
          return el("tr", {}, [el("td", { class: "from", colspan: "3", text: p.raw })]);
        }
        return el("tr", {}, [
          el("td", { class: "field", text: p.field }),
          el("td", { class: "from", text: p.from }),
          el("td", { class: "to", text: p.to })
        ]);
      });

      box.appendChild(el("table", { class: "changes" }, [
        el("thead", {}, [el("tr", {}, [
          el("th", { text: "字段" }), el("th", { text: "当前" }), el("th", { text: "改为" })
        ])]),
        el("tbody", {}, rows)
      ]));
      box.appendChild(el("p", { class: "hint", text: "其余字段原样透传，包括 productName / updates / productFeatures。" }));
    } else if (pv.native) {
      box.appendChild(el("p", { class: "hint", text: "原生模式不生成补丁配置，直接启动目标档位自己的安装。" }));
    } else {
      box.appendChild(el("p", { class: "hint", text: "无需改写：宿主与目标后端已经是同一档位。" }));
    }

    if (pv.warnings && pv.warnings.length) {
      box.appendChild(el("div", { class: "note warn", style: "margin-top:12px" },
        [el("b", { text: "提示" })].concat(pv.warnings.map(function (w) {
          return el("div", { text: w });
        }))));
    }

    box.appendChild(el("dl", { class: "kv", style: "margin-top:14px" }, [
      el("dt", { text: "主程序" }), el("dd", { text: pv.executable }),
      pv.configPath ? el("dt", { text: "配置" }) : null,
      pv.configPath ? el("dd", { text: pv.configPath }) : null,
      el("dt", { text: "数据目录" }), el("dd", { text: pv.dataDir })
    ]));

    if (pv.commandLine) {
      box.appendChild(el("div", { class: "cmdline", text: pv.commandLine }));
    }

    var btn = el("button", {
      class: "primary",
      style: "margin-top:14px",
      text: pv.launched ? "已启动" : "启动客户端",
      disabled: pv.launched,
      onclick: launch
    });
    btn.id = "btn-launch";
    box.appendChild(btn);

    // 侧边栏那个启动按钮与这里同步：它一直在视野里，页面里的这个可能被滚走。
    // 两处都调同一个 launch()，状态由 syncLaunchBtn() 统一更新。
    syncLaunchBtn(pv.launched);
  }

  // syncLaunchBtn 把"能不能启动"这一个状态同步到侧边栏按钮上。
  // 预览还没出来前藏起来——那时还没有目标后端，点了也没用。
  function syncLaunchBtn(state, label) {
    var b = document.getElementById("btn-launch-side");
    if (!b) return;
    if (state === "hide") { b.hidden = true; return; }
    b.hidden = false;
    b.disabled = (state === true);
    b.innerHTML = ICO("play", "ico-sm") + (label || (state === true ? "已启动" : "启动客户端"));
  }

  function renderPreviewError(msg) {
    var sec = document.getElementById("preview-sec");
    var box = document.getElementById("preview-body");
    sec.hidden = false;
    clear(box);
    box.appendChild(el("div", { class: "note bad", text: msg }));
    // 预览没出来就说明还没有可启动的东西（生成中 / 出错），
    // 侧边栏那个按钮跟着收起来，避免点了没反应。
    syncLaunchBtn("hide");
  }

  // ---------- 渲染：体检 ----------

  function renderDoctor(rep) {
    var box = document.getElementById("doctor-body");
    clear(box);

    box.appendChild(el("div", {
      class: "note " + (rep.passed ? "ok" : "bad"),
      style: "margin-bottom:12px",
      text: rep.summary
    }));

    rep.checks.forEach(function (c) {
      var kind = c.level === "OK" ? "ok"
        : c.level === "警告" ? "warn"
        : c.level === "失败" ? "bad" : "ghost";
      box.appendChild(el("div", { class: "check" }, [
        el("span", { class: "lvl" }, [pill(c.level, kind)]),
        el("span", { class: "body" }, [
          el("div", { class: "nm", text: c.name }),
          c.detail ? el("div", { class: "dt", text: c.detail }) : null,
          c.hint ? el("div", { class: "ht", text: c.hint }) : null
        ])
      ]));
    });
  }

  // ---------- 行为 ----------

  function loadPreview() {
    if (!target) return;
    renderPreviewError("正在生成预览…");
    api("/api/preview", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: target, native: nativeMode })
    }).then(renderPreview).catch(function (e) {
      renderPreviewError(e.message);
    });
  }

  // ---------- 代理日志 ----------
  // 每 2 秒轮询一次，而不是用 SSE/长连接：这个服务要能"没人用就自动退出"，
  // 长连接会把它钉住。页面不可见时也不拉，省得白转。
  var LOG_INTERVAL = 2000;

  function logPageActive() {
    var sec = document.querySelector('main [data-page="log"]');
    return sec && !sec.hidden;
  }

  function renderLiveSessionCard(list) {
    var container = document.getElementById("live-session-card");
    if (!container) return;
    clear(container);

    if (!list || !list.length) return;

    var latestReq = null;
    var latestDone = null;
    for (var i = 0; i < list.length; i++) {
      var item = list[i];
      if (!latestDone && item.text && item.text.indexOf("转发完成") >= 0) {
        latestDone = item;
      }
      if (!latestReq && (item.bodyBytes || (item.text && item.text.indexOf("收到请求") >= 0))) {
        latestReq = item;
      }
      if (latestReq && latestDone) break;
    }

    if (!latestReq && !latestDone) return;

    var model = (latestReq && latestReq.model) || (latestDone && latestDone.model) || "未知模型";
    var bytes = (latestReq && latestReq.bodyBytes) || 0;
    var tokens = (latestReq && latestReq.tokens) || (bytes ? Math.round(bytes / 5.5) : 0);
    var scale = (latestReq && latestReq.contextScale) || (bytes >= 1500 * 1024 ? "overload" : (bytes >= 600 * 1024 ? "heavy" : "normal"));

    // 容错：如果还没有拿到结构化 bodyBytes，从文本中正则提取
    if (!bytes && latestReq && latestReq.text) {
      var mBytes = latestReq.text.match(/正文\s+([0-9.]+\s*[KMGT]?B)/i);
      var sizeStr = mBytes ? mBytes[1] : "";
      var mTok = latestReq.text.match(/~([0-9]+k?\s*tokens?)/i);
      var tokStr = mTok ? "~" + mTok[1] : "";
    }

    var sizeDisplay = bytes ? (bytes >= 1048576 ? (bytes / 1048576).toFixed(1) + " MB" : (bytes / 1024).toFixed(1) + " KB") : (sizeStr || "近期未测");
    var tokenDisplay = tokens ? "~" + Math.round(tokens / 1000) + "k tokens" : (tokStr || "");

    var badgeText = "🟢 会话体量健康";
    var badgeClass = "pill ok";
    var tipText = "会话体量轻盈敏捷，首字延迟与响应速度处于极佳状态。";
    var cardBorder = "rgba(74, 157, 106, 0.28)";
    var cardBg = "rgba(74, 157, 106, 0.05)";

    if (scale === "overload" || bytes >= 1500 * 1024 || (sizeStr && parseFloat(sizeStr) >= 1.5 && sizeStr.indexOf("MB") >= 0)) {
      badgeText = "⚡ 密集超载预警";
      badgeClass = "pill warn";
      tipText = "当前上下文体积过大（已超 300k tokens 门槛），极易引发上游算力繁忙 (14003) 或网络超时，强烈建议在当前步骤完成后开启新对话。";
      cardBorder = "rgba(234, 88, 12, 0.4)";
      cardBg = "rgba(234, 88, 12, 0.08)";
    } else if (scale === "heavy" || bytes >= 600 * 1024 || (sizeStr && parseFloat(sizeStr) >= 600 && sizeStr.indexOf("KB") >= 0)) {
      badgeText = "🟡 深度长会话";
      badgeClass = "pill";
      tipText = "会话上下文逐步累积深入，建议关注长任务进度并适时开启新对话。";
      cardBorder = "rgba(184, 134, 59, 0.35)";
      cardBg = "rgba(184, 134, 59, 0.06)";
    }

    var card = el("div", {
      class: "card live-session-box",
      style: "border:1px solid " + cardBorder + ";background:" + cardBg + ";padding:14px 18px;border-radius:10px;margin-bottom:12px;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:14px;"
    }, [
      el("div", { style: "display:flex;align-items:center;gap:18px;flex-wrap:wrap;" }, [
        el("div", {}, [
          el("div", { style: "display:flex;align-items:center;gap:8px;margin-bottom:4px;" }, [
            el("span", { class: badgeClass, style: "font-size:11px;padding:1px 8px;font-weight:600;", text: badgeText }),
            el("code", { style: "font-size:13px;color:var(--ink-1);font-weight:600;font-family:var(--mono);", text: model })
          ]),
          el("div", { style: "font-size:12px;color:var(--c-sub);line-height:1.5;max-width:540px;", text: tipText })
        ]),
        el("div", {
          style: "border-left:1px solid var(--border-soft);padding-left:18px;cursor:pointer;",
          title: "点击呼出官方同款「上下文用量」分析与分段滑块切换",
          onclick: function () {
            openContextMeterModal(tokens || 358400);
          }
        }, [
          el("div", { style: "font-size:11px;color:var(--ink-3);display:flex;align-items:center;gap:4px;", text: "当前最新上下文体积 · 点击切换" }),
          el("div", { style: "display:flex;align-items:baseline;gap:6px;" }, [
            el("span", { style: "font-size:20px;font-weight:700;color:var(--ink-1);font-family:var(--mono);", text: sizeDisplay }),
            tokenDisplay ? el("span", { style: "font-size:12px;color:var(--ink-3);font-family:var(--mono);", text: tokenDisplay }) : null
          ])
        ])
      ]),
      el("div", { style: "display:flex;gap:8px;flex:none;" }, [
        el("button", {
          class: "btn-sm",
          html: "⚙️ 上下文切换",
          title: "打开官方同款上下文用量与分段滑块",
          click: function () {
            openContextMeterModal(tokens || 358400);
          }
        }),
        el("button", {
          class: "btn-sm",
          text: "📊 网关大屏",
          click: function () {
            showPage("gateway");
          }
        })
      ])
    ]);

    container.appendChild(card);
  }

  var lastSilentRadarSync = 0;
  function triggerSilentRadarSync() {
    var now = Date.now();
    if (now - lastSilentRadarSync < 3000) return;
    lastSilentRadarSync = now;
    loadModelRadar(true);
  }

  function renderProxyLog(res) {
    var box = document.getElementById("log-body");
    clear(box);
    var list = res.entries || [];
    var ok = 0, bad = 0, up = 0;
    list.forEach(function (e) {
      if (e.level === "ok") ok++;
      else if (e.level === "warn") up++;
      else if (e.level === "bad") bad++;
    });

    // 渲染置顶当前会话体量感知卡片
    renderLiveSessionCard(list);

    // 流量被动感知：如果最近发生过 429 频控或成功转发，自动防抖对齐雷达
    if (list.length > 0) {
      var latest = list[0];
      if (latest && (latest.level === "bad" || latest.level === "ok" || (latest.text && (latest.text.indexOf("429") >= 0 || latest.text.indexOf("完成") >= 0)))) {
        triggerSilentRadarSync();
      }
    }

    // 摘要里把"上游故障"单列出来
    document.getElementById("log-summary").textContent = list.length
      ? "最近 " + list.length + " 条 · 成功 " + ok + " · 失败 " + bad
        + (up ? " · 上游故障 " + up : "")
      : "";

    if (!list.length) {
      box.appendChild(el("div", { class: "hint",
        text: "还没有请求。在国内客户端里选「（国际免费）」的模型发条消息，这里就会出现。" }));
      return;
    }
    var wrap = el("div", { class: "log-list" });
    list.forEach(function (e) {
      var text = e.text || "";
      var tagClass = "info";
      var tagLabel = "日志";

      if (text.indexOf("收到请求") >= 0) {
        tagClass = "req";
        tagLabel = "📥 收到";
      } else if (text.indexOf("转发完成") >= 0) {
        tagClass = "done";
        tagLabel = "📤 完成";
      } else if (text.indexOf("重试") >= 0 || text.indexOf("防抖") >= 0) {
        tagClass = "warn";
        tagLabel = "⚡ 防抖";
      } else if (e.level === "bad" || text.indexOf("拒绝") >= 0 || text.indexOf("失败") >= 0) {
        tagClass = "bad";
        tagLabel = "❌ 异常";
      }

      var lineKids = [
        el("span", { class: "log-time", text: e.at || "" }),
        el("span", { class: "log-tag " + tagClass, text: tagLabel }),
        el("span", { class: "log-text", text: text })
      ];
      if (e.tools > 0) {
        lineKids.push(el("span", {
          class: "log-badge-tools",
          text: "🛠️ " + e.tools,
          title: "调用方携带了 " + e.tools + " 个工具定义 (Agent 模式)"
        }));
      }
      wrap.appendChild(el("div", { class: "log-line log-" + (e.level || "info") }, lineKids));
    });
    box.appendChild(wrap);
  }

  function loadProxyLog(showError) {
    return api("/api/proxy-log").then(renderProxyLog).catch(function (e) {
      if (!showError) return;   // 自动刷新失败不刷屏，手动刷新才报
      var box = document.getElementById("log-body");
      clear(box);
      box.appendChild(el("div", { class: "note bad", text: "读取日志失败：" + e.message }));
    });
  }

  document.getElementById("btn-log-refresh").addEventListener("click", function () {
    loadProxyLog(true);
  });
  setInterval(function () {
    if (!logPageActive()) return;
    if (!document.getElementById("log-live").checked) return;
    loadProxyLog(false);
  }, LOG_INTERVAL);
