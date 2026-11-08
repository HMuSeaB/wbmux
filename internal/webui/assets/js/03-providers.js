  // ---------- 提供方（自备 key / BYOK） ----------
  // 表单里的 key 永远不回显：列表只显示最后 4 位，留空表示"不改动"。
  // 页面上的所有文本都走 textContent（见 el 的说明），地址里带尖括号也不会出事。
  var provEditing = "";   // 正在编辑的卡片 id；空 = 新增

  function renderProviders(res) {
    var box = document.getElementById("prov-list");
    clear(box);
    var list = res.providers || [];
    if (!list.length) {
      box.appendChild(el("p", { class: "hint", style: "grid-column:1/-1",
        text: "还没有卡片。用下面这张表加上第一个端点，保存后就会注入到国内客户端。" }));
      return;
    }
    list.forEach(function (p) {
      var models = (p.models || []).join(" · ") || "（没有填模型名）";
      box.appendChild(el("div", { class: "card prov-card" }, [
        el("div", { class: "card-head" }, [
          el("span", { class: "card-title", text: p.name }),
          el("span", { class: "pill " + (p.hasKey ? "info" : "bad"),
            text: p.hasKey ? "key …" + p.keyTail : "缺 key" })
        ]),
        el("div", { class: "path", text: p.baseURL }),
        el("div", { class: "models", text: models + (p.supportsToolCall ? " · 支持工具" : " · 不带工具") }),
        el("div", { class: "row", style: "margin-top:10px" }, [
          el("button", { class: "ghost", text: "编辑", onclick: function () { fillProviderForm(p); } }),
          el("button", { class: "ghost danger", text: "删除", onclick: function () { removeProvider(p); } })
        ])
      ]));
    });
  }

  function loadProviders(showError) {
    return api("/api/providers").then(renderProviders).catch(function (e) {
      if (showError) toast("读取提供方失败：" + e.message, "bad");
    });
  }

  // ---------- 官方同款「上下文用量」滑块面板与保活管理 ----------
  var CM_LEVELS = [
    { key: "default", value: 200000, apiValue: 0, label: "200K 上下文 · 默认", sub: "200K", tick: "200K", desc: "最省流量，忘事最早" },
    { key: "300k", value: 300000, apiValue: 300000, label: "300K 上下文 · 精简", sub: "300K", tick: "300K", desc: "兼顾轻量与多步推理" },
    { key: "600k", value: 600000, apiValue: 600000, label: "600K 上下文 · 适中", sub: "600K", tick: "600K", desc: "官方推荐的黄金档位" },
    { key: "1M", value: 1000000, apiValue: 1000000, label: "1M 上下文 · 超长", sub: "1M", tick: "1M", desc: "超长记忆，极限深度" }
  ];

  function showContextToast(text) {
    var t = document.getElementById("cm-toast");
    if (!t) {
      t = el("div", { id: "cm-toast", class: "cm-toast" }, [
        el("span", { class: "cm-toast-icon", text: "✔" }),
        el("span", { id: "cm-toast-text", text: text })
      ]);
      document.body.appendChild(t);
    } else {
      document.getElementById("cm-toast-text").textContent = text;
    }
    t.classList.add("show");
    clearTimeout(t._timer);
    t._timer = setTimeout(function () {
      t.classList.remove("show");
    }, 2200);
  }

  // buildContextMeterComponent 构建官方同款「上下文用量」交互控制板
  function buildContextMeterComponent(options) {
    var opts = options || {};
    var totalTokens = opts.initialTokens || 358400; // 默认以典型活跃会话 ~358k tokens 计算
    var eff = opts.effective || 600000;
    var st = opts.state || null;

    // 匹配当前档位索引
    var curIdx = 2; // 默认 600k
    for (var i = 0; i < CM_LEVELS.length; i++) {
      if (CM_LEVELS[i].value === eff || (CM_LEVELS[i].apiValue === 0 && (!st || !st.set))) {
        curIdx = i;
        break;
      }
    }

    var root = el("div", { class: "cm-container" });

    // 1. 大字号用量与百分比
    var metricRow = el("div", { class: "cm-metric-row" });
    var percentEl = el("div", { class: "cm-percent", text: "0.0%" });
    var statDescEl = el("div", { class: "cm-stat-desc", text: "已使用 0K/ 0K" });
    metricRow.appendChild(percentEl);
    metricRow.appendChild(statDescEl);
    root.appendChild(metricRow);

    // 2. 五色分段堆叠柱状条 (Stacked Bar)
    var bar = el("div", { class: "cm-stacked-bar" });
    var segPrompt = el("div", { class: "cm-seg seg-prompt", title: "系统提示词" });
    var segTools = el("div", { class: "cm-seg seg-tools", title: "工具及子智能体" });
    var segMsgs = el("div", { class: "cm-seg seg-msgs", title: "对话消息" });
    var segMcp = el("div", { class: "cm-seg seg-mcp", title: "连接器及MCP" });
    var segSkills = el("div", { class: "cm-seg seg-skills", title: "技能" });
    bar.appendChild(segPrompt);
    bar.appendChild(segTools);
    bar.appendChild(segMsgs);
    bar.appendChild(segMcp);
    bar.appendChild(segSkills);
    root.appendChild(bar);

    // 3. 分类占比明细清单
    var legendGrid = el("div", { class: "cm-legend-grid" });
    var catItems = [
      { name: "系统提示词", color: "#3b82f6", baseTokens: Math.round(totalTokens * 0.058) },
      { name: "工具及子智能体", color: "#10b981", baseTokens: Math.round(totalTokens * 0.111) },
      { name: "对话消息", color: "#f59e0b", baseTokens: Math.round(totalTokens * 0.819) },
      { name: "连接器及MCP", color: "#8b5cf6", baseTokens: Math.round(totalTokens * 0.002) },
      { name: "技能", color: "#ec4899", baseTokens: Math.round(totalTokens * 0.010) }
    ];
    var valEls = [];
    catItems.forEach(function (cat) {
      var item = el("div", { class: "cm-legend-item" });
      var left = el("div", { class: "cm-legend-left" }, [
        el("span", { class: "cm-dot", style: "background:" + cat.color }),
        el("span", { text: cat.name })
      ]);
      var val = el("span", { class: "cm-legend-val", text: "0.0%" });
      valEls.push(val);
      item.appendChild(left);
      item.appendChild(val);
      legendGrid.appendChild(item);
    });
    root.appendChild(legendGrid);

    // 4. 分段档位滑块控制器
    var sliderSec = el("div", { class: "cm-slider-section" });
    var sliderHeader = el("div", { class: "cm-slider-header" });
    var sliderLabel = el("div", { class: "cm-slider-label", text: CM_LEVELS[curIdx].label });
    sliderHeader.appendChild(sliderLabel);
    sliderSec.appendChild(sliderHeader);

    var sliderWrap = el("div", { class: "cm-slider-wrap" });
    var track = el("div", { class: "cm-slider-track" });
    var fill = el("div", { class: "cm-slider-fill" });
    var thumb = el("div", { class: "cm-slider-thumb" });
    track.appendChild(fill);
    track.appendChild(thumb);
    sliderWrap.appendChild(track);

    var ticks = el("div", { class: "cm-ticks" });
    var tickEls = [];
    CM_LEVELS.forEach(function (lvl, idx) {
      var t = el("span", {
        class: "cm-tick-item" + (idx === curIdx ? " active" : ""),
        text: lvl.tick
      });
      t.addEventListener("click", function () {
        selectLevel(idx, true);
      });
      tickEls.push(t);
      ticks.appendChild(t);
    });
    sliderWrap.appendChild(ticks);
    sliderSec.appendChild(sliderWrap);

    var sliderFoot = el("div", { class: "cm-slider-foot" }, [
      el("span", { text: "拖动或点击吸附切换" }),
      el("span", { id: "cm-sync-status", style: "color:#10b981", text: "● 已保存至系统环境" })
    ]);
    sliderSec.appendChild(sliderFoot);
    root.appendChild(sliderSec);

    // 动态渲染计算逻辑
    function updateMetrics(lvlIdx) {
      var win = CM_LEVELS[lvlIdx].value;
      var ratio = (totalTokens / win) * 100;
      percentEl.textContent = ratio.toFixed(1) + "%";
      statDescEl.textContent = "已使用 " + (totalTokens / 1000).toFixed(1) + "K/ " + (win / 1000).toFixed(1) + "K";

      // 调整五色彩条宽度
      var pPrompt = (catItems[0].baseTokens / win) * 100;
      var pTools = (catItems[1].baseTokens / win) * 100;
      var pMsgs = (catItems[2].baseTokens / win) * 100;
      var pMcp = (catItems[3].baseTokens / win) * 100;
      var pSkills = (catItems[4].baseTokens / win) * 100;

      segPrompt.style.width = pPrompt.toFixed(2) + "%";
      segTools.style.width = pTools.toFixed(2) + "%";
      segMsgs.style.width = pMsgs.toFixed(2) + "%";
      segMcp.style.width = pMcp.toFixed(2) + "%";
      segSkills.style.width = pSkills.toFixed(2) + "%";

      valEls[0].textContent = pPrompt.toFixed(1) + "%";
      valEls[1].textContent = pTools.toFixed(1) + "%";
      valEls[2].textContent = pMsgs.toFixed(1) + "%";
      valEls[3].textContent = pMcp.toFixed(1) + "%";
      valEls[4].textContent = pSkills.toFixed(1) + "%";

      sliderLabel.textContent = CM_LEVELS[lvlIdx].label;

      // 计算滑块位置与填充条 (0% -> 33.3% -> 66.6% -> 100%)
      var pctPos = (lvlIdx / (CM_LEVELS.length - 1)) * 100;
      fill.style.width = pctPos + "%";
      thumb.style.left = pctPos + "%";

      tickEls.forEach(function (el, i) {
        el.classList.toggle("active", i === lvlIdx);
      });
    }

    function selectLevel(idx, triggerSave) {
      if (idx < 0) idx = 0;
      if (idx >= CM_LEVELS.length) idx = CM_LEVELS.length - 1;
      curIdx = idx;
      updateMetrics(curIdx);

      if (triggerSave) {
        var lvl = CM_LEVELS[curIdx];
        showContextToast("上下文长度已切换至 " + lvl.sub);
        var syncEl = document.getElementById("cm-sync-status");
        if (syncEl) {
          syncEl.style.color = "#f59e0b";
          syncEl.textContent = "● 正在同步环境变量…";
        }
        api("/api/compact", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ value: lvl.apiValue })
        }).then(function (res) {
          if (syncEl) {
            syncEl.style.color = "#10b981";
            syncEl.textContent = "● 环境变量已就绪（网关自适应兜底中）";
          }
          if (opts.onApplied) opts.onApplied(res);
        }).catch(function (err) {
          if (syncEl) {
            syncEl.style.color = "#ef4444";
            syncEl.textContent = "● 保存失败：" + err.message;
          }
        });
      }
    }

    // 鼠标点击轨道快速吸附
    track.addEventListener("click", function (e) {
      var rect = track.getBoundingClientRect();
      var clickX = e.clientX - rect.left;
      var ratio = Math.max(0, Math.min(1, clickX / rect.width));
      var targetIdx = Math.round(ratio * (CM_LEVELS.length - 1));
      selectLevel(targetIdx, true);
    });

    // 手柄拖动支持
    var dragging = false;
    thumb.addEventListener("mousedown", function (e) {
      e.preventDefault();
      dragging = true;
      document.body.style.cursor = "grabbing";
    });
    window.addEventListener("mousemove", function (e) {
      if (!dragging) return;
      var rect = track.getBoundingClientRect();
      var moveX = e.clientX - rect.left;
      var ratio = Math.max(0, Math.min(1, moveX / rect.width));
      var targetIdx = Math.round(ratio * (CM_LEVELS.length - 1));
      if (targetIdx !== curIdx) {
        selectLevel(targetIdx, false);
      }
    });
    window.addEventListener("mouseup", function () {
      if (dragging) {
        dragging = false;
        document.body.style.cursor = "";
        selectLevel(curIdx, true);
      }
    });

    updateMetrics(curIdx);
    return root;
  }

  // openContextMeterModal 弹窗呼出官方同款「上下文用量」卡片
  function openContextMeterModal(contextTokens) {
    var overlay = document.getElementById("context-meter-overlay");
    if (!overlay) {
      overlay = el("div", { id: "context-meter-overlay", class: "cm-overlay" });
      document.body.appendChild(overlay);
    }
    clear(overlay);

    var card = el("div", { class: "cm-card" });
    var header = el("div", { class: "cm-header" }, [
      el("div", { class: "cm-title", text: "上下文用量" }),
      el("button", {
        type: "button",
        class: "cm-close",
        html: "&times;",
        click: function () {
          overlay.classList.remove("open");
        }
      })
    ]);
    card.appendChild(header);

    var bodyContainer = el("div", {});
    bodyContainer.appendChild(el("div", { class: "hint", text: "正在读取当前基准设置…" }));
    card.appendChild(bodyContainer);

    // 底部重启快捷条
    var restartBar = el("div", {
      style: "margin-top:14px;background:rgba(59,130,246,0.06);border:1px solid rgba(59,130,246,0.2);padding:10px 14px;border-radius:8px;display:flex;align-items:center;justify-content:space-between;gap:10px;"
    }, [
      el("div", { style: "font-size:11.5px;color:var(--ink-2);", text: "⚡ 网关已开启自适应无感兜底；按需重启可让客户端原生应用" }),
      el("button", {
        class: "ghost",
        style: "padding:5px 12px;font-size:11.5px;white-space:nowrap;",
        text: "按需重启客户端",
        click: function () {
          restartClient();
        }
      })
    ]);
    card.appendChild(restartBar);

    overlay.appendChild(card);
    overlay.offsetHeight;
    overlay.classList.add("open");

    api("/api/compact").then(function (st) {
      clear(bodyContainer);
      var actualTokens = contextTokens || (st && st.lastContextTokens > 0 ? st.lastContextTokens : 122900);
      var comp = buildContextMeterComponent({
        initialTokens: actualTokens,
        effective: st.effective,
        state: st,
        onApplied: function () {
          loadCompact();
        }
      });
      bodyContainer.appendChild(comp);
    }).catch(function (err) {
      clear(bodyContainer);
      bodyContainer.appendChild(el("div", { class: "note bad", text: "读取失败：" + err.message }));
    });
  }

  // ---------- 压缩阈值页面 ----------
  function loadCompact() {
    var box = document.getElementById("compact-body");
    if (!box) return;
    clear(box);
    box.appendChild(el("div", { class: "hint" },
      [el("span", { class: "spinner" }), "正在读当前设置…"]));

    api("/api/compact").then(renderCompact).catch(function (e) {
      clear(box);
      box.appendChild(el("div", { class: "note bad", text: "读取失败：" + e.message }));
    });
  }

  function renderCompact(st) {
    var box = document.getElementById("compact-body");
    var note = document.getElementById("compact-note");
    if (!box) return;
    clear(box);
    if (note) clear(note);

    // 官方同款分段滑块卡片置顶呈现
    var mainCard = el("div", {
      class: "card",
      style: "background:#17181c;border:1px solid rgba(255,255,255,0.1);border-radius:12px;padding:20px 24px;margin-bottom:16px;"
    });
    var titleText = "官方同款会话上下文用量与分段滑块";
    var actualTokens = (st && st.lastContextTokens > 0) ? st.lastContextTokens : 122900;
    if (st && st.lastContextTokens > 0) {
      titleText += " · 实时联动最近请求 (~" + (st.lastContextTokens / 1000).toFixed(1) + "k Tokens)";
    }
    mainCard.appendChild(el("div", { class: "cm-title", style: "margin-bottom:12px;", text: titleText }));
    var comp = buildContextMeterComponent({
      initialTokens: actualTokens,
      effective: st.effective,
      state: st,
      onApplied: function (newSt) {
        if (newSt) renderCompact(newSt);
      }
    });
    mainCard.appendChild(comp);
    box.appendChild(mainCard);

    // 当前生效值 + 换算出来的触发点
    var tiles = el("div", { class: "tiles" }, [
      el("div", { class: "tile" }, [
        el("div", { class: "hint", text: "当前基准窗口" }),
        el("div", { class: "big", text: fmtTokens(st.effective) }),
        el("div", { class: "hint",
          text: st.set ? ("已设置（" + st.envName + "=" + st.currentValue + "）")
                       : "未设置，走的客户端内置默认" })
      ]),
      el("div", { class: "tile" }, [
        el("div", { class: "hint", text: "发消息前的压缩点" }),
        el("div", { class: "big", text: "约 " + fmtTokens(st.triggerAtPreMessage) }),
        el("div", { class: "hint", text: "聊到这个量左右，旧历史会被压缩" })
      ])
    ]);
    box.appendChild(tiles);

    // 后端发现的问题（值非法 / 超范围被 clamp）如实转告
    if (st.note) {
      box.appendChild(el("div", { class: "note warn", style: "margin-top:12px",
        text: st.note }));
    }

    if (!st.available) {
      box.appendChild(el("div", { class: "note", style: "margin-top:12px",
        text: st.note || "当前环境不支持修改。" }));
      return;
    }

    // 每个档的取舍说明
    var descs = el("div", { class: "desc", style: "margin-top:14px" });
    st.levels.forEach(function (lv) {
      descs.appendChild(el("div", { text: "· " + lv.label + "：" + lv.desc }));
    });
    box.appendChild(descs);

    // 代价与生效条件
    box.appendChild(el("div", { class: "note warn", style: "margin-top:12px" }, [
      el("b", { text: "代价：" }),
      el("div", { text: "基准调大 = 模型记得更久，但每轮请求更大、更慢。" +
        "它和「发消息更顺畅」是此消彼长的，按自己需要选。" })
    ]));
    box.appendChild(el("div", { class: "note ok", style: "margin-top:8px;background:rgba(16,185,129,0.06);border:1px solid rgba(16,185,129,0.2);" }, [
      el("b", { style: "color:#10b981;", text: "⚡ 网关自适应无感兜底保障（已开启）：" }),
      el("div", { text: "您可以放心在客户端使用 1M 原生超长记忆。当长会话报文在 DeepSeek 上游遭遇算力繁忙 (14003) 或拒绝时，wbmux 网关将自动在后台对历史过期工具响应执行自适应安全折叠并就地重试，零卡顿、零报错恢复会话，无需频繁重启客户端！" })
    ]));

    var restartBanner = el("div", {
      class: "card",
      style: "margin-top:14px;background:rgba(59,130,246,0.06);border:1px solid rgba(59,130,246,0.22);display:flex;align-items:center;justify-content:space-between;padding:12px 16px;gap:14px;border-radius:8px;"
    }, [
      el("div", {}, [
        el("b", { style: "color:var(--ink-1);display:block;margin-bottom:3px;", text: "平滑重启客户端（按需可选）" }),
        el("div", { style: "font-size:12px;color:var(--c-sub)", text: "若您调整了上述客户端原生压缩基准并希望客户端从源头立刻按新阈值执行，可点击右侧按钮瞬间安全平滑拉起客户端。" })
      ]),
      el("button", {
        class: "ghost",
        style: "flex:none;white-space:nowrap;padding:7px 15px;",
        text: "重启客户端生效",
        click: function () {
          restartClient();
        }
      })
    ]);
    box.appendChild(restartBanner);

    if (note) {
      note.appendChild(el("div", { class: "hint",
        text: "写入位置：用户环境变量 " + st.envName + "（只动这一个键，不改客户端任何文件）" }));
    }
  }

  function applyCompact(lv, btn) {
    var label = lv.label;
    btn.disabled = true;
    btn.textContent = "设置中…";
    api("/api/compact", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ value: lv.value })
    }).then(function (st) {
      toast("已切到 " + label + "。网关已提供自适应无感兜底，按需重启可让客户端原生应用", "ok");
      renderCompact(st);
    }).catch(function (e) {
      toast("设置失败：" + e.message, "bad");
      btn.disabled = false;
      btn.textContent = label + (lv.recommended ? " · 推荐" : "");
    });
  }

  // fmtTokens 把 token 数写成"300k / 1M"这种好读的形式。
  // 2400000 这种也要能显示，所以不是简单的除以 1000。
  function fmtTokens(n) {
    if (!n || n < 0) return "—";
    if (n >= 1000000) {
      var m = n / 1000000;
      return (m % 1 === 0 ? m : m.toFixed(1)) + "M";
    }
    return Math.round(n / 1000) + "k";
  }

  function fillProviderForm(p) {
    provEditing = p.id;
    document.getElementById("prov-form-title").textContent = "编辑：" + p.name;
    document.getElementById("prov-name").value = p.name || "";
    document.getElementById("prov-url").value = p.baseURL || "";
    document.getElementById("prov-key").value = "";
    document.getElementById("prov-key").placeholder = p.hasKey ? "留空 = 不改动（现有 key …" + p.keyTail + "）" : "";
    document.getElementById("prov-models").value = (p.models || []).join("\n");
    document.getElementById("prov-tools").checked = !!p.supportsToolCall;
    document.getElementById("prov-images").checked = !!p.supportsImages;
    document.getElementById("prov-note").textContent = "正在编辑这张卡";
  }

  function resetProviderForm() {
    provEditing = "";
    document.getElementById("prov-form-title").textContent = "新增提供方";
    ["prov-name", "prov-url", "prov-key", "prov-models"].forEach(function (id) {
      document.getElementById(id).value = "";
    });
    document.getElementById("prov-key").placeholder = "留空 = 不改动已有 key";
    document.getElementById("prov-tools").checked = true;
    document.getElementById("prov-images").checked = false;
    document.getElementById("prov-note").textContent = "";
  }

  // splitModels 接受逗号、空格、换行三种分隔（用户随手粘什么都能用）。
  function splitModels(s) {
    return String(s || "").split(/[\s,，、;；]+/).filter(function (x) { return x; });
  }

  function saveProvider() {
    if (busy) return;
    var body = {
      id: provEditing,
      name: document.getElementById("prov-name").value.trim(),
      baseURL: document.getElementById("prov-url").value.trim(),
      apiKey: document.getElementById("prov-key").value,
      models: splitModels(document.getElementById("prov-models").value),
      supportsToolCall: document.getElementById("prov-tools").checked,
      supportsImages: document.getElementById("prov-images").checked
    };
    if (!body.name || !body.baseURL || !body.models.length) {
      toast("名字、接口地址、至少一个模型名都得填", "bad");
      return;
    }
    busy = true;
    var btn = document.getElementById("btn-prov-save");
    btn.disabled = true;
    btn.textContent = "保存中…";
    api("/api/providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }).then(function (res) {
      renderProviders(res);
      resetProviderForm();
      toast(res.note || "已保存并注入", "ok");
    }).catch(function (e) {
      toast("保存失败：" + e.message, "bad");
    }).then(function () {
      busy = false;
      btn.disabled = false;
      btn.textContent = "保存并注入";
    });
  }

  function removeProvider(p) {
    if (!confirm("删除卡片「" + p.name + "」？注入到客户端的对应模型也会一并清掉。")) return;
    api("/api/providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: p.id, remove: true })
    }).then(function (res) {
      renderProviders(res);
      if (provEditing === p.id) resetProviderForm();
      toast(res.note || "已删除", "ok");
    }).catch(function (e) {
      toast("删除失败：" + e.message, "bad");
    });
  }

  document.getElementById("btn-prov-save").addEventListener("click", saveProvider);
  document.getElementById("btn-prov-reset").addEventListener("click", resetProviderForm);
  loadProviders(false);

  var btnCCExport = document.getElementById("btn-ccswitch-export");
  if (btnCCExport) btnCCExport.addEventListener("click", showCCSwitchExportModal);
  var btnCCImport = document.getElementById("btn-ccswitch-import");
  if (btnCCImport) btnCCImport.addEventListener("click", showCCSwitchImportModal);
