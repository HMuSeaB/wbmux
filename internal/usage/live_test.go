package usage

import (
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// TestLiveAccountsCached 只读缓存，绝不发网络请求：
// 空缓存返回空 map；有快照原样返回。
func TestLiveAccountsCached(t *testing.T) {
	// 先触发一次 Once（磁盘快照加载），再换掉全局 map，
	// 否则 Once 会在断言中途把磁盘数据写进刚换好的空 map。
	_ = LiveAccountsCached()

	liveCache.Lock()
	savedData := liveCache.data
	savedAt := liveCache.at
	liveCache.data = map[variant.ID]*LiveAccount{}
	liveCache.at = map[variant.ID]time.Time{}
	liveCache.Unlock()
	defer func() {
		liveCache.Lock()
		liveCache.data = savedData
		liveCache.at = savedAt
		liveCache.Unlock()
	}()

	if got := LiveAccountsCached(); len(got) != 0 {
		t.Fatalf("空缓存应返回空 map，得到 %+v", got)
	}

	liveCache.Lock()
	liveCache.data[variant.CN] = &LiveAccount{
		OK:       true,
		Packages: []LivePackage{{Kind: "subscription", Total: 100, Remain: 62.5}},
	}
	liveCache.Unlock()

	got := LiveAccountsCached()
	acc, ok := got["cn"]
	if !ok || acc == nil || !acc.OK || len(acc.Packages) != 1 || acc.Packages[0].Remain != 62.5 {
		t.Fatalf("快照里的数据应原样返回，得到 %+v", got)
	}
}
