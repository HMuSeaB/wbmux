package inject

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client 是一个连到某个页面 target 的 CDP 会话。
type Client struct {
	ws   *wsConn
	next int
}

// pageTarget 是 /json/list 里的一个条目（只取用得到的字段）。
type pageTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Dial 连到本机调试端口上的 WorkBuddy 渲染页面。
//
// 优先选 renderer/index.html（主窗口），没有就退到第一个 page target。
func Dial(port int, timeout time.Duration) (*Client, error) {
	httpc := &http.Client{Timeout: timeout}
	resp, err := httpc.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, fmt.Errorf("调试端口 %d 不可用（客户端是否带 --remote-debugging-port 启动？）: %w", port, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("调试端口返回 HTTP %d", resp.StatusCode)
	}

	var targets []pageTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, fmt.Errorf("解析调试目标列表失败: %w", err)
	}
	var chosen *pageTarget
	for i := range targets {
		if targets[i].Type == "page" && strings.Contains(targets[i].URL, "renderer/index.html") {
			chosen = &targets[i]
			break
		}
	}
	if chosen == nil {
		for i := range targets {
			if targets[i].Type == "page" {
				chosen = &targets[i]
				break
			}
		}
	}
	if chosen == nil || chosen.WebSocketDebuggerURL == "" {
		return nil, errors.New("没有可注入的页面（窗口可能还没加载完）")
	}

	ws, err := dialWS(chosen.WebSocketDebuggerURL, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{ws: ws}, nil
}

// Close 关闭会话。
func (c *Client) Close() { c.ws.close() }

// evalResponse 是 Runtime.evaluate 的响应子集。
type evalResponse struct {
	Result struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// Evaluate 执行一段表达式，返回 result.value 的原始 JSON。
func (c *Client) Evaluate(expression string, timeout time.Duration) (json.RawMessage, error) {
	c.next++
	id := c.next
	req := map[string]any{
		"id":     id,
		"method": "Runtime.evaluate",
		"params": map[string]any{
			"expression":    expression,
			"returnByValue": true,
			"awaitPromise":  true,
		},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.ws.writeFrame(0x1, raw); err != nil {
		return nil, fmt.Errorf("发送调试指令失败: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errors.New("等待调试响应超时")
		}
		payload, err := c.ws.readMessage(remaining)
		if err != nil {
			return nil, fmt.Errorf("读取调试响应失败: %w", err)
		}
		var probe struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &probe) != nil || probe.ID != id {
			continue // 事件通知或其它 id 的响应，跳过
		}
		if probe.Error != nil {
			return nil, fmt.Errorf("调试指令被拒绝: %s", probe.Error.Message)
		}
		var resp evalResponse
		if err := json.Unmarshal(probe.Result, &resp); err != nil {
			return nil, err
		}
		if resp.ExceptionDetails != nil {
			desc := resp.ExceptionDetails.Text
			if resp.ExceptionDetails.Exception != nil && resp.ExceptionDetails.Exception.Description != "" {
				desc = resp.ExceptionDetails.Exception.Description
			}
			return nil, fmt.Errorf("注入脚本执行出错: %s", desc)
		}
		return resp.Result.Value, nil
	}
}

// EvalString 执行表达式并按字符串取值（undefined 视为空串）。
func (c *Client) EvalString(expression string, timeout time.Duration) (string, error) {
	raw, err := c.Evaluate(expression, timeout)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return string(raw), nil
	}
	return s, nil
}
