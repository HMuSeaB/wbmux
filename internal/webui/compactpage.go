package webui

// 压缩阈值设置页。
//
// # 为什么参数在服务端而不在前端
//
// 界面上给人看的是"300k / 1M"这类友好档位，但落到系统里的是**环境变量**
// （CODEBUDDY_AUTO_COMPACT_WINDOW）。这个映射属于事实性知识，写在前端就会
// 和文档、脚本各写一份，迟早分叉。放在这里一处定义，界面只负责显示。
//
// # 为什么这页要单独做风险提示
//
// 它**写的是客户端的环境变量**，会改变模型的记忆行为：
// 基准设大 → 压缩更晚 → 记得更久，但每轮请求更大更慢。
// 这是"用户该自己拍板"的事，界面必须把代价摆在按钮旁边，
// 不能做成一个看起来无害的开关。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// compactEnvName 是客户端读的那个环境变量。
//
// 取值必须写**纯数字**：CLI 走 parsePositiveInteger，内部是 Number(t)，
// 写 "1m" / "300k" 会变成 NaN 被**静默丢弃**（不报错，就是不生效）。
const compactEnvName = "CODEBUDDY_AUTO_COMPACT_WINDOW"

// 合法范围来自客户端源码里的 clamp：getAutoCompactWindow 把值夹到 [1e5, 1e6]。
// 界面上先拦一道，免得用户输个 50000 却以为生效了（实际会被悄悄抬到 100000）。
const (
	compactMin = 100000
	compactMax = 1000000
)

// compactDefault 是客户端内置的基准窗口（源码常量 ra = 2e5）。
// 环境变量没设时就是这个值。
const compactDefault = 200000

// CompactLevel 是一个可选档位，给界面渲染成按钮。
type CompactLevel struct {
	Key   string `json:"key"`   // 机器用的标识
	Label string `json:"label"` // 按钮上的字
	Value int    `json:"value"` // 对应的数字；0 表示"清掉变量、回默认"
	Desc  string `json:"desc"`  // 一句话说明取舍
	// Recommended 只影响界面上的角标，不参与任何逻辑。
	Recommended bool `json:"recommended,omitempty"`
}

// CompactState 是这一页的全部状态。
type CompactState struct {
	// EnvName 让界面能把"到底改了什么"如实说出来。
	EnvName string `json:"envName"`
	// CurrentValue 是环境变量当前的值；0 表示未设置（走内置默认）。
	CurrentValue int `json:"currentValue"`
	// Set 表示环境变量确实被设过（用来区分"默认 200k"与"手动设成 200k"）。
	Set bool `json:"set"`
	// Effective 是实际生效的基准：设过就用设的，没设就是内置默认。
	Effective int `json:"effective"`
	// TriggerAtPreMessage 是"发消息前压缩"的大致触发点。
	//
	// 为什么值得算出来给用户看：产品配置里阈值全是**比例**
	// （inputTokens.preMessage = 0.5），用户很难自己把"基准"换算成
	// "我聊到多少字会被压"。直接把结果摆出来最省事。
	TriggerAtPreMessage int `json:"triggerAtPreMessage"`

	Min     int            `json:"min"`
	Max     int            `json:"max"`
	Default int            `json:"default"`
	Levels  []CompactLevel `json:"levels"`

	// Available 说明这个环境在当前机器上是否可用。
	// 拿不到客户端安装位置之类的情况要如实说，而不是给个假按钮。
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}

// compactPreMessageRatio 对应产品配置里的 inputTokens.preMessage。
//
// 写死在这里是因为它来自客户端的 product.json，不是我们可控的配置；
// 若哪天官方改了，这里显示的估算就会偏，但不影响实际行为。
const compactPreMessageRatio = 0.5

// envReader / envWriter 是可替换的读写入口。
//
// 为什么要留这层间接：真实实现读写的是 **HKCU 注册表**，测试若直接用它，
// 就会去动开发机上真实的用户环境变量 —— 那正是"测试不许碰真实用户状态"
// 那条规矩要防的事（而且会留下垃圾）。
// 测试里换成内存实现，既隔离又跑得快。
var (
	envReader = readUserEnv
	envWriter = setUserEnv
)

// readCompactState 读当前状态。
//
// # 为什么读注册表而不是 os.Getenv
//
// 这是实测踩出来的：写操作改的是**用户级注册表**（为了跨进程、跨重启生效），
// 而 os.Getenv 只反映**本进程启动时**拿到的环境块。
// 于是点完按钮，值明明写进去了，读回来还是旧的 —— 界面看着"没生效"。
//
// **注册表才是真相**，进程内的环境变量只是它在启动那一刻的快照。
func readCompactState() CompactState {
	raw := strings.TrimSpace(envReader(compactEnvName))

	st := CompactState{
		EnvName: compactEnvName,
		Min:     compactMin,
		Max:     compactMax,
		Default: compactDefault,
		Levels: []CompactLevel{
			{Key: "300k", Label: "300k（折中）", Value: 300000, Recommended: true,
				Desc: "比默认宽松一半，也照顾窗口较小的模型。推荐从这个开始。"},
			{Key: "1M", Label: "1M（最长记忆）", Value: 1000000,
				Desc: "最贴近 1M 窗口模型的上限。记得最久，但每轮请求最大最慢。"},
			{Key: "default", Label: "默认（200k）", Value: 0,
				Desc: "清掉设置，回到客户端内置值。最省流量，但忘事最早。"},
		},
		Available: true,
	}

	if raw == "" {
		st.Effective = compactDefault
		st.TriggerAtPreMessage = int(float64(compactDefault) * compactPreMessageRatio)
		return st
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		// 值存在但读不懂：如实报告，别假装它是默认。
		// 用户看到"设过但无效"才会去查，而不是被一句"200k"蒙过去。
		st.Set = true
		st.Note = "环境变量 " + compactEnvName + " 的值是 " + raw +
			"，不是合法正整数，客户端会忽略它（回落到默认）。"
		st.Effective = compactDefault
		st.TriggerAtPreMessage = int(float64(compactDefault) * compactPreMessageRatio)
		return st
	}

	st.Set = true
	st.CurrentValue = n
	// 超范围时，客户端会 clamp —— 这里也照 clamp 后的值显示，
	// 免得用户以为设的 50000 生效了。
	eff := n
	if eff < compactMin {
		eff = compactMin
	}
	if eff > compactMax {
		eff = compactMax
	}
	if eff != n {
		st.Note = "这个值超出客户端接受的范围（" +
			strconv.Itoa(compactMin) + "~" + strconv.Itoa(compactMax) +
			"），实际会按 " + strconv.Itoa(eff) + " 生效。"
	}
	st.Effective = eff
	st.TriggerAtPreMessage = int(float64(eff) * compactPreMessageRatio)
	return st
}

// handleCompactSetting 读/写压缩基准窗口。
//
// 写操作只改**用户级环境变量**，不改客户端任何文件、不动注册表别处。
// 与自动清理那套一样：它改的是"我们自己的设置"，不碰账号，所以不需要点两下。
func (s *Server) handleCompactSetting(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 只读。
	case http.MethodPost:
		var req struct {
			// Value 0 表示"清掉、回默认"。
			Value int `json:"value"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体无法解析")
			return
		}

		v := req.Value
		// 允许 0（回默认）；非 0 则必须在客户端接受的范围内。
		// 越界的值客户端会**静默 clamp**，那种"设了但没用"最难查，
		// 所以这里直接拒绝，并说清合法区间。
		if v != 0 && (v < compactMin || v > compactMax) {
			writeErr(w, http.StatusBadRequest,
				"值要在 "+strconv.Itoa(compactMin)+" ~ "+strconv.Itoa(compactMax)+
					" 之间；填 0 表示恢复默认")
			return
		}

		// 只设进程内的那份还不够——环境变量是**进程启动时**读的，
		// 必须写进用户级注册表，客户端下次启动才认。
		var err error
		if v == 0 {
			err = envWriter(compactEnvName, "")
			s.logf("压缩基准：已清除（回到默认 %d）", compactDefault)
		} else {
			err = envWriter(compactEnvName, strconv.Itoa(v))
			s.logf("压缩基准：已设为 %d", v)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "写入用户环境变量失败："+err.Error())
			return
		}

	default:
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
		return
	}
	writeJSON(w, readCompactState())
}
