  // ---------- 网关调用分析与实时审计 (Phase 17) ----------
  var gwLoading = false;
  function loadGatewayAudit(manual) {
    if (gwLoading) return;
    gwLoading = true;
    var statusLabel = document.getElementById("gw-table-status-label");
    if (statusLabel) statusLabel.textContent = "正在拉取网关审计数据…";

    api("/api/gateway/stats?limit=100")
      .then(function (summary) {
        gwLoading = false;
        if (statusLabel) statusLabel.textContent = "已更新 (" + new Date().toLocaleTimeString() + ")";
        renderGatewayAudit(summary);
      })
      .catch(function (err) {
        gwLoading = false;
        if (statusLabel) statusLabel.textContent = "拉取失败：" + err.message;
        if (manual) toast("拉取网关统计失败：" + err.message, "bad");
      });
  }

  var gwAuditData = null;
  var gwSelectedDay = null; // null 表示 7 日全貌，或 "YYYY-MM-DD"

  function renderGatewayAudit(s) {
    if (!s) return;
    gwAuditData = s;
    var daily = s.dailyStats || [];

    var todayStr = (function () {
      var d = new Date();
      return d.getFullYear() + "-" +
        String(d.getMonth() + 1).padStart(2, '0') + "-" +
        String(d.getDate()).padStart(2, '0');
    })();

    // 1. 根据当前筛选范围 (gwSelectedDay) 计算指标
    var total = 0, succ = 0, fail = 0;
    var totalTokens = 0, promptTokens = 0, compTokens = 0;
    var avgTtft = 0, avgDur = 0;
    var failover = s.failoverCount || 0;
    var absorbed = s.modelBusyAbsorbedCount || 0;
    var modelCounts = {};

    if (gwSelectedDay) {
      // 选中特定单日
      var curDay = null;
      for (var di = 0; di < daily.length; di++) {
        if (daily[di].date === gwSelectedDay) { curDay = daily[di]; break; }
      }
      if (curDay) {
        total = curDay.totalRequests || 0;
        succ = curDay.successCount || 0;
        fail = curDay.failCount || 0;
        totalTokens = curDay.totalTokens || 0;
        promptTokens = curDay.promptTokens || 0;
        compTokens = curDay.compTokens || 0;
        avgTtft = curDay.avgTtftMs || 0;
        avgDur = curDay.avgDurationMs || 0;
        modelCounts = curDay.modelCounts || {};
      }
    } else {
      // 7 日全貌汇总
      if (daily.length > 0) {
        var ttftSum = 0, ttftCount = 0;
        var durSum = 0;
        daily.forEach(function (d) {
          total += (d.totalRequests || 0);
          succ += (d.successCount || 0);
          fail += (d.failCount || 0);
          totalTokens += (d.totalTokens || 0);
          promptTokens += (d.promptTokens || 0);
          compTokens += (d.compTokens || 0);
          if (d.avgTtftMs > 0 && d.totalRequests > 0) {
            ttftSum += d.avgTtftMs * d.totalRequests;
            ttftCount += d.totalRequests;
          }
          if (d.avgDurationMs > 0 && d.totalRequests > 0) {
            durSum += d.avgDurationMs * d.totalRequests;
          }
          if (d.modelCounts) {
            for (var m in d.modelCounts) {
              modelCounts[m] = (modelCounts[m] || 0) + d.modelCounts[m];
            }
          }
        });
        if (ttftCount > 0) avgTtft = Math.round(ttftSum / ttftCount);
        if (total > 0) avgDur = Math.round(durSum / total);
      } else {
        total = s.totalRequests || 0;
        succ = s.successCount || 0;
        fail = s.failCount || 0;
        totalTokens = s.totalTokens || 0;
        promptTokens = s.totalPromptTokens || 0;
        compTokens = s.totalCompTokens || 0;
        avgTtft = s.avgTtftMs || 0;
        avgDur = s.avgDurationMs || 0;
        modelCounts = s.modelCounts || {};
      }
    }

    var rate = total > 0 ? ((succ / total) * 100).toFixed(1) + "%" : "100%";

    // 渲染 5 联 KPI
    var totalEl = document.getElementById("gw-kpi-total");
    if (totalEl) totalEl.innerHTML = total.toLocaleString() + ' <span class="kpi-unit">次</span>';
    var rateEl = document.getElementById("gw-kpi-rate");
    if (rateEl) {
      rateEl.textContent = rate;
      rateEl.className = "pill " + (fail === 0 ? "ok" : "warn");
    }
    var statusFoot = document.getElementById("gw-kpi-status-foot");
    if (statusFoot) {
      statusFoot.innerHTML = '<span>成功 <b>' + succ.toLocaleString() + '</b></span> · <span>失败 <b>' + fail.toLocaleString() + '</b></span>';
    }

    var tokensEl = document.getElementById("gw-kpi-tokens");
    if (tokensEl) tokensEl.innerHTML = totalTokens.toLocaleString() + ' <span class="kpi-unit">tok</span>';
    var tokensFoot = document.getElementById("gw-kpi-tokens-foot");
    if (tokensFoot) {
      tokensFoot.innerHTML = '<span>输入 <b>' + promptTokens.toLocaleString() + '</b></span> · <span>输出 <b>' + compTokens.toLocaleString() + '</b></span>';
    }

    var ttftEl = document.getElementById("gw-kpi-ttft");
    if (ttftEl) ttftEl.innerHTML = avgTtft.toLocaleString() + ' <span class="kpi-unit">ms</span>';
    var durFoot = document.getElementById("gw-kpi-duration-foot");
    if (durFoot) {
      durFoot.innerHTML = '<span>平均总用时 <b>' + avgDur.toLocaleString() + ' ms</b></span>';
    }

    var failoverEl = document.getElementById("gw-kpi-failover");
    if (failoverEl) failoverEl.innerHTML = failover.toLocaleString() + ' <span class="kpi-unit">次换号</span>';
    var failoverPill = document.getElementById("gw-kpi-failover-pill");
    if (failoverPill) {
      if (failover > 0) {
        failoverPill.textContent = "已激活容灾";
        failoverPill.className = "pill warn";
      } else {
        failoverPill.textContent = "全量健康";
        failoverPill.className = "pill ok";
      }
    }

    var absorbedEl = document.getElementById("gw-kpi-absorbed");
    if (absorbedEl) absorbedEl.innerHTML = absorbed.toLocaleString() + ' <span class="kpi-unit">次化解</span>';
    var absorbedPill = document.getElementById("gw-kpi-absorbed-pill");
    if (absorbedPill) {
      if (absorbed > 0) {
        absorbedPill.textContent = "已平滑吸收";
        absorbedPill.className = "pill ok";
        absorbedPill.style.background = "rgba(56,189,248,0.15)";
        absorbedPill.style.color = "#38bdf8";
        absorbedPill.style.borderColor = "rgba(56,189,248,0.4)";
      } else {
        absorbedPill.textContent = "微抖动自适应";
        absorbedPill.className = "pill ok";
        absorbedPill.style.background = "";
        absorbedPill.style.color = "";
        absorbedPill.style.borderColor = "";
      }
    }

    // 2. 渲染近 7 日用量趋势柱状图
    renderTrendBars(daily, todayStr);

    // 3. 模型调用分布条形排布
    renderModelDistribution(modelCounts, total);

    // 3. 实时流水明细渲染
    var tbody = document.getElementById("gw-audit-tbody");
    if (tbody) {
      clear(tbody);
      var records = s.recentRecords || [];
      if (records.length === 0) {
        tbody.appendChild(el("tr", {}, [
          el("td", {
            colspan: "9",
            style: "text-align:center;padding:36px 16px;color:var(--ink-3)"
          }, [
            el("div", { style: "display:inline-flex;align-items:center;justify-content:center;width:40px;height:40px;border-radius:20px;background:var(--g3);margin-bottom:10px;color:var(--ink-2)" }, [
              el("svg", { class: "ico", "aria-hidden": "true" }, [el("use", { href: "#i-scan" })])
            ]),
            el("div", { style: "font-size:13px;font-weight:600;color:var(--ink-2);margin-bottom:4px", text: "网关就绪 · 暂无请求流水" }),
            el("div", { style: "font-size:11.5px;color:var(--ink-3);line-height:1.6" }, [
              "在 Cursor、Cline、Continue 或任意客户端配置本机 ",
              el("code", { style: "font-family:var(--mono);color:var(--brand);background:var(--g2);padding:1px 5px;border-radius:3px", text: "http://127.0.0.1:<port>/v1" }),
              " 发起对话即可实时捕获全链路审计"
            ])
          ])
        ]));
        return;
      }

      records.forEach(function (r) {
        var tStr = "--";
        var fullTimeTitle = "";
        if (r.timestamp) {
          var d = new Date(r.timestamp);
          if (!isNaN(d.getTime())) {
            var hh = String(d.getHours()).padStart(2, '0');
            var mm = String(d.getMinutes()).padStart(2, '0');
            var ss = String(d.getSeconds()).padStart(2, '0');
            tStr = hh + ":" + mm + ":" + ss;
            fullTimeTitle = d.toLocaleString();
          }
        }

        var codePillClass = (r.statusCode >= 200 && r.statusCode < 300) ? "ok" : (r.statusCode >= 500 ? "danger" : "warn");
        var isFailover = r.failoverTriggered;
        var isBusyRetry = (r.modelBusyRetries || 0) > 0;
        var retryTip = isBusyRetry ? ("算力繁忙微抖动重试 " + r.modelBusyRetries + " 次" + (r.retryDelays && r.retryDelays.length ? " (" + r.retryDelays.join(" → ") + ")" : "")) : "";

        var ttftClass = "gw-latency-mid";
        if (r.ttftMs > 0) {
          if (r.ttftMs < 1000) ttftClass = "gw-latency-fast";
          else if (r.ttftMs > 3000) ttftClass = "gw-latency-slow";
        }

        var durClass = "gw-latency-mid";
        var durText = "--";
        if (r.durationMs > 0) {
          if (r.durationMs < 2000) durClass = "gw-latency-fast";
          else if (r.durationMs > 10000) durClass = "gw-latency-very-slow";
          else if (r.durationMs > 4000) durClass = "gw-latency-slow";

          if (r.durationMs >= 1000) {
            durText = r.durationMs.toLocaleString() + "ms (" + (r.durationMs / 1000).toFixed(1) + "s)";
          } else {
            durText = r.durationMs + "ms";
          }
        }

        var totalTok = r.totalTokens || (r.promptTokens + r.completionTokens);
        var totalTokStr = totalTok.toLocaleString();
        var tokTag = "";
        if (totalTok >= 1000000) {
          tokTag = " (" + (totalTok / 1000000).toFixed(2) + "M)";
        } else if (totalTok >= 10000) {
          tokTag = " (" + (totalTok / 1000).toFixed(1) + "k)";
        }

        var statusDescEl;
        var rawErr = (r.errorMsg || "").trim();
        var is429 = (r.statusCode === 429 || /frequency limit|rate limit|quota exceed|用量超限|频率限制/i.test(rawErr));

        if (is429) {
          // 429 频率超限：美化为结构化卡片
          var resetDisplay = "";
          var matchReset = rawErr.match(/reset\s+at\s+([^\,\.]+)/i);
          if (matchReset && matchReset[1]) {
            var rawTime = matchReset[1].trim();
            var dateM = rawTime.match(/(\d{4}-\d{2}-\d{2})/);
            var timeM = rawTime.match(/(\d{1,2}:\d{2}(?::\d{2})?)/);
            if (timeM) {
              var tStr = timeM[1];
              var d = new Date();
              var todayStr = d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, '0') + "-" + String(d.getDate()).padStart(2, '0');
              if (dateM && dateM[1] === todayStr) {
                resetDisplay = "今日 " + tStr;
              } else if (dateM) {
                resetDisplay = dateM[1].slice(5) + " " + tStr;
              } else {
                resetDisplay = tStr;
              }
            } else {
              resetDisplay = rawTime.replace(/UTC\+8/i, "").trim();
            }
          }

          statusDescEl = el("div", {
            class: "gw-limit-card",
            title: "【上游 429 报错详情 (点击复制)】\n" + rawErr,
            onclick: function (e) {
              e.stopPropagation();
              if (rawErr && navigator.clipboard) {
                navigator.clipboard.writeText(rawErr).then(function () {
                  toast("已复制 429 报错详情", "ok");
                });
              }
            }
          }, [
            el("div", { style: "display:flex;align-items:center;justify-content:space-between;gap:4px" }, [
              el("span", { style: "font-weight:650;color:#f87171;font-size:11px;display:inline-flex;align-items:center;gap:3px" }, [
                "🛑 429 频控",
                resetDisplay ? el("span", { style: "color:#fee2e2;font-weight:650", text: "(" + resetDisplay + " 重置)" }) : null
              ]),
              el("span", {
                style: "font-size:9.5px;padding:0 5px;border-radius:3px;background:rgba(255,255,255,0.08);color:var(--ink-2);flex:none",
                text: "复制"
              })
            ]),
            el("div", { style: "color:var(--ink-3);font-size:10px;line-height:1.3" }, [
              "💡 建议换免积分模型 Auto 或稍后重试"
            ])
          ]);
        } else if (rawErr) {
          // 普通异常卡片
          statusDescEl = el("div", {
            class: "gw-err-card",
            title: rawErr + "\n(点击复制完整错误)",
            onclick: function () {
              if (navigator.clipboard) {
                navigator.clipboard.writeText(rawErr);
                toast("已复制异常信息", "ok");
              }
            }
          }, [
            el("div", {
              style: "color:var(--bad);font-weight:550;font-size:11px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;word-break:break-word",
              text: rawErr
            }),
            isBusyRetry ? el("span", { style: "color:var(--ink-3);font-size:10px;display:block;margin-top:2px", text: "已防抖重试 " + r.modelBusyRetries + " 次" }) : null
          ]);
        } else if (isBusyRetry) {
          statusDescEl = el("span", { style: "color:#38bdf8;font-weight:550;font-size:11.5px;cursor:help", title: retryTip }, [
            "⚡ 算力繁忙已化解",
            el("span", { style: "color:var(--ink-3);font-size:10px;display:block", text: r.retryDelays && r.retryDelays.length ? ("退避: " + r.retryDelays.join(" → ")) : (r.modelBusyRetries + "次重试") })
          ]);
        } else if (isFailover) {
          statusDescEl = el("span", { style: "color:var(--warn);font-size:11.5px", text: "触发限流自动故障转移" });
        } else {
          statusDescEl = el("span", { style: "color:var(--ok);font-size:11.5px", text: "✓ 正常转发" });
        }

        var fmtTokSummary = totalTok >= 1000000
          ? ((totalTok / 1000000).toFixed(2) + "M")
          : (totalTok >= 10000 ? ((totalTok / 1000).toFixed(1) + "k") : totalTok.toLocaleString());
        var fmtInTok = (r.promptTokens >= 10000) ? ((r.promptTokens / 1000).toFixed(0) + "k") : (r.promptTokens || 0);
        var fmtOutTok = (r.completionTokens >= 10000) ? ((r.completionTokens / 1000).toFixed(0) + "k") : (r.completionTokens || 0);

        var tr = el("tr", { class: "gw-audit-row" }, [
          // 1. 时间
          el("td", {
            style: "padding:7px 8px;font-family:var(--mono);color:var(--ink-3);font-size:11px;white-space:nowrap",
            title: fullTimeTitle,
            text: tStr
          }),
          // 2. 模型 · 账号 · 模式（合并展示）
          el("td", { style: "padding:7px 8px;min-width:130px" }, [
            el("div", { style: "font-family:var(--mono);font-weight:600;color:var(--ink-1);font-size:11.5px;white-space:nowrap" }, [
              r.modelRequested || r.modelMapped || "unknown",
              (r.modelMapped && r.modelMapped !== r.modelRequested) ? el("span", { style: "color:var(--ink-3);font-size:10px;margin-left:4px;font-weight:400", text: "→ " + r.modelMapped }) : null
            ]),
            el("div", { style: "display:flex;align-items:center;gap:4px;margin-top:2px;font-size:10.5px;color:var(--ink-3);white-space:nowrap" }, [
              el("span", { class: "pill", style: "font-size:9.5px;padding:0 4px;border-color:var(--g4);color:var(--ink-2)", text: r.isStream ? "流式" : "非流式" }),
              el("span", { style: "max-width:110px;overflow:hidden;text-overflow:ellipsis", title: r.accountMask || r.accountId || "默认" }, [
                r.accountMask || r.accountId || "默认"
              ]),
              isFailover ? el("span", { class: "pill warn", style: "font-size:9.5px;padding:0 3px", text: "换号" }) : null,
              isBusyRetry ? el("span", { class: "pill ok", style: "font-size:9.5px;padding:0 3px;background:rgba(56,189,248,0.12);color:#38bdf8", title: retryTip, text: "防抖(" + r.modelBusyRetries + ")" }) : null
            ])
          ]),
          // 3. 状态码
          el("td", { style: "padding:7px 8px;text-align:center;width:55px;white-space:nowrap" }, [
            el("span", { class: "pill " + codePillClass, style: "font-size:11px", text: String(r.statusCode || 200) })
          ]),
          // 4. 总耗时 · 首字延迟（合并展示）
          el("td", { style: "padding:7px 8px;font-family:var(--mono);text-align:right;white-space:nowrap;width:80px" }, [
            el("div", { class: durClass, style: "font-size:11.5px;font-weight:600", text: durText }),
            el("div", { style: "font-size:10px;color:var(--ink-3)", text: r.ttftMs > 0 ? ("首字 " + r.ttftMs + "ms") : "首字 --" })
          ]),
          // 5. Tokens 吞吐
          el("td", { style: "padding:7px 8px;font-family:var(--mono);text-align:right;white-space:nowrap;width:95px" }, [
            el("div", { style: "color:var(--ink-1);font-weight:600;font-size:11.5px", title: "精确总消耗: " + totalTok.toLocaleString() + " tokens" }, [
              fmtTokSummary,
              el("span", { style: "color:var(--ink-3);font-size:9.5px;margin-left:2px;font-weight:400", text: "tok" })
            ]),
            el("div", { style: "color:var(--ink-3);font-size:10px" }, [
              "入 " + fmtInTok + " · 出 " + fmtOutTok
            ])
          ]),
          // 6. 说明 / 异常详情
          el("td", { style: "padding:7px 8px;font-size:11px;min-width:160px;word-break:break-word" }, [
            statusDescEl
          ])
        ]);
        tbody.appendChild(tr);
      });

    }
  }

  function renderTrendBars(daily, todayStr) {
    var container = document.getElementById("gw-trend-bars-container");
    if (!container) return;
    clear(container);

    if (!daily || daily.length === 0) {
      container.innerHTML = '<div style="grid-column:span 7;text-align:center;padding:24px 0;color:var(--ink-3);font-size:12px">暂无近 7 日用量历史归档</div>';
      return;
    }

    var maxTokens = 0;
    daily.forEach(function (d) {
      if (d.totalTokens > maxTokens) maxTokens = d.totalTokens;
    });
    if (maxTokens <= 0) maxTokens = 1000;

    // 作用域按钮状态更新
    var btnAll = document.getElementById("btn-trend-all");
    var btnToday = document.getElementById("btn-trend-today");
    if (btnAll && btnToday) {
      btnAll.classList.toggle("active", gwSelectedDay === null);
      btnToday.classList.toggle("active", gwSelectedDay === todayStr);
    }

    // 明细条提示
    var detailLeft = document.getElementById("gw-trend-detail-left");
    var detailRight = document.getElementById("gw-trend-detail-right");
    if (detailLeft && detailRight) {
      if (gwSelectedDay) {
        var cur = null;
        for (var di = 0; di < daily.length; di++) {
          if (daily[di].date === gwSelectedDay) { cur = daily[di]; break; }
        }
        var curToks = cur ? (cur.totalTokens || 0).toLocaleString() : "0";
        var curReqs = cur ? (cur.totalRequests || 0) : 0;
        var curTtft = cur ? (cur.avgTtftMs || 0) : 0;
        detailLeft.innerHTML = '已选中 <b>' + gwSelectedDay + (gwSelectedDay === todayStr ? ' (今日)' : '') + '</b>：总吞吐 <b>' + curToks + ' tok</b> · 请求 <b>' + curReqs + ' 次</b> · 平均 TTFT <b>' + curTtft + 'ms</b>';
        detailRight.innerHTML = '<a href="javascript:void(0)" id="link-reset-trend" style="color:var(--brand);text-decoration:none">恢复 7 日全貌 ↺</a>';
        var resetLink = document.getElementById("link-reset-trend");
        if (resetLink) resetLink.onclick = function (e) {
          e.preventDefault();
          gwSelectedDay = null;
          renderGatewayAudit(gwAuditData);
        };
      } else {
        detailLeft.textContent = "点击任意柱子可下钻筛选该日模型分布与消耗；支持跨天持久化累计，隔天打开零丢失";
        detailRight.textContent = "近 7 日总览";
      }
    }

    daily.forEach(function (d) {
      var isToday = (d.date === todayStr);
      var isSelected = (gwSelectedDay === d.date);
      var tokStr = fmtTokens(d.totalTokens);
      var col = el("div", {
        class: "trend-col" + (isSelected ? " active" : ""),
        title: d.date + "：总消耗 " + d.totalTokens.toLocaleString() + " tok (" + d.totalRequests + " 次请求)"
      });

      // 顶部数值与请求胶囊
      var topVal = el("div", { class: "trend-val", text: tokStr });
      col.appendChild(topVal);

      if (d.totalRequests > 0) {
        var reqPill = el("span", {
          style: "font-size:10px;padding:1px 5px;border-radius:999px;background:rgba(168,85,247,0.15);color:#c084fc;font-family:var(--mono);margin-bottom:4px;",
          text: d.totalRequests + "次"
        });
        col.appendChild(reqPill);
      } else {
        var placeholder = el("div", { style: "height:16px;margin-bottom:4px;" });
        col.appendChild(placeholder);
      }

      // 中间柱形轨道
      var track = el("div", { class: "trend-bar-track" });
      if (d.totalTokens > 0) {
        var heightPct = Math.max(8, Math.round((d.totalTokens / maxTokens) * 100));
        var inPct = ((d.promptTokens / d.totalTokens) * 100).toFixed(1) + "%";
        var outPct = ((d.compTokens / d.totalTokens) * 100).toFixed(1) + "%";

        var barFill = el("div", { class: "trend-bar-fill", style: "height:" + heightPct + "%;" }, [
          el("div", { class: "trend-bar-in", style: "height:" + inPct + ";", title: "输入: " + d.promptTokens.toLocaleString() + " tok" }),
          el("div", { class: "trend-bar-out", style: "height:" + outPct + ";", title: "输出: " + d.compTokens.toLocaleString() + " tok" })
        ]);
        track.appendChild(barFill);
      } else {
        var emptyLine = el("div", { style: "width:100%;height:3px;background:var(--g3);border-radius:2px;" });
        track.appendChild(emptyLine);
      }
      col.appendChild(track);

      // 底部日期
      var shortDate = d.date.slice(5);
      var dateLabel = el("div", {
        class: "trend-date",
        style: isToday ? "color:#38bdf8;font-weight:600;" : "",
        text: shortDate + (isToday ? " (今)" : "")
      });
      col.appendChild(dateLabel);

      col.onclick = function () {
        gwSelectedDay = (gwSelectedDay === d.date) ? null : d.date;
        renderGatewayAudit(gwAuditData);
      };

      container.appendChild(col);
    });
  }

  function renderModelDistribution(modelCounts, total) {
    var modelDistBody = document.getElementById("gw-model-dist-body");
    if (!modelDistBody) return;
    clear(modelDistBody);
    var models = Object.keys(modelCounts || {});
    var countLabel = document.getElementById("gw-model-count-label");
    if (countLabel) {
      countLabel.textContent = "共 " + models.length + " 个模型" + (gwSelectedDay ? " (" + gwSelectedDay + ")" : " (7 日合计)");
    }
    if (models.length === 0) {
      modelDistBody.appendChild(el("div", {
        style: "color:var(--ink-3);font-size:12px;text-align:center;padding:12px 0",
        text: gwSelectedDay ? "该日期无模型调用记录" : "暂无模型调用数据"
      }));
      return;
    }
    models.sort(function (a, b) { return modelCounts[b] - modelCounts[a]; });
    models.forEach(function (m) {
      var count = modelCounts[m];
      var pct = total > 0 ? ((count / total) * 100).toFixed(1) : "0.0";
      var row = el("div", { style: "display:flex;flex-direction:column;gap:3px;margin-bottom:4px" }, [
        el("div", { style: "display:flex;justify-content:space-between;align-items:center;font-size:12px" }, [
          el("span", { style: "font-family:var(--mono);color:var(--ink-1);font-weight:600", text: m }),
          el("span", { style: "font-size:11.5px;color:var(--ink-3)" }, [
            el("b", { style: "color:var(--ink-2)", text: count.toLocaleString() }),
            " 次 (" + pct + "%)"
          ])
        ]),
        el("div", { style: "height:6px;background:var(--g3);border-radius:3px;overflow:hidden" }, [
          el("div", {
            style: "height:100%;width:" + pct + "%;background:linear-gradient(90deg, #38bdf8, #818cf8);border-radius:3px;transition:width .2s ease"
          })
        ])
      ]);
      modelDistBody.appendChild(row);
    });
  }

  // ---------- 网关数据导出与文件下载辅助 ----------
  function triggerBlobDownload(blob, filename) {
    var url = URL.createObjectURL(blob);
    var a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    setTimeout(function () {
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    }, 200);
  }

  function getExportTimestamp() {
    var now = new Date();
    return now.getFullYear() +
      String(now.getMonth() + 1).padStart(2, "0") +
      String(now.getDate()).padStart(2, "0") + "-" +
      String(now.getHours()).padStart(2, "0") +
      String(now.getMinutes()).padStart(2, "0") +
      String(now.getSeconds()).padStart(2, "0");
  }

  // 1. 导出网关请求流水 CSV
  function exportGatewayCSV() {
    toast("正在导出网关调用流水 (CSV)...", "ok");
    fetch("/api/gateway/export?format=csv")
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.blob();
      })
      .then(function (blob) {
        triggerBlobDownload(blob, "wbmux-gateway-records-" + getExportTimestamp() + ".csv");
        toast("网关流水 CSV 导出成功", "ok");
      })
      .catch(function () {
        // 兜底离线前端生成
        var records = (gwAuditData && gwAuditData.recentRecords) || [];
        if (records.length === 0) {
          toast("当前暂无调用流水可导出", "bad");
          return;
        }
        var csvRows = [
          "\uFEFF请求ID,时间,请求模型,映射模型,调用账号,模式,状态码,TTFT(ms),总用时(ms),提示词Tokens,补全Tokens,总Tokens,故障转移,说明/异常详情"
        ];
        records.forEach(function (r) {
          var row = [
            '"' + (r.id || "") + '"',
            '"' + (r.timestamp ? new Date(r.timestamp).toLocaleString() : "") + '"',
            '"' + (r.modelRequested || "") + '"',
            '"' + (r.modelMapped || "") + '"',
            '"' + (r.accountMask || r.accountId || "默认") + '"',
            '"' + (r.isStream ? "流式" : "非流式") + '"',
            r.statusCode || 200,
            r.ttftMs || 0,
            r.durationMs || 0,
            r.promptTokens || 0,
            r.completionTokens || 0,
            r.totalTokens || 0,
            r.failoverTriggered ? "是" : "否",
            '"' + (r.errorMsg || "").replace(/"/g, '""') + '"'
          ];
          csvRows.push(row.join(","));
        });
        var blob = new Blob([csvRows.join("\r\n")], { type: "text/csv;charset=utf-8;" });
        triggerBlobDownload(blob, "wbmux-gateway-records-" + getExportTimestamp() + ".csv");
        toast("网关流水 CSV 导出成功 (本地离线备份)", "ok");
      });
  }

  // 2. 导出网关全量审计与统计归档 JSON
  function exportGatewayJSON() {
    toast("正在导出全量归档 (JSON)...", "ok");
    fetch("/api/gateway/export?format=json")
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.blob();
      })
      .then(function (blob) {
        triggerBlobDownload(blob, "wbmux-gateway-archive-" + getExportTimestamp() + ".json");
        toast("全量归档 JSON 导出成功", "ok");
      })
      .catch(function () {
        if (!gwAuditData) {
          toast("暂无数据可导出", "bad");
          return;
        }
        var blob = new Blob([JSON.stringify(gwAuditData, null, 2)], { type: "application/json;charset=utf-8;" });
        triggerBlobDownload(blob, "wbmux-gateway-archive-" + getExportTimestamp() + ".json");
        toast("全量归档 JSON 导出成功 (当前快照)", "ok");
      });
  }

  // 3. 导出近 7 日用量统计聚合 CSV
  function exportGatewayDailyCSV() {
    var daily = (gwAuditData && (gwAuditData.dailyStats || gwAuditData.daily)) || [];
    if (daily.length === 0) {
      toast("暂无近 7 日用量历史可导出", "bad");
      return;
    }
    var rows = [
      "\uFEFF日期,总请求数,成功数,失败数,输入Tokens,输出Tokens,总Tokens,平均TTFT(ms),平均总耗时(ms)"
    ];
    daily.forEach(function (d) {
      rows.push([
        d.date || "",
        d.totalRequests || 0,
        d.successCount || 0,
        d.failCount || 0,
        d.promptTokens || 0,
        d.compTokens || 0,
        d.totalTokens || 0,
        d.avgTtftMs || 0,
        d.avgDurationMs || 0
      ].join(","));
    });
    var blob = new Blob([rows.join("\r\n")], { type: "text/csv;charset=utf-8;" });
    triggerBlobDownload(blob, "wbmux-gateway-daily-summary-" + getExportTimestamp() + ".csv");
    toast("近 7 日用量统计 CSV 导出成功", "ok");
  }

  // 导出下拉菜单开关交互
  var btnGwExport = document.getElementById("btn-gw-export");
  var gwExportMenu = document.getElementById("gw-export-menu");
  if (btnGwExport && gwExportMenu) {
    btnGwExport.addEventListener("click", function (e) {
      e.stopPropagation();
      gwExportMenu.classList.toggle("show");
    });
    document.addEventListener("click", function (e) {
      if (!btnGwExport.contains(e.target) && !gwExportMenu.contains(e.target)) {
        gwExportMenu.classList.remove("show");
      }
    });
  }

  var btnExportCSV = document.getElementById("btn-gw-export-csv");
  if (btnExportCSV) btnExportCSV.addEventListener("click", function () {
    if (gwExportMenu) gwExportMenu.classList.remove("show");
    exportGatewayCSV();
  });

  var btnExportJSON = document.getElementById("btn-gw-export-json");
  if (btnExportJSON) btnExportJSON.addEventListener("click", function () {
    if (gwExportMenu) gwExportMenu.classList.remove("show");
    exportGatewayJSON();
  });

  var btnExportDaily = document.getElementById("btn-gw-export-daily");
  if (btnExportDaily) btnExportDaily.addEventListener("click", function () {
    if (gwExportMenu) gwExportMenu.classList.remove("show");
    exportGatewayDailyCSV();
  });

  function clearGatewayAudit() {
    var recCount = (gwAuditData && gwAuditData.recentRecords) ? gwAuditData.recentRecords.length : 0;
    var tip = "确定要清空全部网关调用统计与请求流水明细吗？\n";
    if (recCount > 0) {
      tip += "\n当前已有 " + recCount + " 条审计流水，清空后不可恢复。\n💡 如需留存，可先点击「导出数据」备份为 CSV 或 JSON。";
    }
    if (!confirm(tip)) return;
    api("/api/gateway/clear", { method: "POST" })
      .then(function () {
        toast("网关调用流水已清空", "ok");
        loadGatewayAudit(true);
      })
      .catch(function (err) {
        toast("清空失败：" + err.message, "bad");
      });
  }

  var btnGwRefresh = document.getElementById("btn-gw-refresh");
  if (btnGwRefresh) btnGwRefresh.addEventListener("click", function () { loadGatewayAudit(true); });
  var btnGwClear = document.getElementById("btn-gw-clear");
  if (btnGwClear) btnGwClear.addEventListener("click", clearGatewayAudit);
  var btnGwClearTable = document.getElementById("btn-gw-clear-table");
  if (btnGwClearTable) btnGwClearTable.addEventListener("click", clearGatewayAudit);


  var btnTrendAll = document.getElementById("btn-trend-all");
  if (btnTrendAll) {
    btnTrendAll.addEventListener("click", function () {
      gwSelectedDay = null;
      if (gwAuditData) renderGatewayAudit(gwAuditData);
    });
  }
  var btnTrendToday = document.getElementById("btn-trend-today");
  if (btnTrendToday) {
    btnTrendToday.addEventListener("click", function () {
      var d = new Date();
      gwSelectedDay = d.getFullYear() + "-" +
        String(d.getMonth() + 1).padStart(2, '0') + "-" +
        String(d.getDate()).padStart(2, '0');
      if (gwAuditData) renderGatewayAudit(gwAuditData);
    });
  }

  // 定期检查遥测守护状态并刷新徽章提示
  function updateTelemetryBadge() {
    api("/api/telemetry").then(function (st) {
      var badge = document.getElementById("telemetry-badge");
      if (!badge) return;
      if (st.running) {
        var hint = "后台自适应遥测守护中 (已上报 " + st.totalPings + " 次活跃心跳)";
        if (st.lastPingAt) {
          var dt = new Date(st.lastPingAt);
          hint += " · 最近心跳：" + dt.toLocaleTimeString();
        }
        if (st.deviceId) {
          hint += " · 设备指纹：" + st.deviceId.substring(0, 8) + "…";
        }
        badge.title = hint;
      }
    }).catch(function () {});
  }
  updateTelemetryBadge();
  setInterval(updateTelemetryBadge, 60000);

  function renderChatProxy(st) {
    var s = (st && st.settings) || {};
    var addr = s.chatProxyAddr || "";
    var desc = s.proxyDescribe || (addr ? addr : "纯直连");
    var sysProxy = s.detectedProxyAddr || "";

    var ipt = document.getElementById("ipt-chat-proxy-port");
    var badge = document.getElementById("chat-proxy-status-badge");
    var descEl = document.getElementById("chat-proxy-describe");
    var btnFillSys = document.getElementById("btn-fill-sys-proxy");

    if (ipt && !ipt.matches(":focus")) {
      ipt.value = addr;
    }
    if (badge) {
      if (addr) {
        badge.className = "growth-task-tag";
        badge.style.background = "rgba(245,158,11,0.18)";
        badge.style.color = "#fbbf24";
        badge.textContent = "⚡ 已配置转发代理 (" + addr + ")";
      } else {
        badge.className = "growth-task-tag";
        badge.style.background = "rgba(34,197,94,0.15)";
        badge.style.color = "#22c55e";
        badge.textContent = "⚡ 纯直连防断连模式 (推荐)";
      }
    }
    if (descEl) descEl.textContent = desc;

    if (btnFillSys) {
      if (sysProxy && sysProxy !== addr) {
        btnFillSys.style.display = "inline-flex";
        btnFillSys.textContent = "填入系统代理 (" + sysProxy + ")";
        btnFillSys.onclick = function () {
          saveChatProxy(sysProxy);
        };
      } else {
        btnFillSys.style.display = "none";
      }
    }
  }

  function saveChatProxy(addr) {
    api("/api/chat-proxy-toggle", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ addr: addr })
    }).then(function (res) {
      toast(addr ? ("已设置大模型代理出口: " + addr) : "已恢复大模型纯直连防断连模式", "ok");
      if (state && state.settings) {
        state.settings.chatProxyAddr = addr;
        state.settings.proxyChatRequests = !!addr;
        if (res && res.describe) state.settings.proxyDescribe = res.describe;
        if (res && res.detectedProxyAddr) state.settings.detectedProxyAddr = res.detectedProxyAddr;
      }
      renderChatProxy(state);
    }).catch(function (err) {
      toast("保存代理设置失败: " + err, "bad");
    });
  }

  var btnSaveChatProxy = document.getElementById("btn-save-chat-proxy");
  var btnResetChatProxy = document.getElementById("btn-reset-chat-proxy");
  var iptChatProxyPort = document.getElementById("ipt-chat-proxy-port");

  if (btnSaveChatProxy && iptChatProxyPort) {
    btnSaveChatProxy.addEventListener("click", function () {
      saveChatProxy(iptChatProxyPort.value.trim());
    });
    iptChatProxyPort.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        saveChatProxy(iptChatProxyPort.value.trim());
      }
    });
  }
  if (btnResetChatProxy) {
    btnResetChatProxy.addEventListener("click", function () {
      saveChatProxy("");
    });
  }

  // ---------- 全网实时联网搜索交互 ----------
  function loadWebSearch() {
    var origin = window.location.origin;
    var elMsg = document.getElementById("txt-search-endpoint-messages");
    var elNative = document.getElementById("txt-search-endpoint-native");
    var elGateway = document.getElementById("txt-search-endpoint-gateway");
    if (elMsg) elMsg.textContent = origin + "/v1/search/messages";
    if (elNative) elNative.textContent = origin + "/v1/search?q=关键词&n=5";
    if (elGateway) elGateway.textContent = origin + "/v1/messages";
    var ipt = document.getElementById("ipt-search-query");
    if (ipt && !ipt.value) setTimeout(function () { ipt.focus(); }, 100);
  }

  var btnRunSearch = document.getElementById("btn-run-search");
  var iptSearchQuery = document.getElementById("ipt-search-query");
  var selSearchLimit = document.getElementById("sel-search-limit");
  var searchResultsList = document.getElementById("search-results-list");
  var searchStatusBar = document.getElementById("search-status-bar");

  function bindSearchCopyBtn(btnId, targetId) {
    var b = document.getElementById(btnId);
    if (b) {
      b.addEventListener("click", function () {
        var el = document.getElementById(targetId);
        if (el) {
          navigator.clipboard.writeText(el.textContent.trim()).then(function () {
            toast("端点地址已复制到剪贴板", "ok");
          });
        }
      });
    }
  }
  bindSearchCopyBtn("btn-copy-search-messages", "txt-search-endpoint-messages");
  bindSearchCopyBtn("btn-copy-search-native", "txt-search-endpoint-native");
  bindSearchCopyBtn("btn-copy-search-gateway", "txt-search-endpoint-gateway");

  if (btnRunSearch && iptSearchQuery) {
    btnRunSearch.addEventListener("click", function () {
      var q = iptSearchQuery.value.trim();
      if (!q || q.length < 2) {
        toast("请输入至少 2 个字符的搜索词", "warn");
        return;
      }
      var limit = parseInt(selSearchLimit ? selSearchLimit.value : "5", 10) || 5;
      btnRunSearch.classList.add("loading");
      btnRunSearch.disabled = true;
      if (searchStatusBar) {
        searchStatusBar.style.display = "block";
        searchStatusBar.textContent = "正在调用官方上游检索并提炼全网内容…";
      }

      api("/api/search/debug", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ query: q, max_results: limit })
      }).then(function (res) {
        btnRunSearch.classList.remove("loading");
        btnRunSearch.disabled = false;
        if (!res || !res.ok) {
          if (searchStatusBar) searchStatusBar.textContent = "搜索未能返回有效结果。";
          return;
        }
        if (searchStatusBar) {
          searchStatusBar.textContent = "全网检索成功 · 耗时 " + (res.response_time_ms || 0) + "ms · 命中 " + (res.results ? res.results.length : 0) + " 条";
        }
        if (!searchResultsList) return;
        searchResultsList.innerHTML = "";
        if (!res.results || res.results.length === 0) {
          searchResultsList.innerHTML = '<div style="font-size:12px;color:var(--ink-3);padding:14px 0;text-align:center">未找到相关结果。</div>';
          return;
        }
        res.results.forEach(function (item, idx) {
          var card = el("div", {
            style: "background:rgba(0,0,0,0.22);border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:12px 14px"
          });
          var head = el("div", { style: "display:flex;align-items:center;justify-content:space-between;margin-bottom:6px" });
          var title = el("a", {
            href: item.url,
            target: "_blank",
            rel: "noopener noreferrer",
            style: "font-size:13px;font-weight:600;color:var(--brand);text-decoration:none",
            text: (idx + 1) + ". " + (item.title || item.url)
          });
          head.appendChild(title);
          card.appendChild(head);
          if (item.snippet) {
            var snip = el("p", {
              style: "font-size:12px;color:var(--ink-1);line-height:1.6;margin:0 0 6px 0",
              text: item.snippet
            });
            card.appendChild(snip);
          }
          var link = el("div", {
            style: "font-size:11px;color:var(--ink-3);word-break:break-all",
            text: item.url
          });
          card.appendChild(link);
          searchResultsList.appendChild(card);
        });
      }).catch(function (err) {
        btnRunSearch.classList.remove("loading");
        btnRunSearch.disabled = false;
        if (searchStatusBar) searchStatusBar.textContent = "搜索失败: " + err;
        toast("联网搜索失败: " + err, "bad");
      });
    });

    iptSearchQuery.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        btnRunSearch.click();
      }
    });
  }

  // SWR 立即首屏呈现：若已存在本地快照，首帧直接上屏渲染，彻底消除白屏与等待感
  if (usageData) {
    try {
      renderUsage();
    } catch (e) {}
  }

  // 兜底启动：若 500ms 内 state 仍未完成加载，自动补充拉取一次
  setTimeout(function () {
    if (!usageData) loadUsage(true, false);
  }, 500);