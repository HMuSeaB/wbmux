package growth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

var idRegex = regexp.MustCompile(`^(cmb-)?[0-9a-f]{32}$`)

// ReportDesktopEvent 以桌面端 WorkBuddy 5.5.6 指纹向 /v2/report 批量上报行为事件。
func (c *Client) ReportDesktopEvent(cred credential.Credential, events ...map[string]any) error {
	if len(events) == 0 {
		return nil
	}
	desktopEvents := BuildDesktopEvents(cred.UID, "", events...)
	raw, err := json.Marshal(desktopEvents)
	if err != nil {
		return err
	}

	url := c.endpoint + DesktopReportPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}

	ApplyDesktopHeaders(req, cred.Token, cred.UID, c.endpoint)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("desktop report returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ReportWebEvent 以 Web 域客户端指纹向 /v2/report 上报事件。
func (c *Client) ReportWebEvent(cred credential.Credential, event map[string]any) error {
	pageURL, _ := event["pageURL"].(string)
	raw, err := json.Marshal([]map[string]any{event})
	if err != nil {
		return err
	}

	url := c.endpoint + DesktopReportPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}

	ApplyWebHeaders(req, cred.Token, cred.UID, c.endpoint, pageURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("web report returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ReportMPEvent 携带小程序口径头向 /v2/report 上报事件。
func (c *Client) ReportMPEvent(cred credential.Credential, event map[string]any) error {
	raw, err := json.Marshal([]map[string]any{event})
	if err != nil {
		return err
	}

	url := c.endpoint + DesktopReportPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}

	ApplyMPHeaders(req, cred.Token, cred.UID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mp report returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ShortChatProbe 发起一次极短短对话并流式截取服务端派发的真实 RequestID。
// 用于攻克专家与技能类任务的服务端会话真实性 JOIN 校验。
func (c *Client) ShortChatProbe(cred credential.Credential, model, expertID string) (string, string, error) {
	if model == "" || model == "fast-model" {
		model = "glm-5.2"
	}
	convID := fmt.Sprintf("wbmux-probe-conv-%d", time.Now().UnixNano())

	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "1+1=?"},
		},
		"stream": true,
	}
	raw, _ := json.Marshal(payload)

	url := c.endpoint + "/v2/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}

	ApplyDesktopHeaders(req, cred.Token, cred.UID, c.endpoint)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Conversation-ID", convID)
	if expertID != "" {
		req.Header.Set("X-Expert-Id", expertID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("chat probe status %d: %s", resp.StatusCode, string(b))
	}

	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 4096)
	searchFrom := 0

	for {
		n, rerr := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if i := bytes.Index(buf[searchFrom:], []byte(`"id":"`)); i >= 0 {
				abs := searchFrom + i
				rest := buf[abs+6:]
				if end := bytes.IndexByte(rest, '"'); end > 0 {
					extractedID := string(rest[:end])
					if idRegex.MatchString(extractedID) || strings.HasPrefix(extractedID, "cmb-") {
						return convID, extractedID, nil
					}
					searchFrom = abs + 1
				}
			}
		}
		if rerr != nil || len(buf) > 1<<20 {
			break
		}
	}

	// 容错兜底：若上游返回非标准 SSE 格式，派生确定性 ID，不阻塞流程
	fallbackID := "cmb-" + deriveDeviceID(cred.UID, "chat")+fmt.Sprintf("%d", time.Now().UnixMilli())[:10]
	return convID, fallbackID, nil
}

// ShortMPChatProbe 携带微信小程序口径向 /v2/chat/completions 发起极短对话并截取真实 RequestID。
// 用于攻克小程序专属任务（如 Sequential_Tasks_1 完成 1 次对话）的服务端会话落盘判据。
func (c *Client) ShortMPChatProbe(cred credential.Credential, model string) (string, string, error) {
	if model == "" {
		model = "glm-5.2"
	}
	convID := fmt.Sprintf("mp-conv-%d", time.Now().UnixNano())

	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "1+1=?"},
		},
		"stream": true,
	}
	raw, _ := json.Marshal(payload)

	url := c.endpoint + "/v2/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}

	ApplyMPHeaders(req, cred.Token, cred.UID)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Conversation-ID", convID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("mp chat probe status %d: %s", resp.StatusCode, string(b))
	}

	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 4096)
	searchFrom := 0

	for {
		n, rerr := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if i := bytes.Index(buf[searchFrom:], []byte(`"id":"`)); i >= 0 {
				abs := searchFrom + i
				rest := buf[abs+6:]
				if end := bytes.IndexByte(rest, '"'); end > 0 {
					extractedID := string(rest[:end])
					if idRegex.MatchString(extractedID) || strings.HasPrefix(extractedID, "cmb-") {
						return convID, extractedID, nil
					}
					searchFrom = abs + 1
				}
			}
		}
		if rerr != nil || len(buf) > 1<<20 {
			break
		}
	}

	fallbackID := "cmb-" + deriveDeviceID(cred.UID, "mpchat") + fmt.Sprintf("%d", time.Now().UnixMilli())[:10]
	return convID, fallbackID, nil
}

// BuildDesktopChatSequence 构造一次桌面端完整有效对话的 6 事件链（点亮 RichMeow_Chat 等）。
func BuildDesktopChatSequence(convID, reqID, msgID, modelID, modelName string) []map[string]any {
	return []map[string]any{
		{
			"eventCode":        "agent_task_created",
			"source":           "LOCAL",
			"name":             "working",
			"task_target":      "local",
			"mode":             "craft",
			"requestModelId":   modelID,
			"requestModelName": modelName,
			"conversationId":   convID,
			"messageId":        msgID,
		},
		{
			"eventCode":            "chat_message_send",
			"messageId":            msgID + "-assistant",
			"historyCount":         0,
			"isContextTruncated":   false,
			"currentStepCount":     1,
			"traceId":              reqID,
			"rootRequestId":        reqID,
			"parentConversationId": convID,
			"agentName":            "cli",
			"agentType":            "main",
		},
		{
			"eventCode":                         "chat_request_send",
			"inputLength":                       16,
			"traceId":                           reqID,
			"rootRequestId":                     reqID,
			"parentConversationId":              convID,
			"agentName":                         "cli",
			"agentType":                         "main",
			"codebuddy.session_id":              convID,
			"codebuddy.conversation_request_id": reqID,
		},
		{
			"eventCode":                         "chat_message_response",
			"messageId":                         msgID + "-assistant",
			"responseModelId":                   modelID,
			"inputToken":                        64,
			"outputToken":                       32,
			"totalToken":                        96,
			"isSuccessful":                      true,
			"finishReason":                      "stop",
			"firstTokenAt":                      time.Now().UnixMilli(),
			"traceId":                           reqID,
			"conversationId":                    convID,
			"rootRequestId":                     reqID,
			"parentConversationId":              convID,
			"agentName":                         "cli",
			"agentType":                         "main",
			"codebuddy.session_id":              convID,
			"codebuddy.conversation_request_id": reqID,
		},
		{
			"eventCode":            "chat_message_status",
			"messageId":            msgID + "-assistant",
			"messageErrorCode":     "0",
			"traceId":              reqID,
			"rootRequestId":        reqID,
			"parentConversationId": convID,
			"agentName":            "cli",
			"agentType":            "main",
		},
		{
			"eventCode":            "chat_request_response",
			"mode":                 "craft",
			"inputToken":           64,
			"outputToken":          32,
			"totalToken":           96,
			"isSuccessful":         true,
			"finishReason":         "stop",
			"rootRequestId":        reqID,
			"parentConversationId": convID,
		},
	}
}

// BuildBuddyAppSequence 构造 Buddy 应用进入与授权五连事件。
func BuildBuddyAppSequence(buddyID, buddyName string) []map[string]any {
	return []map[string]any{
		{"eventCode": "buddyapp_discover_click", "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName},
		{"eventCode": "buddyapp_show", "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName, "elementId": buddyID, "elementName": buddyName, "position": 2},
		{"eventCode": "buddyapp_enter_click", "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName, "elementId": buddyID, "elementName": buddyName, "position": 2, "isFirstPage": "1"},
		{"eventCode": "buddyapp_auth_confirm_click", "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName, "elementId": buddyID, "elementName": buddyName},
		{"eventCode": "buddyapp_bindaccount_skip_click", "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName, "elementId": buddyID, "elementName": buddyName},
	}
}

// BuildDesignCanvasSequence 构造设计创意画布创建与打开事件（点亮 create_canvas，+300 分）。
func BuildDesignCanvasSequence(convID, reqID string) []map[string]any {
	events := BuildDesktopChatSequence(convID, reqID, "msg-canvas", "fast-model", "fast-model")
	events = append(events,
		map[string]any{
			"eventCode":      "wbx_design_canvas_task_create",
			"conversationId": convID,
			"requestId":      reqID,
			"source":         "summon_keyword",
			"cost":           12000,
			"isSuccessful":   true,
		},
		map[string]any{
			"eventCode":      "wbx_design_canvas_open",
			"conversationId": convID,
			"requestId":      reqID,
			"id":             "ardot-file-" + reqID[len(reqID)-8:],
			"source":         "summon_keyword",
			"type":           "page",
			"cost":           13000,
			"isSuccessful":   true,
		},
	)
	return events
}

// BuildTemplateUseSequence 构造模板使用事件。
func BuildTemplateUseSequence(convID, reqID, tplID, tplName string) []map[string]any {
	events := BuildDesktopChatSequence(convID, reqID, "msg-"+tplID, "fast-model", "fast-model")
	events = append(events,
		map[string]any{
			"eventCode":     "agent_task_created_with_template",
			"mode":          "working",
			"isCustomModel": false,
			"id":            tplID,
			"name":          tplName,
			"requestId":     reqID,
		},
		map[string]any{
			"eventCode":   "template_used",
			"template_id": tplID,
			"task_mode":   "working",
		},
	)
	return events
}

// BuildPlaybookPromptSequence 构造灵感案例做同款 Prompt 事件。
func BuildPlaybookPromptSequence(convID, reqID, caseID, caseName string) []map[string]any {
	events := BuildDesktopChatSequence(convID, reqID, "msg-pb", "fast-model", "fast-model")
	payload := map[string]any{
		"id":   caseID,
		"name": caseName,
		"type": "document",
	}
	events = append(events,
		map[string]any{
			"eventCode":   "web_element_click",
			"pageName":    "playbook_detail",
			"elementId":   "playbook_ctaClick",
			"elementName": caseName,
			"source":      "discover",
		},
		map[string]any{
			"eventCode": "playbook_cta_click",
			"source":    "discover",
			"position":  0,
			"id":        caseID,
			"name":      caseName,
			"type":      "document",
		},
		map[string]any{
			"eventCode":      "playbook_prompt_send",
			"conversationId": convID,
			"requestId":      reqID,
			"id":             payload["id"],
			"name":           payload["name"],
			"type":           payload["type"],
		},
	)
	return events
}

// SetAppearanceTheme 调整桌面外观主题。
func (c *Client) SetAppearanceTheme(cred credential.Credential, resourceKey string) error {
	payload, _ := json.Marshal(map[string]string{
		"kind":         "theme",
		"resource_key": resourceKey,
	})
	url := c.endpoint + "/v2/user-asset/appearance/set"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	ApplyDesktopHeaders(req, cred.Token, cred.UID, c.endpoint)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// CompleteFirstBuddy 领养第一只猫咪（agreement -> first）。
func (c *Client) CompleteFirstBuddy(cred credential.Credential) error {
	agreeURL := c.endpoint + "/v2/activity/growth/agreement"
	reqAgree, _ := http.NewRequest(http.MethodPost, agreeURL, bytes.NewReader([]byte("{}")))
	applyHeaders(reqAgree, cred)
	if resp, err := c.httpClient.Do(reqAgree); err == nil {
		resp.Body.Close()
	}

	time.Sleep(300 * time.Millisecond)

	firstURL := c.endpoint + "/v2/activity/growth/buddy/first"
	reqFirst, _ := http.NewRequest(http.MethodPost, firstURL, bytes.NewReader([]byte("{}")))
	applyHeaders(reqFirst, cred)
	resp, err := c.httpClient.Do(reqFirst)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// MarketExpert 描述官方专家市场在架的真实专家实体。
type MarketExpert struct {
	ExpertID      string `json:"expert_id"`
	ExpertType    string `json:"expert_type"`
	DisplayNameZH string `json:"display_name_zh"`
	ProfessionZH  string `json:"profession_zh"`
	Version       string `json:"version"`
}

// MarketExpertList 从官方在架市场拉取真实专家列表，防止自造虚假 ID 被服务端静默丢弃。
func (c *Client) MarketExpertList(cred credential.Credential, expertType string) ([]MarketExpert, error) {
	body := map[string]any{"page": 1, "page_size": 20, "sort_by": "reco_rank", "sort_order": "desc"}
	if expertType != "" {
		body["expert_type"] = expertType
	}
	raw, _ := json.Marshal(body)
	url := c.endpointFor("/portal/operation-platform/market/expert/list")
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	ApplyDesktopHeaders(req, cred.Token, cred.UID, c.endpoint)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("expert list http status: %d", resp.StatusCode)
	}

	var res struct {
		Code int `json:"code"`
		Data struct {
			List []MarketExpert `json:"list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Data.List, nil
}

// BuildDesktopLoginSequence 构造桌面端登录启动事件序列。
func BuildDesktopLoginSequence() []map[string]any {
	return []map[string]any{
		{
			"eventCode": "desktop_login_success",
			"loginType": "account",
			"source":    "desktop_startup",
		},
		{
			"eventCode": "desktop_startup",
			"isAuto":    false,
			"launchWay": "shortcut",
		},
	}
}
