package growth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

// GrowthTask 代表国内端成长计划中单个任务的状态与奖励。
type GrowthTask struct {
	TaskCode     string `json:"task_code"`
	Code         string `json:"code,omitempty"`
	Title        string `json:"title,omitempty"`
	Name         string `json:"name,omitempty"`
	Description  string `json:"description,omitempty"`
	Desc         string `json:"desc,omitempty"`
	TaskDesc     string `json:"task_desc,omitempty"`
	Credit       int64  `json:"credit,omitempty"`
	RewardCredit int64  `json:"reward_credit,omitempty"`
	Energy       int64  `json:"energy,omitempty"`
	RewardEnergy int64  `json:"reward_energy,omitempty"`
	HasReward    bool   `json:"has_reward,omitempty"`
	RewardBuddy  bool   `json:"reward_buddy,omitempty"`
	RewardText   string `json:"reward_text,omitempty"`
	TaskType     string `json:"task_type,omitempty"`
	Tag          string `json:"tag,omitempty"`
	Category     string `json:"category,omitempty"`
	JumpURL      string `json:"jump_url,omitempty"`
	Locked       bool   `json:"locked,omitempty"`
	Target       int64  `json:"target"`
	Current      int64  `json:"current"`
	Progress     string `json:"progress,omitempty"`
	AcceptStatus string `json:"accept_status,omitempty"` // not_accepted / accepted / completed / claimed
	Status       string `json:"status,omitempty"`
	Claimable    bool   `json:"claimable"`
	Claimed      bool   `json:"claimed"`
	CanAutomate  bool   `json:"can_automate"`
}

// CompensationStatus 代表官方不定期服务故障补偿状态（先查后领）。
type CompensationStatus struct {
	Active         bool   `json:"active"`
	Claimed        bool   `json:"claimed"`
	CreditNum      int64  `json:"credit_num"`
	ValidityPeriod int64  `json:"validity_period"`
	EndTime        string `json:"end_time"` // "2026-10-22 23:59:59"
}

// Pending 报告是否存在当前有效且尚未领取的官方补偿。
func (s CompensationStatus) Pending() bool {
	return s.Active && !s.Claimed && s.CreditNum > 0
}

// rawTask 是官方成长任务上游接口返回的数据格式（兼容对象 progress 与标量字段）。
type rawTask struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	TaskDesc     string `json:"task_desc"`
	RewardCredit int64  `json:"reward_credit"`
	RewardEnergy int64  `json:"reward_energy"`
	Credit       int64  `json:"credit"`
	Energy       int64  `json:"energy"`
	HasReward    bool   `json:"has_reward"`
	RewardBuddy  bool   `json:"reward_buddy"`
	TaskType     string `json:"task_type"`
	Tag          string `json:"tag"`
	JumpURL      string `json:"jump_url"`
	Locked       bool   `json:"locked"`
	Target       int64  `json:"target"`
	Current      int64  `json:"current"`
	AcceptStatus string `json:"accept_status"`
	Status       string `json:"status"`
	Progress     any    `json:"progress"` // 可能为 {"current": 1, "target": 5} 或已平铺
}

func (r rawTask) toGrowthTask() GrowthTask {
	t := GrowthTask{
		TaskCode:     r.TaskCode,
		Title:        r.Title,
		Description:  r.Description,
		TaskDesc:     r.TaskDesc,
		Credit:       r.RewardCredit,
		Energy:       r.RewardEnergy,
		HasReward:    r.HasReward,
		RewardBuddy:  r.RewardBuddy,
		TaskType:     r.TaskType,
		Tag:          r.Tag,
		JumpURL:      r.JumpURL,
		Locked:       r.Locked,
		Target:       r.Target,
		Current:      r.Current,
		AcceptStatus: r.AcceptStatus,
		Status:       r.Status,
	}
	if t.Credit == 0 && r.Credit > 0 {
		t.Credit = r.Credit
	}
	if t.Energy == 0 && r.Energy > 0 {
		t.Energy = r.Energy
	}

	// 兼容解析 progress 嵌套对象形态
	if progMap, ok := r.Progress.(map[string]any); ok {
		if cur, ok := progMap["current"].(float64); ok {
			t.Current = int64(cur)
		}
		if tgt, ok := progMap["target"].(float64); ok {
			t.Target = int64(tgt)
		}
	}

	// 研判领取状态
	if t.AcceptStatus == "claimed" {
		t.Claimed = true
	} else if t.Current >= t.Target && t.Target > 0 {
		t.Claimable = true
	} else if t.AcceptStatus == "completed" {
		t.Claimable = true
	}

	// 补全前端友好别名字段
	t.Code = t.TaskCode
	t.Name = t.Title
	if t.Name == "" {
		t.Name = t.TaskCode
	}
	t.Desc = t.Description
	if t.Desc == "" {
		t.Desc = t.TaskDesc
	}
	t.RewardCredit = t.Credit
	t.RewardEnergy = t.Energy
	t.Progress = fmt.Sprintf("%d/%d", t.Current, t.Target)
	t.CanAutomate = (t.TaskCode != "donate_author")
	t.Category = inferTaskCategory(t.TaskCode, t.Tag)
	t.RewardText = formatRewardText(t.Credit, t.Energy)

	return t
}

func inferTaskCategory(code, tag string) string {
	if tag != "" {
		return tag
	}
	switch {
	case strings.HasPrefix(code, "Sequential_Tasks_") || code == "school_season":
		return "小程序任务"
	case strings.Contains(code, "desktop") || code == "switch_theme":
		return "桌面端体验"
	case strings.Contains(code, "Buddy") || strings.Contains(code, "chat"):
		return "Buddy应用"
	case strings.Contains(code, "canvas") || strings.Contains(code, "template"):
		return "灵感设计"
	case code == "donate_author":
		return "开源赞赏"
	default:
		return "常用任务"
	}
}

func formatRewardText(credit, energy int64) string {
	var parts []string
	if credit > 0 {
		parts = append(parts, fmt.Sprintf("+%d 积分", credit))
	}
	if energy > 0 {
		parts = append(parts, fmt.Sprintf("+%d 能量", energy))
	}
	if len(parts) == 0 {
		return "福利奖励"
	}
	return strings.Join(parts, " · ")
}

// ListTasks 查询国内成长计划的全量任务列表。
// isMP 为 true 时注入 X-Client-Platform: miniprogram 以拉取小程序口径专属任务。
func (c *Client) ListTasks(cred credential.Credential, isMP bool) ([]GrowthTask, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	url := c.endpoint + "/v2/activity/growth/tasks"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	applyHeaders(req, cred)
	if isMP {
		ApplyMPHeaders(req, cred.Token, cred.UID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, string(body))
	}

	var resDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Tasks []rawTask `json:"tasks"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &resDoc); err != nil {
		return nil, fmt.Errorf("unmarshal tasks response: %w", err)
	}

	tasks := make([]GrowthTask, 0, len(resDoc.Data.Tasks))
	for _, rt := range resDoc.Data.Tasks {
		tasks = append(tasks, rt.toGrowthTask())
	}
	return tasks, nil
}

// AcceptTasks 报名/接受指定的成长任务（幂等，不推进进度）。
func (c *Client) AcceptTasks(cred credential.Credential, taskCodes []string, isMP bool) error {
	if cred.Token == "" {
		return fmt.Errorf("credential token is empty")
	}
	if len(taskCodes) == 0 {
		return nil
	}

	payload, _ := json.Marshal(map[string]any{
		"task_codes": taskCodes,
	})

	url := c.endpoint + "/v2/activity/growth/tasks/accept"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	applyHeaders(req, cred)
	if isMP {
		ApplyMPHeaders(req, cred.Token, cred.UID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("accept tasks upstream %d: %s", resp.StatusCode, string(body))
	}

	var resDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resDoc); err == nil && resDoc.Code != 0 {
		return fmt.Errorf("accept tasks rejected: %s", resDoc.Msg)
	}

	return nil
}

// ClaimReward 领取已达标任务的奖励积分与能量（幂等；重复领返回 0, 0, nil）。
func (c *Client) ClaimReward(cred credential.Credential, taskCode string, isMP bool) (int64, int64, error) {
	if cred.Token == "" {
		return 0, 0, fmt.Errorf("credential token is empty")
	}

	payload, _ := json.Marshal(map[string]string{
		"task_code": taskCode,
	})

	url := c.endpoint + "/v2/activity/growth/tasks/reward/claim"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, 0, err
	}

	applyHeaders(req, cred)
	if isMP {
		ApplyMPHeaders(req, cred.Token, cred.UID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}

	var resDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			RewardCredit int64 `json:"reward_credit"`
			RewardEnergy int64 `json:"reward_energy"`
			Credit       int64 `json:"credit"`
			Energy       int64 `json:"energy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return 0, 0, fmt.Errorf("unmarshal claim reward: %w", err)
	}

	// 重复领取容错：already_claimed 业务返回 0, 0
	if resDoc.Code != 0 {
		if strings.Contains(resDoc.Msg, "already") || strings.Contains(resDoc.Msg, "已领取") {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("claim failed: %s", resDoc.Msg)
	}

	credit := resDoc.Data.RewardCredit
	if credit == 0 {
		credit = resDoc.Data.Credit
	}
	energy := resDoc.Data.RewardEnergy
	if energy == 0 {
		energy = resDoc.Data.Energy
	}

	return credit, energy, nil
}

// FetchCompensationStatus 查询当前账号的官方限时故障补偿状态。
func (c *Client) FetchCompensationStatus(cred credential.Credential) (*CompensationStatus, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	url := c.endpoint + "/v2/billing/meter/compensation-status"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}

	applyHeaders(req, cred)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var resDoc struct {
		Code int                `json:"code"`
		Msg  string             `json:"msg"`
		Data CompensationStatus `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return nil, fmt.Errorf("unmarshal compensation: %w", err)
	}
	if resp.StatusCode != http.StatusOK || resDoc.Code != 0 {
		return nil, fmt.Errorf("compensation status failed: %s", resDoc.Msg)
	}

	return &resDoc.Data, nil
}

// ClaimCompensation 领取当前生效的官方限时故障补偿积分。
func (c *Client) ClaimCompensation(cred credential.Credential) (int64, error) {
	if cred.Token == "" {
		return 0, fmt.Errorf("credential token is empty")
	}

	url := c.endpoint + "/v2/billing/meter/claim-compensation"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, err
	}

	applyHeaders(req, cred)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var resDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			CreditGranted int64 `json:"credit_granted"`
			Credit        int64 `json:"credit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return 0, fmt.Errorf("unmarshal claim compensation: %w", err)
	}
	if resp.StatusCode != http.StatusOK || resDoc.Code != 0 {
		return 0, fmt.Errorf("claim compensation failed: %s", resDoc.Msg)
	}

	granted := resDoc.Data.CreditGranted
	if granted == 0 {
		granted = resDoc.Data.Credit
	}
	return granted, nil
}
