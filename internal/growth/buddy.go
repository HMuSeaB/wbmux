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

// BuddyItem 描述用户拥有或佩戴的 Buddy 实体。
type BuddyItem struct {
	InstanceID    int64  `json:"instance_id"`
	Name          string `json:"name"`
	Rarity        string `json:"rarity"`
	SoulDesc      string `json:"soul_desc"`
	ThumbnailURL  string `json:"thumbnail_url"`
	BaseStaticURL string `json:"base_static_url"`
}

// BuddyInfoResponse 包含当前佩戴的猫咪实体信息。
type BuddyInfoResponse struct {
	Buddy *BuddyItem `json:"buddy,omitempty"`
}

// BlindBoxBuddy 描述盲盒开出的单只猫咪结果。
type BlindBoxBuddy struct {
	InstanceID   int64  `json:"instance_id"`
	TemplateID   int64  `json:"template_id"`
	Name         string `json:"name"`
	Rarity       string `json:"rarity"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnail_url"`
	IsShiny      bool   `json:"is_shiny"`
}

// OpenBlindBoxResponse 开启盲盒的批量返回实体。
type OpenBlindBoxResponse struct {
	Count   int             `json:"count"`
	Results []BlindBoxBuddy `json:"results"`
}

// TravelLocation 旅行目的地描述。
type TravelLocation struct {
	ID            int    `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	DurationHours int    `json:"duration_hours"`
}

// TravelStatus 猫猫当前旅行状态实体。
type TravelStatus struct {
	State             string          `json:"state"` // idle, traveling, arrived
	BuddyID           int64           `json:"buddy_id"`
	RecordID          int64           `json:"record_id"`
	Location          *TravelLocation `json:"location,omitempty"`
	DepartAt          int64           `json:"depart_at"`
	ArriveAt          int64           `json:"arrive_at"`
	ServerNow         int64           `json:"server_now"`
	RewardCredit      int64           `json:"reward_credit"`
	DailyLimitReached bool            `json:"daily_limit_reached"`
}

// endpointFor 返回适配本地单测和生产环境的请求前缀。
func (c *Client) endpointFor(path string) string {
	if strings.Contains(c.endpoint, "127.0.0.1") || strings.Contains(c.endpoint, "localhost") {
		return c.endpoint + path
	}
	return "https://www.workbuddy.cn" + path
}

// FetchBuddyInfo 拉取当前用户佩戴的 Buddy 信息。
func (c *Client) FetchBuddyInfo(cred credential.Credential) (*BuddyItem, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	url := c.endpointFor("/activity/growth/buddy/info")
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	ApplyWebHeaders(req, cred.Token, cred.UID, "https://www.workbuddy.cn", "https://www.workbuddy.cn/profile/growth-center")

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
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Buddy *BuddyItem `json:"buddy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return nil, fmt.Errorf("unmarshal buddy info: %w", err)
	}
	if resDoc.Code != 0 {
		return nil, fmt.Errorf("buddy info rejected: %s", resDoc.Msg)
	}

	return resDoc.Data.Buddy, nil
}

// OpenBlindBox 消耗能量开启盲盒（每次消耗 10 能量）。
func (c *Client) OpenBlindBox(cred credential.Credential, count int) (*OpenBlindBoxResponse, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}
	if count <= 0 {
		count = 1
	}

	url := c.endpointFor("/activity/growth/buddy/open")
	payload, _ := json.Marshal(map[string]int{"count": count})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	ApplyWebHeaders(req, cred.Token, cred.UID, "https://www.workbuddy.cn", "https://www.workbuddy.cn/profile/growth-center")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rawDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Count   int `json:"count"`
			Results []struct {
				Instance struct {
					ID         int64 `json:"id"`
					TemplateID int64 `json:"template_id"`
					IsShiny    int   `json:"is_shiny"`
				} `json:"instance"`
				Template struct {
					ID           int64  `json:"id"`
					Name         string `json:"name"`
					Description  string `json:"description"`
					ThumbnailURL string `json:"thumbnail_url"`
					Rarity       string `json:"rarity"`
				} `json:"template"`
			} `json:"results"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &rawDoc); err != nil {
		return nil, fmt.Errorf("unmarshal open blind box: %w", err)
	}
	if rawDoc.Code != 0 {
		return nil, fmt.Errorf("open blind box failed: %s", rawDoc.Msg)
	}

	res := &OpenBlindBoxResponse{
		Count:   rawDoc.Data.Count,
		Results: make([]BlindBoxBuddy, 0, len(rawDoc.Data.Results)),
	}

	for _, r := range rawDoc.Data.Results {
		res.Results = append(res.Results, BlindBoxBuddy{
			InstanceID:   r.Instance.ID,
			TemplateID:   r.Template.ID,
			Name:         r.Template.Name,
			Rarity:       r.Template.Rarity,
			Description:  r.Template.Description,
			ThumbnailURL: r.Template.ThumbnailURL,
			IsShiny:      r.Instance.IsShiny > 0,
		})
	}

	return res, nil
}

// FetchTravelStatus 查询当前猫猫旅行状态。
func (c *Client) FetchTravelStatus(cred credential.Credential) (*TravelStatus, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	url := c.endpointFor("/activity/growth/buddy/travel/status")
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	ApplyWebHeaders(req, cred.Token, cred.UID, "https://www.workbuddy.cn", "https://www.workbuddy.cn/profile/growth-center")

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
		Code int          `json:"code"`
		Msg  string       `json:"msg"`
		Data TravelStatus `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return nil, fmt.Errorf("unmarshal travel status: %w", err)
	}
	if resDoc.Code != 0 {
		return nil, fmt.Errorf("travel status failed: %s", resDoc.Msg)
	}

	return &resDoc.Data, nil
}

// DepartTravel 派遣猫猫前往指定目的地旅行（默认目的地 1: 咖啡馆）。
func (c *Client) DepartTravel(cred credential.Credential, locationID int) (*TravelStatus, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}
	if locationID <= 0 {
		locationID = 1
	}

	url := c.endpointFor("/activity/growth/buddy/travel/depart")
	payload, _ := json.Marshal(map[string]int{"location_id": locationID})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	ApplyWebHeaders(req, cred.Token, cred.UID, "https://www.workbuddy.cn", "https://www.workbuddy.cn/profile/growth-center")

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
		Code int          `json:"code"`
		Msg  string       `json:"msg"`
		Data TravelStatus `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return nil, fmt.Errorf("unmarshal depart travel: %w", err)
	}
	if resDoc.Code != 0 {
		return nil, fmt.Errorf("depart travel failed: %s", resDoc.Msg)
	}

	return &resDoc.Data, nil
}

// ClaimTravelReward 领取猫猫旅行归来寄回的明信片与 Credits 积分奖励。
func (c *Client) ClaimTravelReward(cred credential.Credential) (int64, error) {
	if cred.Token == "" {
		return 0, fmt.Errorf("credential token is empty")
	}

	url := c.endpointFor("/activity/growth/buddy/travel/claim")
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, err
	}
	ApplyWebHeaders(req, cred.Token, cred.UID, "https://www.workbuddy.cn", "https://www.workbuddy.cn/profile/growth-center")

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
			RewardCredit int64 `json:"reward_credit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resDoc); err != nil {
		return 0, fmt.Errorf("unmarshal claim travel: %w", err)
	}
	if resDoc.Code != 0 {
		return 0, fmt.Errorf("claim travel failed: %s", resDoc.Msg)
	}

	return resDoc.Data.RewardCredit, nil
}

// AutoTravelSweep 全自动打理猫猫旅行：
// 若猫咪已旅行到达（arrived），自动领奖并再次派遣；
// 若猫咪闲置（idle），自动派遣出门旅行（产出 5~10 Credits）。
func (c *Client) AutoTravelSweep(cred credential.Credential) (string, error) {
	st, err := c.FetchTravelStatus(cred)
	if err != nil {
		return "", fmt.Errorf("fetch status: %w", err)
	}

	switch st.State {
	case "arrived":
		credit, err := c.ClaimTravelReward(cred)
		if err != nil {
			return "", fmt.Errorf("claim travel reward: %w", err)
		}
		// 领奖后再次派遣
		newSt, err := c.DepartTravel(cred, 1)
		locName := "咖啡馆"
		if newSt != nil && newSt.Location != nil {
			locName = newSt.Location.Name
		}
		if err != nil {
			return fmt.Sprintf("已成功领取旅行奖励 (+%d 积分)，但重新派遣失败：%v", credit, err), nil
		}
		return fmt.Sprintf("已成功收割旅行奖励 (+%d 积分)，猫猫已再次启程前往「%s」！", credit, locName), nil

	case "idle":
		newSt, err := c.DepartTravel(cred, 1)
		if err != nil {
			return "", fmt.Errorf("depart failed: %w", err)
		}
		locName := "咖啡馆"
		if newSt != nil && newSt.Location != nil {
			locName = newSt.Location.Name
		}
		return fmt.Sprintf("猫猫已成功派遣出发前往「%s」旅行，将在归来时带回积分奖励！", locName), nil

	case "traveling":
		locName := "未知地点"
		if st.Location != nil {
			locName = st.Location.Name
		}
		remainingSec := st.ArriveAt - st.ServerNow
		if remainingSec < 0 {
			remainingSec = 0
		}
		return fmt.Sprintf("猫猫正在「%s」旅行途中，预计还需 %d 分钟归来寄回积分礼包。", locName, remainingSec/60), nil

	default:
		return fmt.Sprintf("当前旅行状态：%s", st.State), nil
	}
}
