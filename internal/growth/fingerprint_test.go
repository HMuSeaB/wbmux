package growth

import (
	"net/http"
	"strings"
	"testing"
)

func TestDeriveDeviceID(t *testing.T) {
	id1 := deriveDeviceID("user-123456", "machine")
	id2 := deriveDeviceID("user-123456", "machine")
	if id1 != id2 {
		t.Fatalf("deriveDeviceID should be deterministic, got %s != %s", id1, id2)
	}
	if len(id1) != 36 {
		t.Fatalf("expected 36 hex chars, got %d (%s)", len(id1), id1)
	}

	idSalt := deriveDeviceID("user-123456", "session")
	if id1 == idSalt {
		t.Fatalf("different salt should produce different IDs")
	}
}

func TestDesktopFingerprintAndHeaders(t *testing.T) {
	fp := DesktopFingerprint("uid-test", "Tester")
	if fp["ideName"] != "WorkBuddy" || fp["extName"] != "workbuddy-desktop" {
		t.Fatalf("unexpected desktop fingerprint: %+v", fp)
	}
	if fp["userId"] != "uid-test" {
		t.Fatalf("expected userId uid-test, got %v", fp["userId"])
	}

	events := BuildDesktopEvents("uid-test", "Tester", map[string]any{
		"eventCode": "custom_event",
		"customKey": "customVal",
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev["eventCode"] != "custom_event" || ev["customKey"] != "customVal" {
		t.Fatalf("business field missing in event: %+v", ev)
	}
	if ev["ideName"] != "WorkBuddy" {
		t.Fatalf("common fingerprint missing in event: %+v", ev)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://localhost", nil)
	ApplyDesktopHeaders(req, "test-token", "uid-test", "https://copilot.tencent.com")
	if req.Header.Get("User-Agent") != DesktopUA {
		t.Fatalf("unexpected UA: %s", req.Header.Get("User-Agent"))
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
		t.Fatalf("missing authorization header")
	}
	if req.Header.Get("X-Product") != "SaaS" {
		t.Fatalf("missing X-Product SaaS")
	}
}

func TestApplyWebAndMPHeaders(t *testing.T) {
	reqWeb, _ := http.NewRequest(http.MethodPost, "http://localhost", nil)
	ApplyWebHeaders(reqWeb, "token1", "u1", "https://www.workbuddy.cn", "https://www.workbuddy.cn/space")
	if reqWeb.Header.Get("x-client-platform") != "web" {
		t.Fatalf("web platform header missing")
	}
	if reqWeb.Header.Get("Referer") != "https://www.workbuddy.cn/space" {
		t.Fatalf("referer header missing")
	}

	reqMP, _ := http.NewRequest(http.MethodPost, "http://localhost", nil)
	ApplyMPHeaders(reqMP, "token2", "u2")
	if reqMP.Header.Get("X-Client-Platform") != "miniprogram" {
		t.Fatalf("mp platform header missing")
	}
}
