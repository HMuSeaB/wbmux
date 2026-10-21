package webui

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/modelradar"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// chatPipeline 封装一次聊天转发请求的生命周期与重试管道。
// 将鉴权刷新、14003 算力防抖、429 故障转移和自适应瘦身等各分支解耦为独立的策略方法。
type chatPipeline struct {
	s       *Server
	w       http.ResponseWriter
	req     *http.Request
	rawBody []byte
	route   upstreamRoute
	up      upstreamRequest
	started time.Time

	accID   string
	accName string

	modelBusyRetries int
	modelBusyDelays  []string
}

func newChatPipeline(s *Server, w http.ResponseWriter, req *http.Request, rawBody []byte, route upstreamRoute, up upstreamRequest, started time.Time) *chatPipeline {
	accID := req.Header.Get("X-User-Id")
	accName := route.Label
	if route.Provider == nil {
		accName = "国际账号"
		if accs, err := credential.ListAccounts(s.probe(), variant.Intl); err == nil {
			for _, a := range accs {
				if a.UID == accID || (accID == "" && a.Active) {
					if a.Nickname != "" {
						accName = a.Nickname
					} else if a.UID != "" {
						accName = a.UID
					}
					break
				}
			}
		}
	}
	return &chatPipeline{
		s:       s,
		w:       w,
		req:     req,
		rawBody: rawBody,
		route:   route,
		up:      up,
		started: started,
		accID:   accID,
		accName: accName,
	}
}

func (p *chatPipeline) recordAudit(statusCode int, m chatMetrics, failover bool, errMsg string) {
	pTok := m.promptTokens
	cTok := m.completionTokens
	if pTok == 0 && len(p.rawBody) > 0 {
		pTok = len(p.rawBody) / 3
		if pTok < 1 {
			pTok = 1
		}
	}
	if cTok == 0 && m.bytesSent > 0 {
		cTok = int(m.bytesSent) / 3
		if cTok < 1 {
			cTok = 1
		}
	}
	ttft := m.ttftMs
	if ttft <= 0 && statusCode == http.StatusOK {
		ttft = time.Since(p.started).Milliseconds()
	}
	scale := evaluateContextScale(int64(len(p.rawBody)))
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ID:                fmt.Sprintf("req-%d", time.Now().UnixNano()),
		Timestamp:         p.started,
		ModelRequested:    p.up.Model,
		ModelMapped:       p.route.Model,
		AccountID:         p.accID,
		AccountMask:       p.accName,
		IsStream:          p.up.ClientStream,
		StatusCode:        statusCode,
		PromptTokens:      pTok,
		CompletionTokens:  cTok,
		TotalTokens:       pTok + cTok,
		TTFTMs:            ttft,
		DurationMs:        time.Since(p.started).Milliseconds(),
		FailoverTriggered: failover,
		ErrorMsg:          errMsg,
		ContextScale:      scale.Level,
		BodyBytes:         int64(len(p.rawBody)),
		ModelBusyRetries:  p.modelBusyRetries,
		RetryDelays:       p.modelBusyDelays,
	})
}

func (p *chatPipeline) deliverSuccess(respBody io.ReadCloser, isFailover bool, customPrefix string) {
	defer respBody.Close()
	p.s.observe(p.route.Model, http.StatusOK, "")
	var m chatMetrics
	if p.up.ClientStream {
		m = p.s.streamChatThrough(p.w, respBody, p.started)
	} else {
		m = p.s.aggregateChat(p.w, respBody, p.started)
	}

	// 流量被动测速：以本次真实流量的实际首字延迟 TTFT 更新雷达快照
	ttft := m.ttftMs
	if ttft <= 0 {
		ttft = time.Since(p.started).Milliseconds()
	}
	radar := modelradar.Default()
	rec, _ := radar.GetRecord(p.route.Model)
	rec.Model = p.route.Model
	rec.StatusCode = http.StatusOK
	rec.TTFTMs = ttft
	rec.ErrorMsg = ""
	rec.IsRateLimited = false
	if ttft < 800 {
		rec.Status = modelradar.StatusFast
	} else if ttft <= 2500 {
		rec.Status = modelradar.StatusNormal
	} else {
		rec.Status = modelradar.StatusSlow
	}
	rec.CheckedAt = time.Now()
	radar.SetRecord(rec)

	p.recordAudit(http.StatusOK, m, isFailover, "")
	p.s.note(ProxyLogEntry{
		Level:  "ok",
		Model:  p.route.Model,
		Tools:  p.up.Tools,
		Stream: p.up.ClientStream,
		MS:     time.Since(p.started).Milliseconds(),
		Text: fmt.Sprintf("%s model=%s 经%s 回给客户端 %s 用时 %s",
			customPrefix, p.route.Model, p.route.Label,
			describeBytes(m.bytesSent), time.Since(p.started).Round(time.Millisecond)),
	})
}

func (p *chatPipeline) retryWithBody(customBody []byte, token, uid string) (*http.Response, error) {
	reqRetry, errReq := http.NewRequestWithContext(p.req.Context(), "POST", p.route.Endpoint, bytes.NewReader(customBody))
	if errReq != nil {
		return nil, errReq
	}
	for k, v := range p.req.Header {
		reqRetry.Header[k] = v
	}
	reqRetry.Header.Del("Content-Length")
	reqRetry.ContentLength = int64(len(customBody))
	if token != "" {
		reqRetry.Header.Set("Authorization", "Bearer "+token)
	}
	if uid != "" {
		reqRetry.Header.Set("X-User-Id", uid)
	}
	return probeHTTPClient().Do(reqRetry)
}

func (p *chatPipeline) retryOnce(token, uid string) (*http.Response, error) {
	return p.retryWithBody(p.rawBody, token, uid)
}

// tryAuthRefresh 尝试在 401 Unauthorized 时使用 RefreshToken 无感刷新并重试。
func (p *chatPipeline) tryAuthRefresh(resp *http.Response) bool {
	if resp.StatusCode != http.StatusUnauthorized || p.route.Provider != nil {
		return false
	}
	newAcc, errRefresh := credential.RefreshAccount(p.req.Context(), probeHTTPClient(), p.s.probe(), variant.Intl, "active")
	if errRefresh != nil || newAcc == nil || newAcc.Token == "" {
		return false
	}

	p.s.note(ProxyLogEntry{
		Level: "warn",
		Model: p.route.Model,
		Text:  fmt.Sprintf("上游 401 令牌过期，已自动通过 RefreshToken 无感保鲜并重试请求 (UID: %s)", newAcc.UID),
	})
	usage.InvalidateLiveCache(variant.Intl)
	respRetry, errRetry := p.retryOnce(newAcc.Token, newAcc.UID)
	if errRetry == nil && respRetry.StatusCode == http.StatusOK {
		p.deliverSuccess(respRetry.Body, false, "重试转发完成")
		return true
	}
	if errRetry == nil {
		_ = respRetry.Body.Close()
	}
	return false
}

// tryModelBusyRetry 在上游返回 14003 Model Busy 时执行渐进式短退避重试。
func (p *chatPipeline) tryModelBusyRetry(initialResp *http.Response, initialBody []byte) (handled bool, lastResp *http.Response, lastBody []byte, lastMsg string, isModelBusy bool) {
	lastResp = initialResp
	lastBody = initialBody
	lastMsg = upstreamMessage(lastBody)
	isModelBusy = strings.Contains(string(lastBody), "14003") || strings.Contains(strings.ToLower(string(lastBody)), "model busy")

	if !isModelBusy || initialResp.StatusCode != http.StatusTooManyRequests {
		if isModelBusy {
			lastMsg = "[官方该模型当前算力繁忙 Model Busy，非账号额度问题，建议切换其它模型重试] " + lastMsg
		}
		return false, lastResp, lastBody, lastMsg, isModelBusy
	}

	lastMsg = "[官方该模型当前算力繁忙 Model Busy，非账号额度问题，建议切换其它模型重试] " + lastMsg
	scale := evaluateContextScale(int64(len(p.rawBody)))

	for attempt := 0; attempt < len(modelBusyRetryDelays); attempt++ {
		delay := calcModelBusyBackoff(attempt, lastResp)
		p.modelBusyRetries++
		p.modelBusyDelays = append(p.modelBusyDelays, fmt.Sprintf("%v", delay.Round(10*time.Millisecond)))
		tipSuffix := ""
		if scale.Tip != "" {
			tipSuffix = " (建议: " + scale.Tip + ")"
		}
		p.s.note(ProxyLogEntry{
			Level: "warn",
			Model: p.route.Model,
			Text:  fmt.Sprintf("上游模型当前算力繁忙 (14003 Model Busy)，代理层自动防抖等待 %v 进行第 %d 次重试%s", delay.Round(time.Millisecond), attempt+1, tipSuffix),
		})

		select {
		case <-p.req.Context().Done():
			return true, lastResp, lastBody, lastMsg, isModelBusy
		case <-time.After(delay):
		}

		respRetry, errRetry := p.retryOnce("", "")
		if errRetry != nil {
			break
		}
		if respRetry.StatusCode == http.StatusOK {
			p.deliverSuccess(respRetry.Body, false, "算力繁忙重试成功")
			return true, respRetry, nil, "", false
		}

		bodyRetry, _ := io.ReadAll(io.LimitReader(respRetry.Body, 8<<10))
		_ = respRetry.Body.Close()
		lastBody = bodyRetry
		lastMsg = upstreamMessage(lastBody)
		isModelBusy = strings.Contains(string(lastBody), "14003") || strings.Contains(strings.ToLower(string(lastBody)), "model busy")
		if isModelBusy {
			lastMsg = "[官方该模型当前算力繁忙 Model Busy，非账号额度问题，建议切换其它模型重试] " + lastMsg
		} else {
			lastResp = respRetry
			break
		}
	}

	return false, lastResp, lastBody, lastMsg, isModelBusy
}

// tryFailover 在遭遇 429/402 且非算力繁忙时触发多账号故障转移（Failover）。
func (p *chatPipeline) tryFailover(resp *http.Response, isModelBusy bool) bool {
	if isModelBusy || (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusPaymentRequired) || p.route.Provider != nil {
		return false
	}

	accs, _ := credential.ListAccounts(p.s.probe(), variant.Intl)
	var currentActiveID string
	for _, a := range accs {
		if a.Active {
			currentActiveID = a.ID
			break
		}
	}
	if currentActiveID == "" || len(accs) <= 1 {
		return false
	}

	currentFailedID := currentActiveID
	for attempt := 0; attempt < len(accs)-1 && attempt < 3; attempt++ {
		nextAcc, errFailover := credential.GetDefaultFailoverTracker().FailoverToNext(p.s.probe(), variant.Intl, currentFailedID, credential.DefaultCooldownDuration)
		if errFailover != nil || nextAcc == nil || nextAcc.Token == "" {
			break
		}
		p.s.note(ProxyLogEntry{
			Level: "warn",
			Model: p.route.Model,
			Text:  fmt.Sprintf("上游触发 429 账号限流/配额用尽，自动故障转移至备用账号：%s (%s) 并重发 (第 %d 次重试)", nextAcc.Nickname, nextAcc.UID, attempt+1),
		})
		usage.InvalidateLiveCache(variant.Intl)
		respRetry, errRetry := p.retryOnce(nextAcc.Token, nextAcc.UID)
		if errRetry != nil {
			break
		}
		if respRetry.StatusCode == http.StatusOK {
			p.accName = nextAcc.Nickname
			p.accID = nextAcc.UID
			p.deliverSuccess(respRetry.Body, true, fmt.Sprintf("故障转移重发成功 (账号: %s)", nextAcc.Nickname))
			return true
		}
		if respRetry.StatusCode == http.StatusTooManyRequests || respRetry.StatusCode == http.StatusPaymentRequired {
			_ = respRetry.Body.Close()
			currentFailedID = nextAcc.ID
			continue
		}
		_ = respRetry.Body.Close()
		break
	}
	return false
}

// tryAdaptiveSlimming 在上游算力拒载且正文超大时触发自适应兜底瘦身重发。
func (p *chatPipeline) tryAdaptiveSlimming(resp *http.Response, isModelBusy bool) bool {
	if (!isModelBusy && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500) || int64(len(p.rawBody)) < DefaultSlimmingThreshold {
		return false
	}

	slimBody, slimmed, bLen, aLen := slimLargeChatBody(p.rawBody, DefaultSlimmingThreshold)
	if !slimmed {
		return false
	}

	ratio := (1.0 - float64(aLen)/float64(bLen)) * 100
	p.s.note(ProxyLogEntry{
		Level: "warn",
		Model: p.route.Model,
		Text: fmt.Sprintf("⚡ 上游算力拒载，网关自适应智能瘦身就地救援：%s → %s (瘦身率 %.1f%%)，正在快速重发...",
			describeBytes(bLen), describeBytes(aLen), ratio),
	})
	respSlim, errSlim := p.retryWithBody(slimBody, "", "")
	if errSlim == nil {
		if respSlim.StatusCode == http.StatusOK {
			p.deliverSuccess(respSlim.Body, true, "⚡ 自适应瘦身保底成功")
			return true
		}
		_ = respSlim.Body.Close()
	}
	return false
}

// execute 启动完整的转发与多层兜底重试流程。
func (p *chatPipeline) execute() {
	resp, err := probeHTTPClient().Do(p.req)
	if err != nil {
		p.s.note(ProxyLogEntry{
			Level: "bad", Model: p.route.Model, Tools: p.up.Tools,
			Text: fmt.Sprintf("转发失败 model=%s 经%s：%s", p.route.Model, p.route.Label, compactError(err.Error())),
		})
		p.recordAudit(http.StatusBadGateway, chatMetrics{}, false, err.Error())
		writeOpenAIError(p.w, http.StatusBadGateway, "上游请求失败："+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		p.deliverSuccess(resp.Body, false, "转发完成")
		return
	}

	// 1. 401 令牌过期：尝试使用 RefreshToken 无感刷新并重发
	if p.tryAuthRefresh(resp) {
		return
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	// 2. 14003 算力繁忙：秒级短退避重试（带真随机微抖动与自适应 Retry-After）
	handled, resp, body, msg, isModelBusy := p.tryModelBusyRetry(resp, body)
	if handled {
		return
	}

	// 3. 429/402 账号限额：多账号故障转移（Failover）轮换
	if p.tryFailover(resp, isModelBusy) {
		return
	}

	// 4. 网关大上下文自适应智能兜底瘦身 (Adaptive Context Slimming)
	if p.tryAdaptiveSlimming(resp, isModelBusy) {
		return
	}

	p.s.observe(p.route.Model, resp.StatusCode, msg)
	level, verdict := "bad", "拒绝"
	if resp.StatusCode >= 500 {
		level, verdict = "warn", "上游故障"
	}
	p.s.note(ProxyLogEntry{
		Level: level, Model: p.route.Model, Tools: p.up.Tools,
		MS: time.Since(p.started).Milliseconds(),
		Text: fmt.Sprintf("被%s%s model=%s 上游 HTTP %d: %s",
			p.route.Label, verdict, p.route.Model, resp.StatusCode, cutStr(compactError(msg), 300)),
	})
	p.recordAudit(resp.StatusCode, chatMetrics{}, false, msg)
	writeOpenAIError(p.w, resp.StatusCode, fmt.Sprintf("上游 HTTP %d: %s", resp.StatusCode, msg))
}
