package shortstory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/domain"
)

// buildDeliverable 组装一个两场景、已确认草稿的交付底座。
func buildDeliverable(t *testing.T) (*authoring.Store, []Scene) {
	t.Helper()
	auth := authoring.Open(t.TempDir())
	scenes := []Scene{
		{Index: 1, Chapter: 1, POV: "老周", Goal: "接客", Obstacle: "含糊", WordBudget: 2500},
		{Index: 2, Chapter: 2, POV: "老周", Goal: "跟下去", Obstacle: "无路", WordBudget: 2500},
	}
	contents := map[int]string{
		1: "深夜十一点，老周把车停在路口。老周把车停在路口，等那位乘客。",
		2: "码头的废址无路可走。老周收到了一条空短信。",
	}
	for _, scene := range scenes {
		if err := EnsureTarget(auth, SceneTargetID(scene.Index)); err != nil {
			t.Fatal(err)
		}
		candidate, err := ProposeManual(auth, SceneTargetID(scene.Index), contents[scene.Index])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ConfirmTarget(auth, SceneTargetID(scene.Index), candidate.CandidateID, "场景草稿"); err != nil {
			t.Fatal(err)
		}
	}
	return auth, scenes
}

func reviewPayload(summary, quote, severity, issueType string) string {
	if quote == "" {
		return `{"summary":"` + summary + `","issues":[]}`
	}
	return `{"summary":"` + summary + `","issues":[{"type":"` + issueType + `","scene_index":1,"quote":"` + quote + `","severity":"` + severity + `","description":"d"}]}`
}

func runFinalReview(t *testing.T, auth *authoring.Store, scenes []Scene, payload string) *ReviewReport {
	t.Helper()
	head, err := auth.Head(TargetFullText)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ParseReviewResponse(payload, TargetFullText, head, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordReview(auth, report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestFinalizeGatesAndExportConsistency(t *testing.T) {
	auth, scenes := buildDeliverable(t)
	runRoot := filepath.Dir(auth.Dir())
	title := "雾城夜行"

	// 未组装全文：阻断。
	if _, _, err := AssembleFullText(auth, scenes); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeGateCheck(auth, scenes); err == nil || !strings.Contains(err.Error(), "尚未组装") {
		t.Fatalf("未组装未被阻断: %v", err)
	}

	// 组装 + 终审（无 blocker）+ 终稿确认 → 通过。
	fullText, err := CommitFullText(auth, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fullText.Content, "## 第1章") || !strings.Contains(fullText.Content, "## 第2章") {
		t.Fatalf("多章组装缺少章标题: %s", fullText.Content[:80])
	}
	if err := FinalizeGateCheck(auth, scenes); err == nil || !strings.Contains(err.Error(), "缺少终审报告") {
		t.Fatalf("缺少终审未被阻断: %v", err)
	}
	// 带 blocker 的终审 → 阻断。
	runFinalReview(t, auth, scenes, reviewPayload("有矛盾", "老周把车停在路口。", "blocker", "contradiction"))
	if err := FinalizeGateCheck(auth, scenes); err == nil || !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("blocker 未被阻断: %v", err)
	}
	// 无 blocker 终审 → 仍缺作者终稿确认。
	runFinalReview(t, auth, scenes, reviewPayload("通过", "雨刷停在半程", "minor", "style"))
	if err := FinalizeGateCheck(auth, scenes); err == nil || !strings.Contains(err.Error(), "终稿确认") {
		t.Fatalf("缺确认未被阻断: %v", err)
	}
	if _, err := auth.RecordAuthorApproval(TargetFullText, fullText.VersionID, "", "终稿确认", ""); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeGateCheck(auth, scenes); err != nil {
		t.Fatalf("全部通过后仍阻断: %v", err)
	}

	// 导出：清单哈希与文件一致 + 备份存在。
	result, manifest, err := Export(runRoot, title, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FullTextDigest != fullText.ContentDigest {
		t.Fatalf("清单全文摘要不符: %s vs %s", manifest.FullTextDigest, fullText.ContentDigest)
	}
	md, err := os.ReadFile(result.MarkdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if domain.ComputeArcArtifactSHA256(md) != manifest.MarkdownDigest {
		t.Fatal("Markdown 哈希与清单不一致")
	}
	txt, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatal(err)
	}
	if domain.ComputeArcArtifactSHA256(txt) != manifest.TextDigest {
		t.Fatal("TXT 哈希与清单不一致")
	}
	// TXT 去掉章标题标记，保留段落。
	if strings.Contains(string(txt), "## 第1章") || !strings.Contains(string(txt), "第1章") {
		t.Fatalf("纯文本转换错误: %s", string(txt)[:60])
	}
	entries, err := os.ReadDir(result.BackupDir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("备份不完整: %d err=%v", len(entries), err)
	}
}

func TestDeliveryBlockedAfterSceneRework(t *testing.T) {
	auth, scenes := buildDeliverable(t)
	runRoot := filepath.Dir(auth.Dir())

	fullText, err := CommitFullText(auth, scenes)
	if err != nil {
		t.Fatal(err)
	}
	runFinalReview(t, auth, scenes, reviewPayload("通过", "雨刷停在半程", "minor", "style"))
	if _, err := auth.RecordAuthorApproval(TargetFullText, fullText.VersionID, "", "终稿确认", ""); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeGateCheck(auth, scenes); err != nil {
		t.Fatalf("首次交付前置未通过: %v", err)
	}

	// 场景 2 返工（新候选被作者确认）→ 全文版本改变 → 旧终审与旧确认
	// 全部失效：阻断，绝不复用旧哈希审核。
	reworked, err := ProposeManual(auth, SceneTargetID(2), "码头的废址无路可走。老周收到了一条来自未知号码的空短信。")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmTarget(auth, SceneTargetID(2), reworked.CandidateID, "返工"); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeGateCheck(auth, scenes); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("返工后未被阻断: %v", err)
	}

	// 重新组装 + 重新终审 + 重新确认 → 恢复可交付。
	fullText2, err := CommitFullText(auth, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if fullText2.VersionID == fullText.VersionID {
		t.Fatal("返工后全文版本应改变")
	}
	if err := FinalizeGateCheck(auth, scenes); err == nil || !strings.Contains(err.Error(), "缺少终审报告") {
		t.Fatalf("新版本缺少终审未被阻断: %v", err)
	}
	runFinalReview(t, auth, scenes, reviewPayload("通过", "未知号码", "minor", "style"))
	if _, err := auth.RecordAuthorApproval(TargetFullText, fullText2.VersionID, "", "终稿确认 v2", ""); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeGateCheck(auth, scenes); err != nil {
		t.Fatalf("重走终审后仍阻断: %v", err)
	}
	if _, _, err := Export(runRoot, "雾城夜行", scenes); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
}
