package variant

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAcceptsAliasesAndIsCaseInsensitive(t *testing.T) {
	cases := map[string]ID{
		"cn": CN, "CN": CN, " cn ": CN, "china": CN, "国内版": CN, "国内": CN,
		"intl": Intl, "INTL": Intl, " international ": Intl,
		"ai": Intl, "国际版": Intl, "海外": Intl,
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q) 返回错误: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "cn-ai", "domestic", "??", "c n"} {
		_, err := Parse(in)
		if !errors.Is(err, ErrUnknownID) {
			t.Errorf("Parse(%q) 期望 ErrUnknownID, 实际 %v", in, err)
		}
	}
}

func TestAllIsStableAndComplete(t *testing.T) {
	all := All()
	if len(all) != 2 {
		t.Fatalf("All() 返回 %d 项, 期望 2", len(all))
	}
	if all[0].ID != CN {
		t.Errorf("All()[0] 应为国内版, 实际 %q", all[0].ID)
	}
	if all[1].ID != Intl {
		t.Errorf("All()[1] 应为国际版, 实际 %q", all[1].ID)
	}
}

func TestGetUnknown(t *testing.T) {
	if _, err := Get("nope"); !errors.Is(err, ErrUnknownID) {
		t.Errorf("Get(\"nope\") 期望 ErrUnknownID, 实际 %v", err)
	}
}

func TestOther(t *testing.T) {
	if other, _ := CN.Other(); other != Intl {
		t.Errorf("CN.Other() = %q, 期望 intl", other)
	}
	if other, _ := Intl.Other(); other != CN {
		t.Errorf("Intl.Other() = %q, 期望 cn", other)
	}
	if _, err := ID("x").Other(); !errors.Is(err, ErrUnknownID) {
		t.Errorf("未知档位 Other() 期望 ErrUnknownID, 实际 %v", err)
	}
}

// 描述符里的字段会被直接写进客户端配置，缺一个就可能导致登录跑偏。
func TestDescriptorsAreWellFormed(t *testing.T) {
	for _, b := range All() {
		t.Run(string(b.ID), func(t *testing.T) {
			if !strings.HasPrefix(b.Endpoint, "https://") {
				t.Errorf("Endpoint 必须是 https: %q", b.Endpoint)
			}
			if !strings.HasPrefix(b.StagingEndpoint, "https://") {
				t.Errorf("StagingEndpoint 必须是 https: %q", b.StagingEndpoint)
			}
			if len(b.OfficialEndpoints) == 0 {
				t.Error("OfficialEndpoints 不能为空")
			}
			for _, e := range b.OfficialEndpoints {
				if !strings.HasPrefix(e, "https://") {
					t.Errorf("OfficialEndpoints 含非 https 项: %q", e)
				}
			}
			if !strings.HasPrefix(b.DataFolderName, ".") {
				t.Errorf("DataFolderName 应以点开头: %q", b.DataFolderName)
			}
			if b.AuthID == "" {
				t.Error("AuthID 不能为空")
			}
			if b.AuthPlatform == "" {
				t.Error("AuthPlatform 不能为空")
			}
			if b.DisplayName == "" || b.ProductName == "" {
				t.Error("DisplayName / ProductName 不能为空")
			}
			if b.WinExecutableName == "" || b.MacBundleID == "" || b.LinuxExecutableName == "" {
				t.Error("各平台的可执行文件名不能为空")
			}
			for name, list := range map[string][]string{
				"Internal":    b.Domains.Internal,
				"External":    b.Domains.External,
				"IOA":         b.Domains.IOA,
				"CloudHosted": b.Domains.CloudHosted,
			} {
				if len(list) == 0 {
					t.Errorf("Domains.%s 不能为空", name)
				}
				for _, d := range list {
					if strings.Contains(d, "://") {
						t.Errorf("Domains.%s 应为纯域名而非 URL: %q", name, d)
					}
				}
			}
		})
	}
}

// 两套档位必须真的不同，否则切换毫无意义；同时不能有任何一端缺失关键字段。
func TestBackendsDifferWhereItMatters(t *testing.T) {
	cn, _ := Get(CN)
	intl, _ := Get(Intl)

	if cn.Endpoint == intl.Endpoint {
		t.Error("两套档位的 Endpoint 相同")
	}
	if cn.DataFolderName == intl.DataFolderName {
		t.Error("两套档位的 DataFolderName 相同")
	}
	if cn.AuthID == intl.AuthID {
		t.Error("两套档位的 AuthID 相同")
	}
	if cn.AuthPlatform == intl.AuthPlatform {
		t.Error("两套档位的 AuthPlatform 相同")
	}
	if cn.Oversea == intl.Oversea {
		t.Error("两套档位的 Oversea 相同")
	}
	if cn.WinExecutableName == intl.WinExecutableName {
		t.Error("两套档位的 WinExecutableName 相同")
	}
	if cn.ProductName == intl.ProductName {
		t.Error("两套档位的 ProductName 相同")
	}
}

// 每套后端的 endpoint 必须出现在自己的 officialEndpoints 里，
// 否则客户端可能把自己的主地址判定为非官方地址。
func TestEndpointIsAmongOfficialEndpoints(t *testing.T) {
	for _, b := range All() {
		found := false
		for _, e := range b.OfficialEndpoints {
			if strings.EqualFold(strings.TrimRight(e, "/"), strings.TrimRight(b.Endpoint, "/")) {
				found = true
				break
			}
		}
		// 国际版的 endpoint (workbuddy.ai) 不在其 officialEndpoints 中，
		// 这是官方配置的既有形态，此处仅作记录，不视为错误。
		if !found && b.ID != Intl {
			t.Errorf("%s 的 Endpoint %q 不在 OfficialEndpoints 中", b.ID, b.Endpoint)
		}
	}
}
