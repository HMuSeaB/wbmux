
  // 令牌由服务端写进访问地址，作为后续接口调用的凭据。
  // 界面不把它存到 localStorage：每次启动都是新的，没有跨会话复用的必要。
  var TOKEN = new URLSearchParams(location.search).get("t") || "";

  var state = null;      // /api/state 的结果
  var target = null;     // 当前选中的目标后端 id
  var nativeMode = false;
  var busy = false;
  var selectedIntlModels = [];
  var selectedCNModels = [];
  var curatedCNModels = ["hy4free", "hy3", "deepseek-v4.1-flash", "glm-5.3-flash", "glm-5.3", "minimax-m2.5", "kimi-k2.6"];
  // selectedFromState 标记"已选模型"是否已经由 /api/state 初始化过。
  // /api/state 读本地配置、毫秒级返回；/api/usage 要拉双端账单，
  // 实测能到 20s+。两者之间的空档里任何一次重画都会把勾选状态显示成 0，
  // 所以一旦 state 回来，就不许慢接口再覆盖它（见 loadUsage）。
  var selectedFromState = false;

  // ---------- 连通性雷达状态池 ----------
  var radarRecordsMap = {};
  var fastestFreeModelId = "";
  var hasHealthyFreeModel = false;
  var radarLoading = false;

  function loadModelRadar(silent, callback) {
    api("/api/model-radar/status")
      .then(function (res) {
        if (res && res.records) {
          res.records.forEach(function (r) {
            if (r && r.model) radarRecordsMap[String(r.model).toLowerCase()] = r;
          });
        }
        if (res && res.fastestFreeModel) fastestFreeModelId = res.fastestFreeModel;
        if (res && res.hasHealthyFree !== undefined) hasHealthyFreeModel = res.hasHealthyFree;
        if (callback) callback();
      }).catch(function () {});
  }

  function pingAllFreeRadar(btnEl) {
    if (radarLoading) return;
    radarLoading = true;
    if (btnEl) {
      btnEl.disabled = true;
      btnEl.innerHTML = '<span class="radar-spinning">' + ICO("reload", "ico-sm") + '</span> 测速中…';
    }

    api("/api/model-radar/ping", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({})
    }).then(function (res) {
      radarLoading = false;
      if (btnEl) {
        btnEl.disabled = false;
        btnEl.innerHTML = ICO("scan", "ico-sm") + " 一键测速全部";
      }
      if (res && res.records) {
        res.records.forEach(function (r) {
          if (r && r.model) radarRecordsMap[String(r.model).toLowerCase()] = r;
        });
      }
      if (res && res.fastestFreeModel) fastestFreeModelId = res.fastestFreeModel;
      if (res && res.hasHealthyFree !== undefined) hasHealthyFreeModel = res.hasHealthyFree;
      var fastestName = fastestFreeModelId ? prettyModel(fastestFreeModelId) : "完成";
      toast("全量限免模型测速完成！当前推荐最速：" + fastestName, "ok");
      renderUsage();
    }).catch(function (err) {
      radarLoading = false;
      if (btnEl) {
        btnEl.disabled = false;
        btnEl.innerHTML = ICO("scan", "ico-sm") + " 一键测速全部";
      }
      toast("批量测速失败：" + err.message, "bad");
    });
  }

  function renderModelRadarPill(modelId) {
    var mid = String(modelId || "").toLowerCase();
    var rec = radarRecordsMap[mid];
    var pillClass = "untested";
    var pillHtml = '<span class="radar-dot"></span> 未测速';
    var tip = "点击对该模型进行即时单点测速";

    if (rec) {
      if (rec.status === "fast") {
        pillClass = "fast";
        pillHtml = '<span class="radar-dot"></span> ' + (rec.ttftMs || 0) + 'ms 极速';
        tip = "首字延迟 " + rec.ttftMs + "ms · 连通畅快（点击重测）";
      } else if (rec.status === "normal") {
        pillClass = "normal";
        pillHtml = '<span class="radar-dot"></span> ' + (rec.ttftMs || 0) + 'ms 正常';
        tip = "首字延迟 " + rec.ttftMs + "ms · 正常连通（点击重测）";
      } else if (rec.status === "slow") {
        pillClass = "slow";
        pillHtml = '<span class="radar-dot"></span> ' + (rec.ttftMs || 0) + 'ms 延迟';
        tip = "首字延迟 " + rec.ttftMs + "ms · 响应偏慢（点击重测）";
      } else if (rec.status === "down") {
        if (rec.isRateLimited || (rec.statusCode === 429 && rec.resetAtHuman)) {
          pillClass = "rate-limited";
          var shortTime = "";
          if (rec.resetAtHuman) {
            var parts = rec.resetAtHuman.split(" ");
            shortTime = parts.length > 1 ? parts[1].substring(0, 5) : rec.resetAtHuman;
          }
          pillHtml = '<span class="radar-dot"></span> 429 频控' + (shortTime ? ' (' + shortTime + '重置)' : '');
          tip = "官方限免频控限制\n· 预计重置时刻：" + (rec.resetAtHuman || "未知") + (rec.resetRemainingDesc ? " (" + rec.resetRemainingDesc + ")" : "") + "\n· 建议：可切换至其他可用限免模型继续对话\n· 点击可发起即时测速重试";
        } else {
          pillClass = "down";
          pillHtml = '<span class="radar-dot"></span> ' + (rec.statusCode ? rec.statusCode + " 异常" : "不可达");
          tip = (rec.errorMsg || "上游超时或不可达") + "（点击重试）";
        }
      }
    }

    var pill = el("span", {
      class: "radar-pill " + pillClass,
      html: pillHtml,
      title: tip
    });

    pill.addEventListener("click", function (e) {
      e.stopPropagation();
      if (pill.getAttribute("data-loading")) return;
      pill.setAttribute("data-loading", "true");
      pill.innerHTML = '<span class="radar-spinning">' + ICO("reload", "ico-sm") + '</span> 测速中…';

      api("/api/model-radar/ping", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ model: modelId })
      }).then(function (res) {
        pill.removeAttribute("data-loading");
        if (res && res.records && res.records.length) {
          var r = res.records[0];
          if (r && r.model) radarRecordsMap[String(r.model).toLowerCase()] = r;
        }
        if (res && res.fastestFreeModel) fastestFreeModelId = res.fastestFreeModel;
        if (res && res.hasHealthyFree !== undefined) hasHealthyFreeModel = res.hasHealthyFree;
        var latencyStr = (res && res.records && res.records[0] && res.records[0].ttftMs) ? res.records[0].ttftMs + "ms" : "完成";
        toast("已完成 " + prettyModel(modelId) + " 探针测速（" + latencyStr + "）", "ok");
        renderUsage();
      }).catch(function (err) {
        pill.removeAttribute("data-loading");
        toast("测速失败：" + err.message, "bad");
        renderUsage();
      });
    });

    return pill;
  }

  function isIntlModelSelected(modelId) {
    if (!modelId) return false;
    var targetId = String(modelId).toLowerCase();
    for (var i = 0; i < selectedIntlModels.length; i++) {
      if (String(selectedIntlModels[i]).toLowerCase() === targetId) return true;
    }
    return false;
  }

  function isCNModelCurated(modelId) {
    if (!modelId) return false;
    var targetId = String(modelId).toLowerCase();
    for (var i = 0; i < curatedCNModels.length; i++) {
      if (String(curatedCNModels[i]).toLowerCase() === targetId) return true;
    }
    return false;
  }

  function isCNModelSelected(modelId) {
    if (!modelId) return false;
    var targetId = String(modelId).toLowerCase();
    if (!selectedCNModels || selectedCNModels.length === 0) {
      return isCNModelCurated(targetId);
    }
    for (var i = 0; i < selectedCNModels.length; i++) {
      if (String(selectedCNModels[i]).toLowerCase() === targetId) return true;
    }
    return false;
  }

  // ---------- 工具 ----------

  // ICO 生成一个内联图标元素。文本按钮在运行时改文案时必须用它，
  // 不能用 textContent——那会把图标子节点一起清掉。
  function ICO(name, extra) {
    return '<svg class="ico' + (extra ? " " + extra : "") + '" aria-hidden="true">'
      + '<use href="#i-' + name + '"/></svg>';
  }

  function el(tag, attrs, kids) {
    var n = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        var v = attrs[k];
        if (v === null || v === undefined || v === false) return;
        if (k === "class") n.className = v;
        else if (k === "text") n.textContent = v;   // 一律走 textContent，路径里出现尖括号也不会被解析
        else if (k === "html") n.innerHTML = v;
        else if (k.slice(0, 2) === "on") n.addEventListener(k.slice(2), v);
        else n.setAttribute(k, v);
      });
    }
    if (kids && !Array.isArray(kids)) {
      kids = [kids];
    }
    (kids || []).forEach(function add(c) {
      if (c === null || c === undefined || c === false) return;
      // 数组递归展开：嵌套分组时直接传数组，别让 appendChild(数组) 炸掉
      if (Array.isArray(c)) return c.forEach(add);
      n.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    });
    return n;
  }

  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  function api(path, opts) {
    opts = opts || {};
    var headers = Object.assign({ "X-Wbmux-Token": TOKEN }, opts.headers || {});
    opts.headers = headers;
    return fetch(path, opts).then(function (res) {
      return res.json().catch(function () { return { error: "响应无法解析" }; })
        .then(function (data) {
          if (!res.ok) throw new Error(data && data.error ? data.error : "HTTP " + res.status);
          return data;
        });
    });
  }

  function toast(msg, kind) {
    var t = el("div", { class: "toast" + (kind ? " " + kind : ""), text: msg });
    document.body.appendChild(t);
    setTimeout(function () { t.remove(); }, kind === "bad" ? 6000 : 3000);
  }

  function pill(text, kind) { return el("span", { class: "pill " + kind, text: text }); }
