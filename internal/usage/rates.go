package usage

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/HMuSeaB/wbmux/internal/product"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 消耗倍率（来自安装包内置的产品配置） ----------

// ModelRate 是一个模型的消耗倍率。
type ModelRate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Rate 是解析出来的倍数；解析不了为 0，界面退回显示 RateRaw 原文。
	Rate float64 `json:"rate"`
	// RateRaw 是配置里的原文（如 "x2.00 credits"）。
	RateRaw string `json:"rateRaw"`
}

// creditsRateRe 解析 credits 字段原文，如 "x2.00 credits" → 2.00。
var creditsRateRe = regexp.MustCompile(`x\s*([0-9]+(?:\.[0-9]+)?)`)

// SurveyRates 读两侧产品配置里各模型的消耗倍率。
//
// 来源是安装包内的 product.json（app.asar.unpacked/cli/product.json）。
// 它是打包时的快照，服务端的模型清单后来可能更新过（实测：快照里
// 还是 Hy3，客户端里已经是 Hy4 preview）——所以界面上必须注明
// "以客户端实际为准"。本地不发起任何请求，能拿到多少展示多少。
func SurveyRates(probe *variant.Probe) map[string][]ModelRate {
	out := map[string][]ModelRate{}
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		inst := probe.Detect(id, "")
		if inst.ProductJSON == "" {
			continue // 没装这一侧就没有倍率可读，不是错误
		}
		doc, err := product.Load(inst.ProductJSON)
		if err != nil {
			continue // 读不到倍率不是致命的：限流与消耗两块仍然有用
		}
		list, _ := doc["models"].([]any)
		var rates []ModelRate
		for _, m := range list {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			mid, _ := mm["id"].(string)
			name, _ := mm["name"].(string)
			raw, _ := mm["credits"].(string)
			if raw == "" {
				continue // 没标注倍率的（免费/内部模型）不猜，空着比编一个强
			}
			mr := ModelRate{ID: mid, Name: name, RateRaw: raw}
			if mm := creditsRateRe.FindStringSubmatch(raw); mm != nil {
				mr.Rate, _ = strconv.ParseFloat(mm[1], 64)
			}
			rates = append(rates, mr)
		}
		sort.SliceStable(rates, func(i, j int) bool { return rates[i].Rate > rates[j].Rate })
		out[string(id)] = rates
	}
	return out
}
