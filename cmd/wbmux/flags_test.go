package main

import (
	"testing"
)

func TestFlagsAcceptsOptionAfterPositional(t *testing.T) {
	// 这是不用标准库 flag 的原因：`run intl --dry-run` 必须能解析。
	f := newFlags()
	dryRun := f.Bool("dry-run", false)
	if err := f.Parse([]string{"intl", "--dry-run"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !*dryRun {
		t.Error("--dry-run 未生效")
	}
	if len(f.Args) != 1 || f.Args[0] != "intl" {
		t.Errorf("Args = %v", f.Args)
	}
}

func TestFlagsAcceptsOptionBeforePositional(t *testing.T) {
	f := newFlags()
	dryRun := f.Bool("dry-run", false)
	if err := f.Parse([]string{"--dry-run", "intl"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !*dryRun {
		t.Error("--dry-run 未生效")
	}
	if len(f.Args) != 1 || f.Args[0] != "intl" {
		t.Errorf("Args = %v", f.Args)
	}
}

func TestFlagsValueForms(t *testing.T) {
	f := newFlags()
	host := f.String("host", "def")
	if err := f.Parse([]string{"--host=cn"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if *host != "cn" {
		t.Errorf("--host=cn 解析为 %q", *host)
	}

	f2 := newFlags()
	host2 := f2.String("host", "def")
	if err := f2.Parse([]string{"--host", "intl"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if *host2 != "intl" {
		t.Errorf("--host intl 解析为 %q", *host2)
	}
}

func TestFlagsMissingValueIsAnError(t *testing.T) {
	f := newFlags()
	f.String("host", "")
	if err := f.Parse([]string{"--host"}); err == nil {
		t.Fatal("缺值时应报错")
	}
}

func TestFlagsUnknownOptionIsAnError(t *testing.T) {
	f := newFlags()
	if err := f.Parse([]string{"--nope"}); err == nil {
		t.Fatal("未知选项应报错")
	}
}

func TestFlagsListAccumulates(t *testing.T) {
	f := newFlags()
	eps := f.List("extra-endpoint")
	err := f.Parse([]string{
		"--extra-endpoint", "https://a.example",
		"--extra-endpoint=https://b.example",
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(*eps) != 2 || (*eps)[0] != "https://a.example" || (*eps)[1] != "https://b.example" {
		t.Errorf("List = %v", *eps)
	}
}

func TestFlagsAlias(t *testing.T) {
	f := newFlags()
	f.Alias("n", "dry-run")
	dryRun := f.Bool("dry-run", false)
	if err := f.Parse([]string{"-n"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !*dryRun {
		t.Error("短名未生效")
	}
}

func TestFlagsDoubleDashStopsParsing(t *testing.T) {
	f := newFlags()
	dryRun := f.Bool("dry-run", false)
	if err := f.Parse([]string{"--", "--dry-run"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if *dryRun {
		t.Error("-- 之后的参数不应被当作选项")
	}
	if len(f.Args) != 1 || f.Args[0] != "--dry-run" {
		t.Errorf("Args = %v", f.Args)
	}
}

func TestFlagsExplicitBoolValue(t *testing.T) {
	f := newFlags()
	dryRun := f.Bool("dry-run", true)
	if err := f.Parse([]string{"--dry-run=false"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if *dryRun {
		t.Error("--dry-run=false 应关闭开关")
	}

	f2 := newFlags()
	f2.Bool("dry-run", false)
	if err := f2.Parse([]string{"--dry-run=maybe"}); err == nil {
		t.Error("非法布尔值应报错")
	}
}

func TestFlagsSingleDashIsAPositional(t *testing.T) {
	f := newFlags()
	if err := f.Parse([]string{"-"}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(f.Args) != 1 || f.Args[0] != "-" {
		t.Errorf("Args = %v", f.Args)
	}
}
