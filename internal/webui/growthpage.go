package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/growth"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// domesticCred 读取国内侧当前账号的凭证。
func (s *Server) domesticCred(p *variant.Probe) (credential.Credential, error) {
	if s.domesticCredFn != nil {
		return s.domesticCredFn(p)
	}
	if exe := strings.TrimSpace(s.opts.HostExe); exe != "" {
		return credential.ResolveWith(p, variant.CN, exe)
	}
	return credential.Resolve(p, variant.CN)
}

// domesticEndpoint 返回国内版 API 端点根地址。
func (s *Server) domesticEndpoint() string {
	if s.domesticEndpointFn != nil {
		return s.domesticEndpointFn()
	}
	b, err := variant.Get(variant.CN)
	if err == nil && b.Endpoint != "" {
		return strings.TrimRight(b.Endpoint, "/")
	}
	return "https://copilot.tencent.com"
}

// handleGrowthStreak 读取国际端当前账号的成长中心连续活跃打卡与阶梯积分状态。
func (s *Server) handleGrowthStreak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	p := s.probe()
	c, err := s.intlCred(p)
	if err != nil || c.Token == "" {
		writeErr(w, http.StatusNotFound, "未配置国际版账号凭证")
		return
	}
	client := growth.NewClient(s.upstreamBase())
	streak, err := client.FetchStreak(credential.Credential{Token: c.Token, UID: c.UID})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "读取成长中心状态失败: "+err.Error())
		return
	}
	writeJSON(w, streak)
}

// handleGrowthRedeem 兑换国际端连续活跃里程碑阶梯积分奖励（如 14d / 28d）。
func (s *Server) handleGrowthRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tier == "" {
		writeErr(w, http.StatusBadRequest, "请求缺少 tier 参数")
		return
	}

	p := s.probe()
	c, err := s.intlCred(p)
	if err != nil || c.Token == "" {
		writeErr(w, http.StatusNotFound, "未配置国际版账号凭证")
		return
	}

	client := growth.NewClient(s.upstreamBase())
	granted, err := client.RedeemTier(credential.Credential{Token: c.Token, UID: c.UID}, req.Tier)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	usage.InvalidateLiveCache(variant.Intl)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已成功兑换国际端成长中心「%s」里程碑奖励，获得 %d 积分！", req.Tier, granted),
	})

	writeJSON(w, map[string]any{
		"ok":      true,
		"tier":    req.Tier,
		"granted": granted,
	})
}

// DomesticTasksResponse 国内端成长计划全量任务与收益响应。
type DomesticTasksResponse struct {
	Tasks              []growth.GrowthTask        `json:"tasks"`
	Items              []growth.GrowthTask        `json:"items"`
	PotentialCredit    int64                      `json:"potential_credit"`
	TotalCreditPending int64                      `json:"totalCreditPending"`
	PotentialEnergy    int64                      `json:"potential_energy"`
	TotalEnergyPending int64                      `json:"totalEnergyPending"`
	ClaimableCredit    int64                      `json:"claimable_credit"`
	ClaimableEnergy    int64                      `json:"claimable_energy"`
	Compensation       *growth.CompensationStatus `json:"compensation,omitempty"`
}

// handleGrowthDomesticTasks 查询国内侧全量成长计划任务状态与官方故障补偿。
func (s *Server) handleGrowthDomesticTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}

	p := s.probe()
	cred, err := s.domesticCred(p)
	if err != nil || cred.Token == "" {
		writeErr(w, http.StatusNotFound, "未检测到国内侧有效凭证，请先在官方客户端登录或导入凭据")
		return
	}

	client := growth.NewClient(s.domesticEndpoint())

	// 1. 双口径拉取并去重合并
	tasksNormal, terr := client.ListTasks(cred, false)
	if terr != nil {
		writeErr(w, http.StatusBadGateway, "拉取成长任务失败: "+terr.Error())
		return
	}
	tasksMP, _ := client.ListTasks(cred, true)

	taskMap := make(map[string]growth.GrowthTask)
	for _, t := range tasksNormal {
		taskMap[t.TaskCode] = t
	}
	for _, t := range tasksMP {
		if _, exists := taskMap[t.TaskCode]; !exists {
			taskMap[t.TaskCode] = t
		}
	}

	tasks := make([]growth.GrowthTask, 0, len(taskMap))
	var potentialCredit, potentialEnergy, claimableCredit, claimableEnergy int64
	for _, t := range taskMap {
		tasks = append(tasks, t)
		if !t.Claimed {
			potentialCredit += t.Credit
			potentialEnergy += t.Energy
		}
		if t.Claimable && !t.Claimed {
			claimableCredit += t.Credit
			claimableEnergy += t.Energy
		}
	}

	// 2. 检测官方限时故障补偿
	comp, _ := client.FetchCompensationStatus(cred)

	writeJSON(w, DomesticTasksResponse{
		Tasks:              tasks,
		Items:              tasks,
		PotentialCredit:    potentialCredit,
		TotalCreditPending: potentialCredit,
		PotentialEnergy:    potentialEnergy,
		TotalEnergyPending: potentialEnergy,
		ClaimableCredit:    claimableCredit,
		ClaimableEnergy:    claimableEnergy,
		Compensation:       comp,
	})
}

// handleGrowthDomesticCompensation 查询官方故障补偿状态。
func (s *Server) handleGrowthDomesticCompensation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	p := s.probe()
	cred, err := s.domesticCred(p)
	if err != nil || cred.Token == "" {
		writeJSON(w, map[string]any{"hasPending": false})
		return
	}
	client := growth.NewClient(s.domesticEndpoint())
	comp, err := client.FetchCompensationStatus(cred)
	if err != nil || comp == nil || !comp.Pending() {
		writeJSON(w, map[string]any{"hasPending": false})
		return
	}
	writeJSON(w, map[string]any{
		"hasPending": true,
		"rewards":    fmt.Sprintf("+%d 积分", comp.CreditNum),
		"desc":       fmt.Sprintf("官方服务异常补偿礼包（有效截止至：%s）", comp.EndTime),
	})
}

// handleGrowthDomesticRun 单任务自动化触发执行与领奖闭环。
func (s *Server) handleGrowthDomesticRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	var req struct {
		TaskCode      string `json:"task_code"`
		Code          string `json:"code"`
		TaskCodeCamel string `json:"taskCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求解析失败")
		return
	}
	taskCode := req.TaskCode
	if taskCode == "" {
		taskCode = req.Code
	}
	if taskCode == "" {
		taskCode = req.TaskCodeCamel
	}
	if taskCode == "" {
		writeErr(w, http.StatusBadRequest, "请求缺少 taskCode 参数")
		return
	}

	p := s.probe()
	cred, err := s.domesticCred(p)
	if err != nil || cred.Token == "" {
		writeErr(w, http.StatusNotFound, "未检测到国内侧有效凭证")
		return
	}

	client := growth.NewClient(s.domesticEndpoint())
	res, err := client.RunTask(cred, req.TaskCode)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	if res.Claimed && (res.CreditGranted > 0 || res.EnergyGranted > 0) {
		s.note(ProxyLogEntry{
			Level: "info",
			Text:  fmt.Sprintf("成长任务「%s」自动达成并入账：+%d 积分, +%d 能量！", res.Title, res.CreditGranted, res.EnergyGranted),
		})
	}

	writeJSON(w, res)
}

// handleGrowthDomesticSweep 一键自动扫荡全部待办任务并领奖。
func (s *Server) handleGrowthDomesticSweep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	p := s.probe()
	cred, err := s.domesticCred(p)
	if err != nil || cred.Token == "" {
		writeErr(w, http.StatusNotFound, "未检测到国内侧有效凭证")
		return
	}

	client := growth.NewClient(s.domesticEndpoint())
	sweepRes, err := client.SweepAll(cred)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "扫荡任务执行失败: "+err.Error())
		return
	}

	if sweepRes.TotalCreditGranted > 0 || sweepRes.TotalEnergyGranted > 0 {
		s.note(ProxyLogEntry{
			Level: "info",
			Text: fmt.Sprintf("成长任务一键扫荡完成！共达成 %d 项任务，累计获得 %d 积分与 %d 能量！",
				sweepRes.TasksCompleted, sweepRes.TotalCreditGranted, sweepRes.TotalEnergyGranted),
		})
	}

	writeJSON(w, sweepRes)
}

// handleGrowthDomesticCompensationClaim 手动/单独领取官方故障补偿。
func (s *Server) handleGrowthDomesticCompensationClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	p := s.probe()
	cred, err := s.domesticCred(p)
	if err != nil || cred.Token == "" {
		writeErr(w, http.StatusNotFound, "未检测到国内侧有效凭证")
		return
	}

	client := growth.NewClient(s.domesticEndpoint())
	granted, err := client.ClaimCompensation(cred)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("官方服务异常补偿已成功领入账号，获得 %d 积分！", granted),
	})

	writeJSON(w, map[string]any{
		"ok":      true,
		"granted": granted,
	})
}
