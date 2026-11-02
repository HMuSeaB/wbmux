package webui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
)

func TestGatewayExportCSVAndJSON(t *testing.T) {
	s := &Server{}

	// 记录两条测试流水
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ID:               "test-req-001",
		Timestamp:        time.Now(),
		ModelRequested:   "deepseek-v4.1-flash",
		ModelMapped:      "primary-model",
		AccountID:        "acc-123456",
		AccountMask:      "测试账号",
		IsStream:         true,
		StatusCode:       429,
		PromptTokens:     1485538,
		CompletionTokens: 0,
		TotalTokens:      1485538,
		TTFTMs:           0,
		DurationMs:       15031,
		ErrorMsg:         "usage exceeds frequency limit, but don't worry, your usage will reset at 2026-11-02 22:25:05 UTC+8",
	})
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ID:               "test-req-002",
		Timestamp:        time.Now(),
		ModelRequested:   "default-model",
		ModelMapped:      "default-model",
		AccountID:        "acc-123456",
		AccountMask:      "测试账号",
		IsStream:         true,
		StatusCode:       200,
		PromptTokens:     120,
		CompletionTokens: 35,
		TotalTokens:      155,
		TTFTMs:           320,
		DurationMs:       850,
	})

	// 1. 测试 CSV 导出
	reqCSV := httptest.NewRequest(http.MethodGet, "/api/gateway/export?format=csv", nil)
	recCSV := httptest.NewRecorder()
	s.handleGatewayExport(recCSV, reqCSV)

	if recCSV.Code != http.StatusOK {
		t.Fatalf("CSV 导出期望 200，得到 %d", recCSV.Code)
	}
	bodyBytes := recCSV.Body.Bytes()
	// 验证 UTF-8 BOM
	if !bytes.HasPrefix(bodyBytes, []byte("\xef\xbb\xbf")) {
		t.Fatalf("CSV 导出必须携带 UTF-8 BOM 防止 Excel 乱码")
	}
	csvContent := string(bodyBytes)
	if !strings.Contains(csvContent, "请求ID,时间,请求模型,映射模型") {
		t.Fatalf("CSV 未包含正确表头: %s", csvContent)
	}
	if !strings.Contains(csvContent, "deepseek-v4.1-flash") || !strings.Contains(csvContent, "usage exceeds frequency limit") {
		t.Fatalf("CSV 未包含审计流水明细内容")
	}

	// 2. 测试 JSON 导出
	reqJSON := httptest.NewRequest(http.MethodGet, "/api/gateway/export?format=json", nil)
	recJSON := httptest.NewRecorder()
	s.handleGatewayExport(recJSON, reqJSON)

	if recJSON.Code != http.StatusOK {
		t.Fatalf("JSON 导出期望 200，得到 %d", recJSON.Code)
	}
	jsonContent := recJSON.Body.String()
	if !strings.Contains(jsonContent, `"recentRecords"`) || !strings.Contains(jsonContent, `"deepseek-v4.1-flash"`) {
		t.Fatalf("JSON 导出未包含结构化记录: %s", jsonContent)
	}
}
