package shortstory

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
)

func TestImpactPreviewAndStaleness(t *testing.T) {
	auth := authoring.Open(t.TempDir())
	scenes := []Scene{
		{Index: 1, POV: "老周", Goal: "接客", Obstacle: "含糊", WordBudget: 2500},
		{Index: 2, POV: "老周", Goal: "跟下去", Obstacle: "无路", CausalDeps: []int{1}, WordBudget: 2500},
	}
	v1 := seedContent(t, auth, SceneTargetID(1), "深夜十一点，老周把车停在路口。\n\n雨刷停在半程。")

	// 提取事实 → 作者接受一条（绑定 v1）。
	_, refs, err := RunExtraction(context.Background(), reviewFakeModel(`{"facts":["老周深夜在路口停车"],"foreshadows":[]}`),
		auth, testPrompts(), scenes[0], SceneTargetID(1), v1.VersionID, v1.Content)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AcceptSource(SceneTargetID(1), refs[0].SourceID); err != nil {
		t.Fatal(err)
	}
	// 给 v1 加一份审阅报告（接受态）。
	review, err := ParseReviewResponse(`{"summary":"通过","issues":[{"type":"style","scene_index":1,"quote":"雨刷停在半程","severity":"minor","description":"d"}]}`,
		SceneTargetID(1), v1, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordReview(auth, review); err != nil {
		t.Fatal(err)
	}

	// 提议修改（基线=当前 head）。
	candidate, err := ProposeManual(auth, SceneTargetID(1), "深夜十一点，老周把车滑进雨幕。\n\n雨刷停得更低了。")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewAcceptImpact(auth, SceneTargetID(1), candidate.CandidateID, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Blocked {
		t.Fatalf("合法候选不应阻断: %s", preview.BlockReason)
	}
	foundFact := false
	for _, sourceID := range preview.SourcesToStale {
		if sourceID == refs[0].SourceID {
			foundFact = true
		}
	}
	if !foundFact {
		t.Fatalf("影响预览未列出将陈旧的事实来源: %+v", preview.SourcesToStale)
	}
	if preview.ReviewsInvalidated != 1 {
		t.Fatalf("审阅失效数错误: %d", preview.ReviewsInvalidated)
	}
	if !preview.FullTextInvalidated {
		t.Fatal("场景修改应标记全文失效")
	}
	if len(preview.LaterSceneDeps) != 1 || preview.LaterSceneDeps[0] != 2 {
		t.Fatalf("下游依赖场景未识别: %+v", preview.LaterSceneDeps)
	}
	if !preview.SceneReviewRequired {
		t.Fatal("修改后应要求场景重审")
	}

	// 接受当前候选推进 head，然后以旧基线（v1）发起新候选 → 预览阻断。
	if _, err := auth.Accept(SceneTargetID(1), candidate.CandidateID); err != nil {
		t.Fatal(err)
	}
	staleCandidate, err := auth.Propose(SceneTargetID(1), v1.VersionID, "ai", "stale-preview", "基于旧版本的修改。")
	if err != nil {
		t.Fatal(err)
	}
	stalePreview, err := PreviewAcceptImpact(auth, SceneTargetID(1), staleCandidate.CandidateID, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if !stalePreview.Blocked || !strings.Contains(stalePreview.BlockReason, "已落后") {
		t.Fatalf("过期基线预览未阻断: %+v", stalePreview)
	}
	afterAccept, err := auth.StaleSourceReport(SceneTargetID(1))
	if err != nil || len(afterAccept) < 1 || afterAccept[0].Reason != "version_superseded" {
		t.Fatalf("接受后陈旧来源报告不符: %+v err=%v", afterAccept, err)
	}
	foundStaleFact := false
	for _, entry := range afterAccept {
		if entry.SourceID == refs[0].SourceID {
			foundStaleFact = true
		}
	}
	if !foundStaleFact {
		t.Fatalf("陈旧报告未包含事实来源: %+v", afterAccept)
	}
	requires, err := auth.RequiresReview(SceneTargetID(1))
	if err != nil || !requires {
		t.Fatalf("接受后应要求重审: %v err=%v", requires, err)
	}
	// 新版本补审 → RequiresReview=false。
	head, _ := auth.Head(SceneTargetID(1))
	review2, err := ParseReviewResponse(`{"summary":"通过","issues":[]}`, SceneTargetID(1), head, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordReview(auth, review2); err != nil {
		t.Fatal(err)
	}
	requires, err = auth.RequiresReview(SceneTargetID(1))
	if err != nil || requires {
		t.Fatalf("补审后不应再要求重审: %v err=%v", requires, err)
	}
	if !strings.Contains(head.Content, "雨幕") {
		t.Fatalf("head 内容未推进: %s", head.Content)
	}
}
