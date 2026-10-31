package shortstory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
)

// TestP2ExitDemoFullLoopWithRecovery 是计划第 7 节 P2 出口演示：创建短篇
// → 确认结构 → 生成正文 → 定向修改 → 接受候选 → 全文检查 → 作者确认
// → 导出；中段重建全部 store 实例模拟关闭程序后从持久工件恢复。
// 全程 mock 模型，不发起真实调用。
func TestP2ExitDemoFullLoopWithRecovery(t *testing.T) {
	runRoot := t.TempDir()

	// 1. 创建短篇作品：显式选择 short 管线，创意与作者约束落盘。
	manifest, err := project.Create(runRoot, project.CreateOptions{Title: "雾城夜行", PipelineKind: project.PipelineShort})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(runRoot, manifest, Brief{
		Idea:              "深夜出租车司机接到一个去往已拆除码头站的乘客。",
		AuthorConstraints: []string{"开放式结局", "第一人称"},
	}); err != nil {
		t.Fatal(err)
	}
	stateStore, err := Open(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	auth := authoring.Open(runRoot)
	prompts := testPrompts()
	ctx := context.Background()

	// 2. 结构：生成候选 → 确认 → 状态机推进。
	req, err := AssembleStructureRequest(prompts, Brief{Idea: "夜航", AuthorConstraints: []string{"开放式结局"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	structureResult, err := RunGeneration(ctx, draftFakeModel(structureResponseJSON()), req)
	if err != nil {
		t.Fatal(err)
	}
	structure, err := ParseStructureResponse(structureResult.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureTarget(auth, TargetStructure); err != nil {
		t.Fatal(err)
	}
	structureContent, _ := MarshalCanonical(structure)
	structureCandidate, err := ProposeGenerated(auth, TargetStructure, "structure-v1", structureContent)
	if err != nil {
		t.Fatal(err)
	}
	structureVersion, err := ConfirmTarget(auth, TargetStructure, structureCandidate.CandidateID, "结构确认")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stateStore.Advance(StageStructure, map[string]string{
		"version_id": structureVersion.VersionID, "candidate_id": structureCandidate.CandidateID,
	}); err != nil {
		t.Fatal(err)
	}

	// 3. 场景计划：生成 → 校验（预算/DAG/承诺）→ 确认。
	scenesReq, err := AssembleScenesRequest(prompts, structure, "[5000, 30000]")
	if err != nil {
		t.Fatal(err)
	}
	scenesResult, err := RunGeneration(ctx, draftFakeModel(scenesResponseJSON()), scenesReq)
	if err != nil {
		t.Fatal(err)
	}
	scenes, total, err := ParseScenesResponse(scenesResult.Text, structure)
	if err != nil {
		t.Fatal(err)
	}
	if total <= 0 {
		t.Fatalf("字数预算合计异常: %d", total)
	}
	if err := EnsureTarget(auth, TargetScenes); err != nil {
		t.Fatal(err)
	}
	scenesContent, _ := MarshalCanonical(scenes)
	scenesCandidate, err := ProposeGenerated(auth, TargetScenes, "scenes-v1", scenesContent)
	if err != nil {
		t.Fatal(err)
	}
	scenesVersion, err := ConfirmTarget(auth, TargetScenes, scenesCandidate.CandidateID, "场景计划确认")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stateStore.Advance(StageScenes, map[string]string{"version_id": scenesVersion.VersionID}); err != nil {
		t.Fatal(err)
	}

	// 4. 逐场景草稿：检索知识 + 前文上下文 → 候选 → 确认。
	draftTexts := map[int]string{
		1: "深夜十一点，老周把车停在路口。雨刷停在半程，像一句没说完的话。那位乘客报出了码头站。",
		2: "码头的废址无路可走。老周点燃最后一根烟，收到了一条空短信。",
	}
	draftVersions := map[int]*authoring.TextVersion{}
	for _, scene := range scenes {
		known := RetrieveKnowledge(scene.Goal+scene.Obstacle, []KnowledgeDoc{
			{ID: "k1", Kind: "fact", Text: "码头站十年前已经拆除。"},
			{ID: "k2", Kind: "fact", Text: "老周的女儿小名叫豆豆。"},
		}, 2000)
		draftReq, err := AssembleSceneDraftRequest(prompts, SceneDraftContext{
			Structure: structure, Scenes: scenes, Scene: scene,
			PriorDrafts: map[int]string{1: draftTexts[1]}, Facts: knowledgeTexts(known),
		}, ContextBudgetRunes)
		if err != nil {
			t.Fatal(err)
		}
		draftResult, err := RunGeneration(ctx, draftFakeModel(draftTexts[scene.Index]), draftReq)
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureTarget(auth, SceneTargetID(scene.Index)); err != nil {
			t.Fatal(err)
		}
		candidate, err := ProposeGenerated(auth, SceneTargetID(scene.Index), "draft-v1", draftResult.Text)
		if err != nil {
			t.Fatal(err)
		}
		version, err := ConfirmTarget(auth, SceneTargetID(scene.Index), candidate.CandidateID, "场景草稿确认")
		if err != nil {
			t.Fatal(err)
		}
		draftVersions[scene.Index] = version
		// 事实/伏笔提取登记为候选（作者接受转正）。
		if _, _, err := RunExtraction(ctx, reviewFakeModel(`{"facts":["老周深夜停车载客"],"foreshadows":["空短信等待解释"]}`),
			auth, prompts, scene, SceneTargetID(scene.Index), version.VersionID, draftResult.Text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = stateStore.Advance(StageDraft, map[string]string{"scenes": "2"}); err != nil {
		t.Fatal(err)
	}

	// —— 关闭程序：重建全部实例，从持久工件恢复 ——
	authRecovered := authoring.Open(runRoot)
	stateRecovered, err := Open(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	recoveredState, err := stateRecovered.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recoveredState.Stage != StageDraft || len(recoveredState.History) != 4 {
		t.Fatalf("恢复状态不一致: stage=%s history=%d", recoveredState.Stage, len(recoveredState.History))
	}
	if head, _ := authRecovered.Head(SceneTargetID(1)); head.Content != draftTexts[1] {
		t.Fatalf("场景 1 草稿恢复不一致")
	}

	// 5. 定向修改：对场景 1 局部重写（AI 候选）→ 确认；拒绝另一候选不改正文。
	rewriteReq, err := AssembleSceneRewriteRequest(prompts, scenes[0], draftTexts[1], "加重雨夜氛围")
	if err != nil {
		t.Fatal(err)
	}
	rewriteResult, err := RunGeneration(ctx, draftFakeModel("深夜十一点，老周把车滑进路口的雨幕里。雨刷停在半程，那位乘客报出了码头站。"), rewriteReq)
	if err != nil {
		t.Fatal(err)
	}
	beforeHead, _ := authRecovered.Head(SceneTargetID(1))
	rejectedCandidate, err := ProposeGenerated(authRecovered, SceneTargetID(1), "rewrite-alt", "另一个备选开头。")
	if err != nil {
		t.Fatal(err)
	}
	if err := authRecovered.Reject(SceneTargetID(1), rejectedCandidate.CandidateID, "语气不对"); err != nil {
		t.Fatal(err)
	}
	if head, _ := authRecovered.Head(SceneTargetID(1)); head.VersionID != beforeHead.VersionID {
		t.Fatal("拒绝候选改变了正文")
	}
	acceptedCandidate, err := ProposeGenerated(authRecovered, SceneTargetID(1), "rewrite-rain", rewriteResult.Text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmTarget(authRecovered, SceneTargetID(1), acceptedCandidate.CandidateID, "定向修改确认"); err != nil {
		t.Fatal(err)
	}

	// 6. 全文检查：结构化审阅（矛盾 blocker 先被拒 → 干净终审）。
	if err := EnsureTarget(authRecovered, TargetFullText); err != nil {
		t.Fatal(err)
	}
	fullText, err := CommitFullText(authRecovered, scenes)
	if err != nil {
		t.Fatal(err)
	}
	blockingReview, err := ParseReviewResponse(
		reviewPayload("有矛盾", "老周把车滑进路口的雨幕里。", "blocker", "contradiction"),
		TargetFullText, fullText, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordReview(authRecovered, blockingReview); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeGateCheck(authRecovered, scenes); err == nil || !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("blocker 终审未阻断: %v", err)
	}
	cleanReview, err := ParseReviewResponse(
		reviewPayload("通过", "像一句没说完的话", "minor", "style"),
		TargetFullText, fullText, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordReview(authRecovered, cleanReview); err != nil {
		t.Fatal(err)
	}

	// 7. 作者终稿确认 + 状态机走完。
	if err := FinalizeGateCheck(authRecovered, scenes); err == nil || !strings.Contains(err.Error(), "终稿确认") {
		t.Fatalf("缺终稿确认未阻断: %v", err)
	}
	if _, err := authRecovered.RecordAuthorApproval(TargetFullText, fullText.VersionID, "", "作者终稿确认", "演示确认"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []Stage{StageReview, StageAuthorApproval, StageFinalize, StageDelivered} {
		if _, err = stateRecovered.Advance(step, map[string]string{"fulltext_version": fullText.VersionID}); err != nil {
			t.Fatal(err)
		}
	}

	// 8. 导出 + 校验交付物。
	result, manifestData, err := Export(runRoot, "雾城夜行", scenes)
	if err != nil {
		t.Fatal(err)
	}
	if manifestData.FullTextDigest != fullText.ContentDigest || manifestData.AuthorApprovalID == "" {
		t.Fatalf("交付清单不完整: %+v", manifestData)
	}
	if _, err := os.Stat(result.MarkdownPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.BackupDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

// knowledgeTexts 把检索结果铺平为事实清单。
func knowledgeTexts(docs []KnowledgeDoc) []string {
	out := make([]string, 0, len(docs))
	for _, doc := range docs {
		out = append(out, doc.Text)
	}
	return out
}
