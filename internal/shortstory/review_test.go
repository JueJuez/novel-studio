package shortstory

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/voocel/agentcore"
)

const reviewSceneContent = "深夜十一点，老周把车停在路口。雨刷停在半程，像一句没说完的话。"

func reviewFakeModel(payload string) *fakeChatModel {
	return &fakeChatModel{response: agentcore.Message{
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: payload}},
		Usage:   &agentcore.Usage{Provider: "mock", Model: "mock-probe", Input: 30, Output: 15},
	}}
}

// seedContent 用规范路径（manual 候选 + 作者确认）为目标种入内容并返回
// 其版本，保证与真实流程一致。
func seedContent(t *testing.T, auth *authoring.Store, targetID, content string) *authoring.TextVersion {
	t.Helper()
	if err := EnsureTarget(auth, targetID); err != nil {
		t.Fatal(err)
	}
	candidate, err := ProposeManual(auth, targetID, content)
	if err != nil {
		t.Fatal(err)
	}
	version, err := ConfirmTarget(auth, targetID, candidate.CandidateID, "seed")
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestReviewQuoteBindingAndRoundtrip(t *testing.T) {
	auth := authoring.Open(t.TempDir())
	initial := seedContent(t, auth, TargetFullText, reviewSceneContent)
	prompts := testPrompts()
	structure := &Structure{Promises: []string{"空短信的来源"}, EndingDirection: "开放式结局"}
	scenes, _, err := ParseScenesResponse(scenesResponseJSON(), structure)
	if err != nil {
		t.Fatal(err)
	}

	// 引用幻觉（不在原文）+ 非法类别 + 合法 blocker 引用。
	payload := "```json\n" + `{
  "summary": "存在一处矛盾",
  "issues": [
    {"type": "contradiction", "scene_index": 1, "quote": "这句原文里没有", "severity": "blocker", "description": "幻觉引用"},
    {"type": "magic", "scene_index": 1, "quote": "老周把车停在路口。", "severity": "major", "description": "未知类别"},
    {"type": "contradiction", "scene_index": 1, "quote": "老周把车停在路口。", "severity": "blocker", "description": "与前文雨刷状态矛盾"},
    {"type": "style", "scene_index": 1, "quote": "雨刷停在半程", "severity": "minor", "description": "比喻略显陈旧"}
  ]
}` + "\n```"
	req, err := AssembleReviewRequest(prompts, TargetFullText, reviewSceneContent, structure, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.UserPrompt, "空短信的来源") || !strings.Contains(req.UserPrompt, reviewSceneContent) {
		t.Fatal("审阅提示词缺少正文/承诺")
	}
	report, err := ParseReviewResponse(payload, TargetFullText, initial, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) != 2 {
		t.Fatalf("有效意见应为 2 条（幻觉与未知类别被拒）: %+v", report.Issues)
	}
	if report.Issues[0].QuoteDigest == "" || !strings.Contains(report.Issues[0].Quote, "老周把车停在路口。") {
		t.Fatalf("合法意见缺少原文绑定: %+v", report.Issues[0])
	}
	if !report.SemanticReviewRequired {
		t.Fatal("矛盾问题必须触发语义评审标记")
	}

	// 落盘 + 读取往返：绑定同一版本。
	if err := RecordReview(auth, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLatestReview(auth, TargetFullText)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentVersionID != initial.VersionID || len(loaded.Issues) != 2 {
		t.Fatalf("审阅往返不一致: %+v", loaded)
	}

	// 全部有效且无矛盾 → 不需要语义评审。
	cleanPayload := `{"summary":"通过","issues":[{"type":"style","scene_index":1,"quote":"雨刷停在半程","severity":"minor","description":"可接受"}]}`
	clean, err := ParseReviewResponse(cleanPayload, TargetFullText, initial, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if clean.SemanticReviewRequired {
		t.Fatalf("干净报告不应要求语义评审: %+v", clean)
	}
	// 全空报告（无意见无摘要）→ 要求语义评审兜底。
	empty, err := ParseReviewResponse(`{"summary":"","issues":[]}`, TargetFullText, initial, scenes)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.SemanticReviewRequired {
		t.Fatal("全空报告应要求语义评审兜底")
	}
}

func TestFactsExtractionCandidatesAndAcceptance(t *testing.T) {
	auth := authoring.Open(t.TempDir())
	version := seedContent(t, auth, SceneTargetID(1), reviewSceneContent)
	scene := Scene{Index: 1, POV: "老周", WordBudget: 2000}

	// 提示词包含场景计划与正文。
	req, err := ExtractFactsRequest(testPrompts(), scene, reviewSceneContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.UserPrompt, "老周") || !strings.Contains(req.UserPrompt, reviewSceneContent) {
		t.Fatal("事实提取提示词缺少场景计划或正文")
	}

	payload := `{"facts": ["老周深夜在路口停车载客", "码头站十年前已拆除"], "foreshadows": ["空短信等待解释"], "junk": ""}`
	extracted, refs, err := RunExtraction(context.Background(), reviewFakeModel(payload), auth, testPrompts(), scene, SceneTargetID(1), version.VersionID, reviewSceneContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(extracted.Facts) != 2 || len(refs) != 3 {
		t.Fatalf("提取数量错误: facts=%d refs=%d", len(extracted.Facts), len(refs))
	}
	// 全部是候选：未决定。
	for _, ref := range refs {
		if auth.SourceDecision(SceneTargetID(1), ref.SourceID) != "" {
			t.Fatalf("提取结果应先为候选: %s", ref.SourceID)
		}
	}
	// 作者接受 → 进入已确认资料。
	if err := auth.AcceptSource(SceneTargetID(1), refs[0].SourceID); err != nil {
		t.Fatal(err)
	}
	confirmed, err := auth.ConfirmedSources(SceneTargetID(1))
	if err != nil || len(confirmed) != 1 {
		t.Fatalf("已确认资料数量错误: %d err=%v", len(confirmed), err)
	}
	// 拒绝 + 互斥。
	if err := auth.RejectSource(SceneTargetID(1), refs[1].SourceID, "并非正文事实"); err != nil {
		t.Fatal(err)
	}
	if err := auth.RejectSource(SceneTargetID(1), refs[1].SourceID, "重复拒绝幂等"); err != nil {
		t.Fatal(err)
	}
	if err := auth.AcceptSource(SceneTargetID(1), refs[1].SourceID); err == nil {
		t.Fatal("拒绝后接受未被拒绝")
	}
	// 重复接受幂等。
	if err := auth.AcceptSource(SceneTargetID(1), refs[0].SourceID); err != nil {
		t.Fatalf("重复接受不幂等: %v", err)
	}
}
