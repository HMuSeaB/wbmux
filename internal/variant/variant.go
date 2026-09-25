// Package variant 定义 WorkBuddy 的两套后端档位（国内版 / 国际版），
// 并负责在本机探测对应的安装与数据目录。
//
// 本包只保存事实性的接入信息：地址、目录名、认证标识与域名白名单。
// 不包含任何官方配置文件的复制品——真正的 product.json 始终在运行时
// 从用户自己的安装目录读取，见 internal/product。
package variant

import (
	"errors"
	"fmt"
	"strings"
)

// ID 是后端档位的标识。
type ID string

const (
	// CN 国内版后端。
	CN ID = "cn"
	// Intl 国际版后端。
	Intl ID = "intl"
)

// ErrUnknownID 表示传入了无法识别的档位名。
var ErrUnknownID = errors.New("variant: 未知档位")

// Domains 是认证流程中允许的域名集合。
// 客户端按域名把登录目标归类为内部域 / 外部域 / iOA 域 / 云托管域，
// 切换后端时必须一并改写，否则登录会被导向原后端。
type Domains struct {
	Internal    []string `json:"internalDomain"`
	External    []string `json:"externalDomain"`
	IOA         []string `json:"iOADomain"`
	CloudHosted []string `json:"cloudHostedDomain"`
}

// Backend 描述一套后端的完整接入信息。
type Backend struct {
	ID                ID       `json:"id"`
	DisplayName       string   `json:"displayName"`
	ProductName       string   `json:"productName"`
	Endpoint          string   `json:"endpoint"`
	StagingEndpoint   string   `json:"stagingEndpoint"`
	OfficialEndpoints []string `json:"officialEndpoints"`
	DataFolderName    string   `json:"dataFolderName"`
	Oversea           bool     `json:"oversea"`
	AuthID            string   `json:"authID"`
	AuthLabel         string   `json:"authLabel"`
	AuthPlatform      string   `json:"authPlatform"`
	Domains           Domains  `json:"domains"`

	// 以下字段用于在本机定位对应的安装，不参与配置补丁。
	WinExecutableName   string `json:"winExecutableName"`
	MacAppName          string `json:"macAppName"`
	MacBundleID         string `json:"macBundleID"`
	LinuxExecutableName string `json:"linuxExecutableName"`
	URLProtocol         string `json:"urlProtocol"`
}

var backends = map[ID]Backend{
	CN: {
		ID:              CN,
		DisplayName:     "国内版",
		ProductName:     "WorkBuddy",
		Endpoint:        "https://www.workbuddy.cn",
		StagingEndpoint: "https://staging.workbuddy.cn",
		OfficialEndpoints: []string{
			"https://www.workbuddy.cn",
			"https://staging.workbuddy.cn",
			"https://wb.tencentbuddy.com",
			"https://copilot.tencent.com",
			"https://staging-copilot.tencent.com",
			"https://www.codebuddy.ai",
			"https://staging-codebuddy.tencent.com",
		},
		DataFolderName: ".workbuddy",
		Oversea:        false,
		AuthID:         "workbuddy-desktop",
		AuthLabel:      "TencentCloud",
		AuthPlatform:   "workbuddy",
		Domains: Domains{
			Internal: []string{
				"copilot.tencent.com",
				"staging-copilot.tencent.com",
				"www.codebuddy.cn",
				"staging.codebuddy.cn",
				"www.workbuddy.cn",
				"staging.workbuddy.cn",
			},
			External: []string{
				"www.codebuddy.ai",
				"staging-codebuddy.tencent.com",
			},
			IOA: []string{
				"tencent.sso.copilot.tencent.com",
				"tencent.sso.copilot-staging.tencent.com",
				"tencent.sso.codebuddy.cn",
				"tencent.staging-sso.codebuddy.cn",
			},
			CloudHosted: []string{
				"*.sso.copilot.tencent.com",
				"*.sso.copilot-staging.tencent.com",
				"*.copilot.qq.com",
				"*.copilot-staging.qq.com",
				"*.sso.codebuddy.cn",
				"*.staging-sso.codebuddy.cn",
			},
		},
		WinExecutableName:   "WorkBuddy",
		MacAppName:          "WorkBuddy.app",
		MacBundleID:         "com.tencent.workbuddy.mac",
		LinuxExecutableName: "workbuddy",
		URLProtocol:         "workbuddy",
	},
	Intl: {
		ID:              Intl,
		DisplayName:     "国际版",
		ProductName:     "WorkBuddy AI",
		Endpoint:        "https://www.workbuddy.ai",
		StagingEndpoint: "https://staging-codebuddy.tencent.com",
		OfficialEndpoints: []string{
			"https://copilot.tencent.com",
			"https://staging-copilot.tencent.com",
			"https://www.codebuddy.ai",
			"https://staging-codebuddy.tencent.com",
		},
		DataFolderName: ".workbuddy-ai",
		Oversea:        true,
		AuthID:         "workbuddy-desktop-ai",
		AuthLabel:      "TencentCloud",
		AuthPlatform:   "workbuddy-ai",
		Domains: Domains{
			Internal: []string{
				"copilot.tencent.com",
				"staging-copilot.tencent.com",
				"www.codebuddy.cn",
			},
			External: []string{
				"www.codebuddy.ai",
				"staging-codebuddy.tencent.com",
				"www.workbuddy.ai",
				"staging.workbuddy.ai",
			},
			IOA: []string{
				"tencent.sso.copilot.tencent.com",
				"tencent.sso.copilot-staging.tencent.com",
				"tencent.sso.codebuddy.cn",
				"tencent.staging-sso.codebuddy.cn",
			},
			CloudHosted: []string{
				"*.sso.copilot.tencent.com",
				"*.sso.copilot-staging.tencent.com",
				"*.copilot.qq.com",
				"*.copilot-staging.qq.com",
				"*.sso.codebuddy.cn",
				"*.staging-sso.codebuddy.cn",
			},
		},
		WinExecutableName:   "WorkBuddyAI",
		MacAppName:          "WorkBuddy AI.app",
		MacBundleID:         "com.workbuddy.workbuddy-ai",
		LinuxExecutableName: "workbuddy-ai",
		URLProtocol:         "workbuddy-ai",
	},
}

// All 返回全部档位，顺序固定为国内版在前。
func All() []Backend {
	return []Backend{backends[CN], backends[Intl]}
}

// Get 按 ID 取档位描述。
func Get(id ID) (Backend, error) {
	b, ok := backends[id]
	if !ok {
		return Backend{}, fmt.Errorf("%w: %q", ErrUnknownID, id)
	}
	return b, nil
}

// aliases 让命令行输入宽容一些。
var aliases = map[string]ID{
	"cn": CN, "china": CN, "cn-zh": CN, "zh": CN,
	"国内": CN, "国内版": CN, "国服": CN,
	"intl": Intl, "international": Intl, "global": Intl, "ai": Intl,
	"国际": Intl, "国际版": Intl, "海外": Intl, "海外版": Intl,
}

// Parse 把用户输入解析为档位 ID，大小写与首尾空白不敏感。
func Parse(s string) (ID, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if key == "" {
		return "", fmt.Errorf("%w: 空字符串", ErrUnknownID)
	}
	if id, ok := aliases[key]; ok {
		return id, nil
	}
	if _, ok := backends[ID(key)]; ok {
		return ID(key), nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownID, s)
}

// Other 返回另一套档位，用于"从当前切到对面"这类操作。
func (id ID) Other() (ID, error) {
	switch id {
	case CN:
		return Intl, nil
	case Intl:
		return CN, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownID, id)
	}
}

// String 实现 fmt.Stringer。
func (id ID) String() string { return string(id) }
