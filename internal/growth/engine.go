package growth

import (
	"fmt"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

// TaskRunResult 描述单个成长任务的执行与领奖结果。
type TaskRunResult struct {
	TaskCode       string `json:"task_code"`
	Code           string `json:"code,omitempty"`
	Title          string `json:"title"`
	TaskName       string `json:"task_name,omitempty"`
	Name           string `json:"name,omitempty"`
	Success        bool   `json:"success"`
	Skipped        bool   `json:"skipped,omitempty"`
	Claimed        bool   `json:"claimed"`
	CreditGranted  int64  `json:"credit_granted"`
	Credit         int64  `json:"credit,omitempty"`
	EnergyGranted  int64  `json:"energy_granted"`
	Energy         int64  `json:"energy,omitempty"`
	ProgressBefore string `json:"progress_before"`
	ProgressAfter  string `json:"progress_after"`
	Message        string `json:"message"`
}

// SweepResult 描述一键扫荡全部任务的战报总结。
type SweepResult struct {
	TotalCreditGranted int64           `json:"total_credit_granted"`
	TotalCreditEarned  int64           `json:"total_credit_earned"`
	TotalEnergyGranted int64           `json:"total_energy_granted"`
	TotalEnergyEarned  int64           `json:"total_energy_earned"`
	TasksCompleted     int             `json:"tasks_completed"`
	SuccessCount       int             `json:"success_count"`
	TasksSkipped       int             `json:"tasks_skipped"`
	Details            []TaskRunResult `json:"details"`
	Results            []TaskRunResult `json:"results,omitempty"`
	CompensationNotice string          `json:"compensation_notice,omitempty"`
}

// mpTasksMap 记录小程序口径专属任务（accept/claim 必须携带 MP 头）。
var mpTasksMap = map[string]bool{
	"school_season":      true,
	"Sequential_Tasks_1": true,
	"Sequential_Tasks_2": true,
	"Sequential_Tasks_3": true,
	"Sequential_Tasks_4": true,
	"Sequential_Tasks_5": true,
	"Sequential_Tasks_6": true,
	"Sequential_Tasks_7": true,
}

// IsMPTask 检查指定任务编码是否为小程序专属口径。
func IsMPTask(code string) bool {
	return mpTasksMap[code]
}

// autoTaskOrder 定义任务自动化的依赖推荐执行顺序。
var autoTaskOrder = []string{
	"chat_5",
	"first_buddy",
	"Model_chat_GLM5.2",
	"RichMeow_Chat",
	"Buddy_App",
	"Buddy_App_QQ",
	"automation_1",
	"Library_read",
	"template_5",
	"playbook_prompt",
	"create_canvas",
	"Hp_Appearance",
	"skill_1",
	"Expert_lighthouse",
	"expert_5",
	"Expert_team_use_3",
	"school_season",
	"Sequential_Tasks_1",
	"Sequential_Tasks_2",
	"Sequential_Tasks_3",
}

// accountMu 确保单账号的任务执行互斥，防止并发重跑浪费真实额度。
var (
	accountMu  sync.Mutex
	locks      = make(map[string]accountLockInfo)
	sweepMu    sync.Mutex
	sweepLocks = make(map[string]chan struct{})
)

type accountLockInfo struct {
	lockedAt time.Time
	taskCode string
}

func lockAccountWithTimeout(uid, taskCode string, timeout time.Duration) bool {
	if uid == "" {
		uid = "default"
	}
	deadline := time.Now().Add(timeout)
	for {
		accountMu.Lock()
		info, locked := locks[uid]
		if locked {
			// 若锁持有超过 90 秒，视为死锁/僵尸锁强制回收
			if time.Since(info.lockedAt) > 90*time.Second {
				delete(locks, uid)
				locked = false
			}
		}
		if !locked {
			locks[uid] = accountLockInfo{
				lockedAt: time.Now(),
				taskCode: taskCode,
			}
			accountMu.Unlock()
			return true
		}
		accountMu.Unlock()

		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func unlockAccount(uid string) {
	if uid == "" {
		uid = "default"
	}
	accountMu.Lock()
	defer accountMu.Unlock()
	delete(locks, uid)
}

// RunTask 执行单个任务的完整闭环：前置判定 -> 执行动作 -> 异步落账回读 -> 自动领奖。
func (c *Client) RunTask(cred credential.Credential, taskCode string) (*TaskRunResult, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	if !lockAccountWithTimeout(cred.UID, taskCode, 8*time.Second) {
		return nil, fmt.Errorf("当前账号已有成长任务正在执行中，请稍后再试")
	}
	defer unlockAccount(cred.UID)

	isMP := IsMPTask(taskCode)

	// 1. 读取当前状态
	allTasks, err := c.ListTasks(cred, isMP)
	if err != nil {
		return nil, fmt.Errorf("读取任务列表失败: %w", err)
	}

	var targetTask *GrowthTask
	for i := range allTasks {
		if allTasks[i].TaskCode == taskCode {
			targetTask = &allTasks[i]
			break
		}
	}
	if targetTask == nil {
		return nil, fmt.Errorf("任务 %s 未在当前账号列表中下发", taskCode)
	}

	progBefore := fmt.Sprintf("%d/%d", targetTask.Current, targetTask.Target)
	if targetTask.Claimed {
		return &TaskRunResult{
			TaskCode:       taskCode,
			Code:           taskCode,
			Title:          targetTask.Title,
			TaskName:       targetTask.Title,
			Name:           targetTask.Title,
			Success:        true,
			Skipped:        true,
			Claimed:        true,
			ProgressBefore: "claimed",
			ProgressAfter:  "claimed",
			Message:        "该任务此前已领取奖励",
		}, nil
	}

	// 若任务此前已达成（如在客户端或网页端已达 1/1，或 accept_status 为 completed），无需重复跑动作，直接领奖！
	if targetTask.Claimable || (targetTask.Target > 0 && targetTask.Current >= targetTask.Target) || targetTask.AcceptStatus == "completed" {
		credit, energy, cerr := c.ClaimReward(cred, taskCode, isMP)
		if cerr == nil {
			return &TaskRunResult{
				TaskCode:       taskCode,
				Code:           taskCode,
				Title:          targetTask.Title,
				TaskName:       targetTask.Title,
				Name:           targetTask.Title,
				ProgressBefore: progBefore,
				ProgressAfter:  "claimed",
				Claimed:        true,
				CreditGranted:  credit,
				Credit:         credit,
				EnergyGranted:  energy,
				Energy:         energy,
				Success:        true,
				Message:        fmt.Sprintf("任务此前已达成（%s），直接领奖入账（+%dc +%de）", progBefore, credit, energy),
			}, nil
		}
	}

	// 2. 若未报名则先 accept 并短暂等待登记生效
	if targetTask.AcceptStatus == "not_accepted" || targetTask.AcceptStatus == "" {
		_ = c.AcceptTasks(cred, []string{taskCode}, isMP)
		time.Sleep(800 * time.Millisecond)
	}

	// 3. 执行特定动作
	actionMsg, err := c.dispatchTaskAction(cred, taskCode)
	if err != nil {
		return nil, fmt.Errorf("执行任务动作失败: %w", err)
	}

	// 4. 有界轮询异步计分（最多 5 轮，每次 2.5s，总计 12.5s 覆盖官方服务端异步落账延迟）
	var finalTask *GrowthTask
	for attempt := 0; attempt < 5; attempt++ {
		time.Sleep(2500 * time.Millisecond)
		tasksNow, qerr := c.ListTasks(cred, isMP)
		if qerr == nil {
			for i := range tasksNow {
				if tasksNow[i].TaskCode == taskCode {
					finalTask = &tasksNow[i]
					break
				}
			}
		}
		if finalTask != nil && (finalTask.Claimable || finalTask.Claimed || finalTask.Current >= targetTask.Target || finalTask.AcceptStatus == "completed") {
			break
		}
	}

	progAfter := progBefore
	if finalTask != nil {
		progAfter = fmt.Sprintf("%d/%d", finalTask.Current, finalTask.Target)
		if finalTask.Claimed {
			progAfter = "claimed"
		}
	}

	// 5. 达标即自动领奖
	res := &TaskRunResult{
		TaskCode:       taskCode,
		Code:           taskCode,
		Title:          targetTask.Title,
		TaskName:       targetTask.Title,
		Name:           targetTask.Title,
		ProgressBefore: progBefore,
		ProgressAfter:  progAfter,
		Message:        actionMsg,
		Success:        true,
	}

	if finalTask != nil && (finalTask.Claimable || finalTask.Current >= targetTask.Target || finalTask.AcceptStatus == "completed") && !finalTask.Claimed {
		credit, energy, cerr := c.ClaimReward(cred, taskCode, isMP)
		if cerr == nil && (credit > 0 || energy > 0) {
			res.Claimed = true
			res.CreditGranted = credit
			res.Credit = credit
			res.EnergyGranted = energy
			res.Energy = energy
			res.ProgressAfter = "claimed"
			res.Message += fmt.Sprintf("；已自动领奖入账（+%dc +%de）", credit, energy)
		}
	}

	return res, nil
}

// dispatchTaskAction 派发各个具体任务的行为动作链。
func (c *Client) dispatchTaskAction(cred credential.Credential, code string) (string, error) {
	ms := time.Now().UnixMilli()
	convID := fmt.Sprintf("wbmux-task-%s-%d", code, ms)
	reqID := fmt.Sprintf("req-%d", ms)

	switch code {
	case "chat_5":
		for i := 0; i < 5; i++ {
			ev := map[string]any{
				"eventCode":                         "chat_request_send",
				"traceId":                           fmt.Sprintf("%s-%d", convID, i),
				"rootRequestId":                     fmt.Sprintf("%s-%d", reqID, i),
				"parentConversationId":              convID,
				"agentName":                         "cli",
				"agentType":                         "main",
				"codebuddy.session_id":              convID,
				"codebuddy.conversation_request_id": fmt.Sprintf("%s-%d", reqID, i),
			}
			_ = c.ReportDesktopEvent(cred, ev)
			time.Sleep(300 * time.Millisecond)
		}
		return "已上报 5 条对话活跃事件", nil

	case "first_buddy":
		_ = c.ReportDesktopEvent(cred, map[string]any{"eventCode": "chat_request_send", "traceId": convID})
		time.Sleep(300 * time.Millisecond)
		if err := c.CompleteFirstBuddy(cred); err != nil {
			return "已触发领养链路", nil
		}
		return "已同意协议并领养首只 Buddy 猫咪（+300c +8e）", nil

	case "Model_chat_GLM5.2":
		_, probeReqID, _ := c.ShortChatProbe(cred, "glm-5.2", "")
		if probeReqID == "" {
			probeReqID = reqID
		}
		ev := map[string]any{
			"eventCode":        "chat_message_response",
			"requestModelId":   "glm-5.2",
			"requestModelName": "GLM-5.2",
			"isSuccessful":     true,
			"traceId":          probeReqID,
			"conversationId":   convID,
		}
		_ = c.ReportDesktopEvent(cred, ev)
		return "已完成 GLM-5.2 真实单次短对话并对齐上报", nil

	case "RichMeow_Chat":
		events := BuildDesktopChatSequence(convID, reqID, "msg-"+reqID, "fast-model", "fast-model")
		if err := c.ReportDesktopEvent(cred, events...); err != nil {
			return "", err
		}
		return "已按 WorkBuddy Desktop 5.5.6 指纹上报完整 6 事件链", nil

	case "Buddy_App", "Buddy_App_QQ":
		events := BuildBuddyAppSequence("cb_y5Dy46tPQGGWtueMxXbe", "企鹅教师助手")
		if err := c.ReportDesktopEvent(cred, events...); err != nil {
			return "", err
		}
		return "已上报 Buddy 应用发现与进入五连事件", nil

	case "automation_1":
		ev := map[string]any{
			"eventCode":    "automated_task_create_suc",
			"name":         "wbmux 自动化",
			"source":       "manually",
			"modelId":      "fast-model",
			"scheduleType": "once",
			"mode":         "LOCAL",
		}
		if err := c.ReportDesktopEvent(cred, ev); err != nil {
			return "", err
		}
		return "已上报定时任务创建成功事件", nil

	case "Library_read":
		const docURL = "https://www.workbuddy.cn/space/d/o0KWYeynteVv06UnAZqIFm"
		ev := BuildWebEvent(cred.UID, "", "web_element_click", docURL, "library_doc_intro_click", "WorkBuddy资料库介绍")
		if err := c.ReportWebEvent(cred, ev); err != nil {
			return "", err
		}
		return "已上报资料库介绍阅读事件", nil

	case "template_5", "template_use":
		templates := [][2]string{{"1", "深度研究"}, {"2", "周报生成"}, {"3", "竞品分析"}, {"4", "活动策划"}, {"5", "代码评审"}}
		for i, tp := range templates {
			cID := fmt.Sprintf("%s-%d", convID, i)
			rID := fmt.Sprintf("%s-%d", reqID, i)
			events := BuildTemplateUseSequence(cID, rID, tp[0], tp[1])
			_ = c.ReportDesktopEvent(cred, events...)
			time.Sleep(200 * time.Millisecond)
		}
		return "已上报 5 组模板任务使用事件组", nil

	case "playbook_prompt":
		events := BuildPlaybookPromptSequence(convID, reqID, "pm-gtm-launch-plan", "新产品上市 GTM 发布计划一页纸")
		if err := c.ReportDesktopEvent(cred, events...); err != nil {
			return "", err
		}
		return "已上报灵感案例做同款 Prompt 发送事件", nil

	case "create_canvas", "canvas_node_create":
		events := BuildDesignCanvasSequence(convID, reqID)
		if err := c.ReportDesktopEvent(cred, events...); err != nil {
			return "", err
		}
		return "已上报设计创意画布创建与打开事件（+300c +5e）", nil

	case "Hp_Appearance", "switch_theme":
		_ = c.SetAppearanceTheme(cred, "theme-tkmw7j")
		time.Sleep(300 * time.Millisecond)
		ev := map[string]any{
			"eventCode": "appearance_skin_apply",
			"action":    "apply",
			"source":    "settings_close",
			"id":        "theme-tkmw7j",
		}
		_ = c.ReportDesktopEvent(cred, ev)
		return "已设置激战金秋主题并上报皮肤生效事件", nil

	case "desktop_login":
		events := BuildDesktopLoginSequence()
		_ = c.ReportDesktopEvent(cred, events...)
		return "已上报桌面客户端登录成功事件", nil

	case "skill_1":
		_, probeReqID, _ := c.ShortChatProbe(cred, "fast-model", "")
		if probeReqID == "" {
			probeReqID = reqID
		}
		ev := map[string]any{
			"eventCode":      "skill_info",
			"id":             "润泽小馆·日报撰写",
			"skillId":        "skill_2097350077599879168",
			"skillVersion":   "1.0.0",
			"toolStatus":     "success",
			"fileCount":      56,
			"source":         "workbuddy-desktop",
			"conversationId": convID,
			"requestId":      probeReqID,
		}
		_ = c.ReportDesktopEvent(cred, ev)
		return "已完成短会话并上报热门技能尝鲜事件", nil

	case "Expert_lighthouse":
		const lhID = "ex_2cvvUZQhDyeJ"
		_, probeReqID, _ := c.ShortChatProbe(cred, "fast-model", lhID)
		if probeReqID == "" {
			probeReqID = reqID
		}
		events := BuildDesktopChatSequence(convID, probeReqID, "msg-"+probeReqID, "fast-model", "fast-model")
		events = append(events, map[string]any{
			"eventCode":      "expert_actual_use",
			"id":             lhID,
			"name":           "腾讯轻量云专家",
			"expertTitle":    "腾讯轻量云专家",
			"type":           "",
			"mode":           "LOCAL",
			"cost":           0,
			"conversationId": convID,
			"requestId":      probeReqID,
		})
		_ = c.ReportDesktopEvent(cred, events...)
		return "已完成腾讯轻量云专家召唤与使用（包含免费领取一个月轻量云服务器权益）", nil

	case "expert_5", "Expert_team_use_3":
		rounds := 5
		expertType := "agent"
		if code == "Expert_team_use_3" {
			rounds = 3
			expertType = "team"
		}
		// 动态拉取市场真实在架专家列表，防止自造假 ID 被服务端静默丢弃
		realExperts, _ := c.MarketExpertList(cred, expertType)
		var fallbackIDs = []string{"ex_2cvvUZQhDyeJ", "ex_1789234850", "ex_2097350077599879168"}

		for i := 0; i < rounds; i++ {
			cID := fmt.Sprintf("%s-%d", convID, i)
			eID := fallbackIDs[i%len(fallbackIDs)]
			eName := "腾讯效能专家"
			if len(realExperts) > i {
				eID = realExperts[i].ExpertID
				if realExperts[i].DisplayNameZH != "" {
					eName = realExperts[i].DisplayNameZH
				}
			}

			_, pReqID, _ := c.ShortChatProbe(cred, "fast-model", eID)
			if pReqID == "" {
				pReqID = fmt.Sprintf("%s-%d", reqID, i)
			}
			events := BuildDesktopChatSequence(cID, pReqID, "msg-"+pReqID, "fast-model", "fast-model")
			events = append(events, map[string]any{
				"eventCode":      "expert_actual_use",
				"id":             eID,
				"name":           eName,
				"mode":           "LOCAL",
				"conversationId": cID,
				"requestId":      pReqID,
			})
			_ = c.ReportDesktopEvent(cred, events...)
			time.Sleep(300 * time.Millisecond)
		}
		return fmt.Sprintf("已完成 %d 轮真实在架专家/专家团召唤与使用事件链", rounds), nil

	case "Sequential_Tasks_2":
		// 小程序选中专家并完成有效对话：mp 指纹 expert_actual_use + 真实在架专家 ID
		experts, _ := c.MarketExpertList(cred, "")
		expertID := "ex_2cvvUZQhDyeJ"
		expertName := "腾讯轻量云专家"
		if len(experts) > 0 {
			expertID = experts[0].ExpertID
			if experts[0].DisplayNameZH != "" {
				expertName = experts[0].DisplayNameZH
			}
		}
		ev := map[string]any{
			"eventCode":  "expert_actual_use",
			"id":         expertID,
			"name":       expertName,
			"type":       "send_message",
			"extVersion": "2.2.8",
			"source":     "mini_program",
		}
		_ = c.ReportMPEvent(cred, ev)
		return "已按小程序口径上报专家对话事件（+200c +5e）", nil

	case "school_season", "Sequential_Tasks_1", "Sequential_Tasks_3":
		ev := map[string]any{
			"eventCode":                         "chat_request_send",
			"source":                            "mini_program",
			"traceId":                           convID,
			"rootRequestId":                     reqID,
			"parentConversationId":              convID,
			"codebuddy.session_id":              convID,
			"codebuddy.conversation_request_id": reqID,
		}
		if code == "school_season" {
			ev["activityId"] = "school_open_day_2026"
		}
		_ = c.ReportMPEvent(cred, ev)
		return "已按小程序专属口径上报对话判据事件", nil

	default:
		return "未定义动作，已跳过", nil
	}
}

// SweepAll 一键扫荡当前账号的全部待办任务，自动完成并领取全部奖励，顺带检测官方故障补偿。
func (c *Client) SweepAll(cred credential.Credential) (*SweepResult, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	uid := cred.UID
	if uid == "" {
		uid = "default"
	}

	sweepMu.Lock()
	waitCh, running := sweepLocks[uid]
	if running {
		sweepMu.Unlock()
		// 若后台守护或外部已有扫荡在进行中，平滑等待其完成（最多 30s），避免并发冲突
		select {
		case <-waitCh:
		case <-time.After(30 * time.Second):
		}
	} else {
		doneCh := make(chan struct{})
		sweepLocks[uid] = doneCh
		sweepMu.Unlock()
		defer func() {
			sweepMu.Lock()
			delete(sweepLocks, uid)
			close(doneCh)
			sweepMu.Unlock()
		}()
	}

	// 1. 扫描常规任务 + 小程序任务
	tasksNormal, _ := c.ListTasks(cred, false)
	tasksMP, _ := c.ListTasks(cred, true)

	taskMap := make(map[string]GrowthTask)
	for _, t := range tasksNormal {
		taskMap[t.TaskCode] = t
	}
	for _, t := range tasksMP {
		if _, exists := taskMap[t.TaskCode]; !exists {
			taskMap[t.TaskCode] = t
		}
	}

	result := &SweepResult{
		Details: make([]TaskRunResult, 0),
	}

	// 2. 按依赖顺序执行待办任务
	for _, code := range autoTaskOrder {
		t, exists := taskMap[code]
		if !exists {
			continue
		}
		if t.Claimed {
			result.TasksSkipped++
			continue
		}

		runRes, err := c.RunTask(cred, code)
		if err != nil {
			result.Details = append(result.Details, TaskRunResult{
				TaskCode: code,
				Title:    t.Title,
				Message:  "执行中断: " + err.Error(),
			})
			continue
		}

		result.Details = append(result.Details, *runRes)
		if runRes.Claimed {
			result.TasksCompleted++
			result.TotalCreditGranted += runRes.CreditGranted
			result.TotalEnergyGranted += runRes.EnergyGranted
		} else if runRes.Skipped {
			result.TasksSkipped++
		}
	}

	// 3. 收割所有残留达标待领奖任务（防止由于异步延迟或非核心列表遗漏）
	tasksAfterNormal, _ := c.ListTasks(cred, false)
	tasksAfterMP, _ := c.ListTasks(cred, true)
	allFinalTasks := make(map[string]GrowthTask)
	for _, t := range tasksAfterNormal {
		allFinalTasks[t.TaskCode] = t
	}
	for _, t := range tasksAfterMP {
		if _, exists := allFinalTasks[t.TaskCode]; !exists {
			allFinalTasks[t.TaskCode] = t
		}
	}

	for _, t := range allFinalTasks {
		if t.Claimed {
			continue
		}
		if t.Claimable || (t.Target > 0 && t.Current >= t.Target) || t.AcceptStatus == "completed" {
			isMP := IsMPTask(t.TaskCode)
			credit, energy, cerr := c.ClaimReward(cred, t.TaskCode, isMP)
			if cerr == nil && (credit > 0 || energy > 0) {
				result.TasksCompleted++
				result.TotalCreditGranted += credit
				result.TotalEnergyGranted += energy
				result.Details = append(result.Details, TaskRunResult{
					TaskCode:       t.TaskCode,
					Code:           t.TaskCode,
					Title:          t.Title,
					TaskName:       t.Title,
					Name:           t.Title,
					ProgressBefore: fmt.Sprintf("%d/%d", t.Current, t.Target),
					ProgressAfter:  "claimed",
					Claimed:        true,
					CreditGranted:  credit,
					Credit:         credit,
					EnergyGranted:  energy,
					Energy:         energy,
					Success:        true,
					Message:        fmt.Sprintf("残留待领奖任务已自动入账（+%dc +%de）", credit, energy),
				})
			}
		}
	}

	// 4. 顺带检查官方异常补偿状态
	comp, err := c.FetchCompensationStatus(cred)
	if err == nil && comp != nil && comp.Pending() {
		granted, cerr := c.ClaimCompensation(cred)
		if cerr == nil && granted > 0 {
			result.TotalCreditGranted += granted
			result.CompensationNotice = fmt.Sprintf("发现官方异常补偿并已自动领取 +%d 积分！", granted)
		} else {
			result.CompensationNotice = fmt.Sprintf("发现官方异常补偿（+%d 积分，截止: %s），待领取", comp.CreditNum, comp.EndTime)
		}
	}

	result.TotalCreditEarned = result.TotalCreditGranted
	result.TotalEnergyEarned = result.TotalEnergyGranted
	result.SuccessCount = result.TasksCompleted
	result.Results = result.Details

	return result, nil
}

// ClaimAllPending 一键立即领取当前账号中所有已达标但尚未领取的奖励（不执行任何消耗性任务动作）。
func (c *Client) ClaimAllPending(cred credential.Credential) (*SweepResult, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	tasksNormal, _ := c.ListTasks(cred, false)
	tasksMP, _ := c.ListTasks(cred, true)

	taskMap := make(map[string]GrowthTask)
	for _, t := range tasksNormal {
		taskMap[t.TaskCode] = t
	}
	for _, t := range tasksMP {
		if _, exists := taskMap[t.TaskCode]; !exists {
			taskMap[t.TaskCode] = t
		}
	}

	result := &SweepResult{
		Details: make([]TaskRunResult, 0),
	}

	for _, t := range taskMap {
		if t.Claimed {
			continue
		}
		isReached := t.Claimable || (t.Target > 0 && t.Current >= t.Target) || t.AcceptStatus == "completed"
		if !isReached {
			continue
		}

		isMP := IsMPTask(t.TaskCode)
		credit, energy, cerr := c.ClaimReward(cred, t.TaskCode, isMP)
		runRes := TaskRunResult{
			TaskCode:       t.TaskCode,
			Code:           t.TaskCode,
			Title:          t.Title,
			TaskName:       t.Title,
			Name:           t.Title,
			ProgressBefore: fmt.Sprintf("%d/%d", t.Current, t.Target),
			ProgressAfter:  "claimed",
			Success:        cerr == nil,
		}
		if cerr == nil {
			runRes.Claimed = true
			runRes.CreditGranted = credit
			runRes.Credit = credit
			runRes.EnergyGranted = energy
			runRes.Energy = energy
			runRes.Message = fmt.Sprintf("已成功领奖入账（+%dc +%de）", credit, energy)
			result.TasksCompleted++
			result.TotalCreditGranted += credit
			result.TotalEnergyGranted += energy
		} else {
			runRes.Message = "领奖失败: " + cerr.Error()
		}
		result.Details = append(result.Details, runRes)
	}

	// 顺带检查官方异常补偿状态
	comp, err := c.FetchCompensationStatus(cred)
	if err == nil && comp != nil && comp.Pending() {
		granted, cerr := c.ClaimCompensation(cred)
		if cerr == nil && granted > 0 {
			result.TotalCreditGranted += granted
			result.CompensationNotice = fmt.Sprintf("发现官方异常补偿并已自动领取 +%d 积分！", granted)
		}
	}

	result.TotalCreditEarned = result.TotalCreditGranted
	result.TotalEnergyEarned = result.TotalEnergyGranted
	result.SuccessCount = result.TasksCompleted
	result.Results = result.Details

	return result, nil
}

