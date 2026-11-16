package zcode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestBackupDB(t *testing.T) {
	tmp := t.TempDir()
	probe := &variant.Probe{Home: tmp}

	// 1. 数据库不存在时，应该返回错误
	if _, err := BackupDB(probe); err == nil {
		t.Error("数据库不存在时期望报错，但返回了 nil")
	}

	// 2. 正常数据库备份
	dbDir := filepath.Join(tmp, ".zcode", "cli", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	dbFile := filepath.Join(dbDir, "db.sqlite")
	content := []byte("fake sqlite database header")
	if err := os.WriteFile(dbFile, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	backupPath, err := BackupDB(probe)
	if err != nil {
		t.Fatalf("BackupDB: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Errorf("备份文件不存在：%s", backupPath)
	}
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("备份内容不一致，期望 %q，得到 %q", content, got)
	}
}

func TestDeleteSessionValidation(t *testing.T) {
	tmp := t.TempDir()
	probe := &variant.Probe{Home: tmp}

	// 1. 空 ID 报错
	if _, err := DeleteSession(probe, ""); err == nil {
		t.Error("空 ID 期望报错")
	}

	// 2. 库不存在报错
	if _, err := DeleteSession(probe, "sess_123"); err == nil {
		t.Error("ZCode 库不存在时期望报错")
	}
}
