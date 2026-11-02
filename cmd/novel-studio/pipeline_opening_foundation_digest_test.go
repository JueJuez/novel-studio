package main

import (
	"os"
	"path/filepath"
	"testing"
)

// P5-02 回归：layered_outline.json 是可选产物（architect_check 把
// outline.json 视为其兼容视图）。开篇产物摘要不得因 layered 缺失而
// 阻塞 --refresh-architect，否则短篇级规划的书无法走任何 targeted
// 修复（catch-22：唯一缺的就是 layered，refresh 又要求它已存在）。
func TestOpeningFoundationDigestTreatsLayeredOutlineAsOptional(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "outline.json"), []byte(`[{"chapter":1,"title":"开篇"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	withoutLayered, err := pipelineOpeningFoundationDigest(base)
	if err != nil {
		t.Fatalf("缺失 layered_outline.json 不应阻塞摘要: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "layered_outline.json"), []byte(`[{"index":1}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	withLayered, err := pipelineOpeningFoundationDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	if withoutLayered == withLayered {
		t.Fatal("layered 内容应参与摘要区分")
	}
	// outline.json 缺失仍是硬错误（它才是必需的开篇产物）。
	if err := os.Remove(filepath.Join(base, "outline.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelineOpeningFoundationDigest(base); err == nil {
		t.Fatal("outline.json 缺失应报错")
	}
}
