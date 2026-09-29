package credential

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// 本文件里的用例默认**跳过**，只有显式设 WBMUX_LIVE_PROBE=1 才跑。
//
// # 为什么必须有一条真链路用例
//
// 本包的密码学全在客户端的原生模块里，单位测试只能验"协议接得对不对"，
// 验不了"算法写对了没有"——AAD 少一个字节、密钥派生哈希错了对象，
// 症状都只是 DECRYPT_FAILED，单测用假应答永远不会暴露。
// 这条用例拿真实客户端跑一次真实信封，是唯一能拦住那类错误的闸门。
//
// 用法：
//
//	WBMUX_LIVE_PROBE=1 go test ./internal/credential/ -run LiveUnseal -v
//
// 它只读凭据文件、只解一次密，不发起任何网络请求，也不消耗积分。

// tokenShape 是明文令牌应当匹配的形态（JWT 与常见 base64url token 都在内）。
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

func TestLiveUnsealRealCredential(t *testing.T) {
	if os.Getenv("WBMUX_LIVE_PROBE") != "1" {
		t.Skip("联调用例：设 WBMUX_LIVE_PROBE=1 才跑")
	}

	p := variant.DefaultProbe()
	ClearCache()

	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		t.Run(string(id), func(t *testing.T) {
			c, err := Resolve(p, id)
			if err != nil {
				// 没装这一侧、或没登录过，都算正常情况。
				if strings.Contains(err.Error(), "没找到") {
					t.Skipf("本机没有%s的登录态：%v", id, err)
				}
				t.Fatalf("Resolve: %v", err)
			}
			if c.Sealed && !c.Unsealed {
				t.Fatalf("%s：信封没被解开", id)
			}
			if len(c.Token) < 32 {
				t.Fatalf("%s：令牌短得不像真的（%d 字符）", id, len(c.Token))
			}
			if !tokenShape.MatchString(c.Token) {
				t.Fatalf("%s：令牌里有非法字符，可能解出了垃圾", id)
			}
			// 只报形态，绝不打印令牌本身。
			t.Logf("%s：ok，令牌 %d 字符，sealed=%v unsealedBy=%s domain=%s",
				id, len(c.Token), c.Sealed, c.UnsealedBy, c.Domain)
		})
	}
}
