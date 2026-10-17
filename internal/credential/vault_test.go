package credential

import (
	"testing"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestVaultMultiAccountWorkflow(t *testing.T) {
	p := newEnv(t)
	restore := config.SetRoot(t.TempDir())
	defer restore()

	side := variant.Intl

	// 1. 初始列表为空
	list, err := ListAccounts(p, side)
	if err != nil {
		t.Fatalf("ListAccounts 失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("预期空列表，得到 %d", len(list))
	}

	// 2. 添加账号 1 (主账号)
	acc1 := Account{
		UID:      "user_alpha",
		Nickname: "主账号Alpha",
		Token:    "eyJhbGciOi-alpha-token-12345678",
	}
	saved1, err := SaveAccount(p, side, acc1, true)
	if err != nil {
		t.Fatalf("SaveAccount 1 失败: %v", err)
	}
	if !saved1.Active {
		t.Error("首个账号应当为 Active")
	}

	// 验证官方凭据路径也被同步写入了
	cred1, err := Read(p, side)
	if err != nil {
		t.Fatalf("读取官方凭据失败: %v", err)
	}
	if cred1.Token != acc1.Token {
		t.Errorf("官方凭据未同步，预期 %q 得到 %q", acc1.Token, cred1.Token)
	}

	// 3. 添加账号 2 (备用账号，非激活)
	acc2 := Account{
		UID:      "user_beta",
		Nickname: "小号Beta",
		Token:    "eyJhbGciOi-beta-token-87654321",
	}
	saved2, err := SaveAccount(p, side, acc2, false)
	if err != nil {
		t.Fatalf("SaveAccount 2 失败: %v", err)
	}
	if saved2.Active {
		t.Error("账号 2 不应当为 Active")
	}

	// 4. 列出账号，校验数量与脱敏
	all, err := ListAccounts(p, side)
	if err != nil {
		t.Fatalf("ListAccounts 失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("预期 2 个账号，得到 %d", len(all))
	}

	redacted := all[0].Redact()
	if redacted.TokenMask == "" || redacted.TokenMask == all[0].Token {
		t.Errorf("脱敏失败: %q", redacted.TokenMask)
	}

	// 5. 切换到账号 2
	switched, err := SwitchAccount(p, side, saved2.ID)
	if err != nil {
		t.Fatalf("SwitchAccount 失败: %v", err)
	}
	if !switched.Active {
		t.Error("切换后账号 2 应当为 Active")
	}

	// 验证官方凭据路径已同步刷成账号 2
	cred2, err := Read(p, side)
	if err != nil {
		t.Fatalf("读取官方凭据失败: %v", err)
	}
	if cred2.Token != acc2.Token {
		t.Errorf("切换后官方凭据未同步成账号 2，预期 %q 得到 %q", acc2.Token, cred2.Token)
	}

	// 6. 删除当前激活的账号 2，应该自动回退激活剩余的账号 1
	if err := DeleteAccount(p, side, saved2.ID); err != nil {
		t.Fatalf("DeleteAccount 失败: %v", err)
	}
	remaining, err := ListAccounts(p, side)
	if err != nil {
		t.Fatalf("ListAccounts 失败: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("预期剩余 1 个账号，得到 %d", len(remaining))
	}
	if !remaining[0].Active {
		t.Error("剩余账号 1 应当被自动激活")
	}

	// 验证官方凭据路径自动还原成账号 1
	credRestored, err := Read(p, side)
	if err != nil {
		t.Fatalf("读取还原后官方凭据失败: %v", err)
	}
	if credRestored.Token != acc1.Token {
		t.Errorf("还原后官方凭据未同步回账号 1，得到 %q", credRestored.Token)
	}
}
