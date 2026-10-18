package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Account 代表多账号凭据库中保存的一个账号条目。
type Account struct {
	ID               string `json:"id"`
	Side             string `json:"side"`
	UID              string `json:"uid"`
	Nickname         string `json:"nickname"`
	Token            string `json:"token"`
	RefreshToken     string `json:"refreshToken,omitempty"`
	ExpiresAt        int64  `json:"expiresAt,omitempty"`        // 毫秒时间戳
	RefreshExpiresAt int64  `json:"refreshExpiresAt,omitempty"` // 毫秒时间戳
	Domain           string `json:"domain"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
	Active           bool   `json:"active"`
}

// RedactedAccount 供前端展示的安全账号视图，对 Token 进行了脱敏掩码。
type RedactedAccount struct {
	ID               string `json:"id"`
	Side             string `json:"side"`
	UID              string `json:"uid"`
	Nickname         string `json:"nickname"`
	Domain           string `json:"domain"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
	Active           bool   `json:"active"`
	TokenMask        string `json:"tokenMask"`
	HasRefreshToken  bool   `json:"hasRefreshToken"`
	ExpiresAt        int64  `json:"expiresAt,omitempty"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt,omitempty"`
	CooldownSec      int64  `json:"cooldownSec,omitempty"`
}

// Redact 返回用于安全展示的脱敏账号对象。
func (a Account) Redact() RedactedAccount {
	mask := ""
	tok := strings.TrimSpace(a.Token)
	if len(tok) > 12 {
		mask = tok[:6] + "…" + tok[len(tok)-4:]
	} else if len(tok) > 0 {
		mask = "******"
	}
	cooldownSec := int64(0)
	if isCool, rem := GetDefaultFailoverTracker().GetCooldown(a.ID); isCool {
		cooldownSec = int64(rem.Seconds())
	}
	return RedactedAccount{
		ID:               a.ID,
		Side:             a.Side,
		UID:              a.UID,
		Nickname:         a.Nickname,
		Domain:           a.Domain,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
		Active:           a.Active,
		TokenMask:        mask,
		HasRefreshToken:  strings.TrimSpace(a.RefreshToken) != "",
		ExpiresAt:        a.ExpiresAt,
		RefreshExpiresAt: a.RefreshExpiresAt,
		CooldownSec:      cooldownSec,
	}
}

var vaultMu sync.Mutex

func accountsFilePath(side variant.ID) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("accounts_%s.json", side)), nil
}

// ListAccounts 获取指定后端的全部已保存账号。
// 当凭据库尚为空、但官方客户端路径已存在有效凭据时，会自动将其迁入作为首个激活账号。
func ListAccounts(p *variant.Probe, side variant.ID) ([]Account, error) {
	vaultMu.Lock()
	defer vaultMu.Unlock()

	return listAccountsLocked(p, side)
}

func listAccountsLocked(p *variant.Probe, side variant.ID) ([]Account, error) {
	fpath, err := accountsFilePath(side)
	if err != nil {
		return nil, err
	}

	var list []Account
	raw, err := os.ReadFile(fpath)
	if err == nil {
		_ = json.Unmarshal(raw, &list)
	}

	// 零配置平滑迁移：若账号库为空，自动检查官方当前已有凭据
	if len(list) == 0 && p != nil {
		curr, err := Read(p, side)
		if err == nil && curr.Token != "" && (strings.HasPrefix(curr.Token, "eyJ") || len(curr.Token) >= 30) {
			name := curr.Nickname
			if name == "" {
				name = "默认账号"
			}
			initAcc := Account{
				ID:               "acc_default",
				Side:             string(side),
				UID:              curr.UID,
				Nickname:         name,
				Token:            curr.Token,
				RefreshToken:     curr.RefreshToken,
				ExpiresAt:        curr.ExpiresAtMs,
				RefreshExpiresAt: curr.RefreshExpiresAtMs,
				Domain:           curr.Domain,
				CreatedAt:        time.Now().UnixMilli(),
				UpdatedAt:        time.Now().UnixMilli(),
				Active:           true,
			}
			list = append(list, initAcc)
			_ = saveListLocked(side, list)
		}
	}

	return list, nil
}

// SaveAccount 添加或更新一个账号。
// 若 setAsActive 为 true，则会将其设为当前活跃账号，并自动同步写入官方凭据路径。
func SaveAccount(p *variant.Probe, side variant.ID, acc Account, setAsActive bool) (*Account, error) {
	vaultMu.Lock()
	defer vaultMu.Unlock()

	if strings.TrimSpace(acc.Token) == "" {
		return nil, errors.New("账号 Token 不能为空")
	}

	list, err := listAccountsLocked(p, side)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	if acc.ID == "" {
		if acc.UID != "" {
			acc.ID = "acc_" + acc.UID
		} else {
			acc.ID = fmt.Sprintf("acc_%d", now)
		}
	}
	acc.Side = string(side)
	acc.UpdatedAt = now

	// 检查是否已有活跃账号，若当前列表为空，则强制设为 active
	if len(list) == 0 {
		setAsActive = true
	}

	replaced := false
	for i := range list {
		if list[i].ID == acc.ID || (acc.UID != "" && list[i].UID == acc.UID) {
			acc.ID = list[i].ID
			if acc.CreatedAt == 0 {
				acc.CreatedAt = list[i].CreatedAt
			}
			if acc.RefreshToken == "" && list[i].RefreshToken != "" {
				acc.RefreshToken = list[i].RefreshToken
			}
			if acc.ExpiresAt == 0 && list[i].ExpiresAt != 0 {
				acc.ExpiresAt = list[i].ExpiresAt
			}
			if acc.RefreshExpiresAt == 0 && list[i].RefreshExpiresAt != 0 {
				acc.RefreshExpiresAt = list[i].RefreshExpiresAt
			}
			if setAsActive {
				acc.Active = true
			} else {
				acc.Active = list[i].Active
			}
			list[i] = acc
			replaced = true
			break
		}
	}
	if !replaced {
		if acc.CreatedAt == 0 {
			acc.CreatedAt = now
		}
		if setAsActive {
			acc.Active = true
		}
		list = append(list, acc)
	}

	if setAsActive {
		for i := range list {
			if list[i].ID != acc.ID {
				list[i].Active = false
			}
		}
	}

	if err := saveListLocked(side, list); err != nil {
		return nil, err
	}

	// 若激活该账号，原子同步写入官方凭据文件。
	// 安全防护：真实客户端 Token 为长 JWT（通常上千字符），拦截短测试桩或异常数据，防止误登出客户端。
	if setAsActive && p != nil && (strings.HasPrefix(acc.Token, "eyJ") || len(acc.Token) >= 30) {
		cred := Credential{
			Token:              acc.Token,
			RefreshToken:       acc.RefreshToken,
			UID:                acc.UID,
			Nickname:           acc.Nickname,
			Domain:             acc.Domain,
			ExpiresAtMs:        acc.ExpiresAt,
			RefreshExpiresAtMs: acc.RefreshExpiresAt,
		}
		if err := Write(p, side, cred); err != nil {
			return nil, fmt.Errorf("写入官方凭据失败: %w", err)
		}
	}

	return &acc, nil
}

// SwitchAccount 切换指定 ID 的账号为当前活跃账号。
func SwitchAccount(p *variant.Probe, side variant.ID, accountID string) (*Account, error) {
	vaultMu.Lock()
	defer vaultMu.Unlock()

	list, err := listAccountsLocked(p, side)
	if err != nil {
		return nil, err
	}

	var target *Account
	for i := range list {
		if list[i].ID == accountID {
			list[i].Active = true
			target = &list[i]
		} else {
			list[i].Active = false
		}
	}

	if target == nil {
		return nil, fmt.Errorf("未找到指定账号: %s", accountID)
	}

	if err := saveListLocked(side, list); err != nil {
		return nil, err
	}

	// 安全防护：仅当 Token 符合 JWT/正常格式时才写出至官方客户端
	if p != nil && (strings.HasPrefix(target.Token, "eyJ") || len(target.Token) >= 30) {
		cred := Credential{
			Token:              target.Token,
			RefreshToken:       target.RefreshToken,
			UID:                target.UID,
			Nickname:           target.Nickname,
			Domain:             target.Domain,
			ExpiresAtMs:        target.ExpiresAt,
			RefreshExpiresAtMs: target.RefreshExpiresAt,
		}
		if err := Write(p, side, cred); err != nil {
			return nil, fmt.Errorf("切换写出官方凭据失败: %w", err)
		}
	}

	return target, nil
}

// DeleteAccount 从凭据库中移除指定账号。
// 若删除的是当前激活账号，且库中还有其他账号，会自动激活剩余的第一个账号。
func DeleteAccount(p *variant.Probe, side variant.ID, accountID string) error {
	vaultMu.Lock()
	defer vaultMu.Unlock()

	list, err := listAccountsLocked(p, side)
	if err != nil {
		return err
	}

	newList := make([]Account, 0, len(list))
	wasActive := false
	for _, a := range list {
		if a.ID == accountID {
			if a.Active {
				wasActive = true
			}
			continue
		}
		newList = append(newList, a)
	}

	if wasActive && len(newList) > 0 {
		newList[0].Active = true
		if p != nil {
			cred := Credential{
				Token:              newList[0].Token,
				RefreshToken:       newList[0].RefreshToken,
				UID:                newList[0].UID,
				Nickname:           newList[0].Nickname,
				Domain:             newList[0].Domain,
				ExpiresAtMs:        newList[0].ExpiresAt,
				RefreshExpiresAtMs: newList[0].RefreshExpiresAt,
			}
			_ = Write(p, side, cred)
		}
	}

	return saveListLocked(side, newList)
}

func saveListLocked(side variant.ID, list []Account) error {
	fpath, err := accountsFilePath(side)
	if err != nil {
		return err
	}
	config.GuardRealWrite(fpath)
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", fpath, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, fpath)
}
