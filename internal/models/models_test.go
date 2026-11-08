package models

import (
	"testing"
)

func TestResolveAlias(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hy4", "hy4free"},
		{"hy-4", "hy4free"},
		{"hy4free", "hy4free"},
		{"hy3", "hy3"},
		{"混元3", "hy3"},
		{"ds4.1flash", "deepseek-v4.1-flash-sg"},
		{"ds4.1-flash", "deepseek-v4.1-flash-sg"},
		{"ds4.1", "deepseek-v4.1-flash-sg"},
		{"glm5.3flash", "glm-5.3-flash"},
		{"glm-5.3flash", "glm-5.3-flash"},
		{"glm5.3", "glm-5.3"},
		{"glm-5.3", "glm-5.3"},
		{"glm5.2", "glm-5.2"},
		{"glm-5.1", "glm-5.3"}, // 历史兼容映射
		{"minimax", "minimax-m2.5"},
		{"kimi", "kimi-k2.6"},
		{"deepseek-v4-flash", "deepseek-v4.1-flash"},
		{"unknown-model-xyz", "unknown-model-xyz"},
	}

	for _, c := range cases {
		got := ResolveAlias(c.in)
		if got != c.want {
			t.Errorf("ResolveAlias(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModelQueries(t *testing.T) {
	// 1. IsFree
	if !IsFree("hy4free") || !IsFree("hy4") || !IsFree("hy3") {
		t.Errorf("IsFree failed for core free models")
	}
	if IsFree("glm-5.3") || IsFree("deepseek-v4.1-flash-sg") {
		t.Errorf("IsFree false positive for paid models")
	}

	// 2. DefaultCuratedCNIDs
	cnIDs := DefaultCuratedCNIDs()
	if len(cnIDs) < 5 {
		t.Errorf("DefaultCuratedCNIDs too short: %v", cnIDs)
	}
	if !IsCuratedCN("hy4free") || !IsCuratedCN("glm-5.3") || !IsCuratedCN("minimax-m2.5") {
		t.Errorf("IsCuratedCN failed on expected CN curated models")
	}

	// 3. ContextWindowFor
	if ContextWindowFor("hy4free") != 1000000 {
		t.Errorf("ContextWindowFor hy4free want 1M, got %d", ContextWindowFor("hy4free"))
	}
	if ContextWindowFor("hy3") != 192000 {
		t.Errorf("ContextWindowFor hy3 want 192k, got %d", ContextWindowFor("hy3"))
	}
	if ContextWindowFor("minimax-m2.5") != 256000 {
		t.Errorf("ContextWindowFor minimax want 256k, got %d", ContextWindowFor("minimax-m2.5"))
	}

	// 4. Core Lists
	freeList := CoreFreeList()
	if len(freeList) != 2 {
		t.Errorf("CoreFreeList want 2 models (hy4free, hy3), got %d", len(freeList))
	}
	intlPaid := CoreIntlPaidList()
	if len(intlPaid) < 3 {
		t.Errorf("CoreIntlPaidList want >= 3 models, got %d", len(intlPaid))
	}
	cnList := CoreCNList()
	if len(cnList) < 5 {
		t.Errorf("CoreCNList want >= 5 models, got %d", len(cnList))
	}
	exCN := ExclusiveCNList()
	if len(exCN) < 2 {
		t.Errorf("ExclusiveCNList want >= 2 models (minimax, kimi), got %d", len(exCN))
	}
}
