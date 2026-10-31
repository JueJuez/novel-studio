package shortstory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/assets"
	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/voocel/agentcore"
)

func testPrompts() assets.Prompts {
	return assets.Load("").Prompts
}

func structureResponseJSON() string {
	return "```json\n" + `{
  "conflict": "司机想查明乘客身份，乘客只想在消失前完成一次告别",
  "characters": [{"name": "老周", "desire": "弄清乘客的秘密", "choice": "在码头点燃最后一根烟"}],
  "ending_direction": "开放式：乘客消失，老周收到一条空短信（刻意保留发信人）",
  "promises": ["空短信的来源在结尾揭晓"],
  "author_constraints": ["开放式结局", "第一人称"]
}` + "\n```"
}

func scenesResponseJSON() string {
	return `[
  {"index": 1, "chapter": 0, "pov": "老周", "goal": "接到这单生意", "obstacle": "出发点含糊", "facts": ["接到去往已拆除码头站的乘客"], "word_budget": 2500, "continuity_next": "乘客报出码头站"},
  {"index": 2, "chapter": 0, "pov": "老周", "goal": "决定是否跟下去", "obstacle": "码头废址无路", "causal_deps": [1], "facts": ["空短信的来源在结尾揭晓"], "word_budget": 2500, "turn": "乘客消失"}
]`
}

func draftResponseText() string {
	return "深夜十一点，老周把车停在路口。雨刷停在半程，像一句没说完的话。"
}

func draftFakeModel(text string) *fakeChatModel {
	return &fakeChatModel{response: agentcore.Message{
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: text}},
		Usage:   &agentcore.Usage{Provider: "mock", Model: "mock-probe", Input: 20, Output: 10},
	}}
}

func TestStructureGenerationProposesAndConfirms(t *testing.T) {
	prompts := testPrompts()
	brief := Brief{Idea: "深夜出租车接到去已拆除码头站的乘客。", AuthorConstraints: []string{"开放式结局", "第一人称"}}
	req, err := AssembleStructureRequest(prompts, brief, []string{"乘客声称知道老周女儿"})
	if err != nil {
		t.Fatal(err)
	}
	// 作者约束与检索知识必须进入提示词。
	if !strings.Contains(req.UserPrompt, "开放式结局") || !strings.Contains(req.UserPrompt, "老周女儿") {
		t.Fatalf("作者约束/人物知识未进入结构提示词: %s", req.UserPrompt)
	}

	structure, err := ParseStructureResponse(draftFakeModel(structureResponseJSON()).response.Content[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	if len(structure.Promises) != 1 || structure.Characters[0].Name != "老周" {
		t.Fatalf("结构解析不完整: %+v", structure)
	}

	// 生成 → 候选 → 确认：AI 产出不自动成为正式故事。
	auth := authoring.Open(t.TempDir())
	if err := EnsureTarget(auth, TargetStructure); err != nil {
		t.Fatal(err)
	}
	content, err := MarshalCanonical(structure)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := auth.Head(TargetStructure)
	if head.Content != "" {
		t.Fatalf("确认前 head 应为空根版本: %q", head.Content)
	}
	candidate, err := ProposeGenerated(auth, TargetStructure, "structure-v1", content)
	if err != nil {
		t.Fatal(err)
	}
	version, err := ConfirmTarget(auth, TargetStructure, candidate.CandidateID, "规则摘要 v1")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, _ := auth.Head(TargetStructure)
	if confirmed.Content != content || confirmed.VersionID != version.VersionID {
		t.Fatalf("确认后 head 不正确")
	}
	state, err := auth.AuthorApprovalForHead(TargetStructure)
	if err != nil || !state.Approved {
		t.Fatalf("结构确认未生效: %+v err=%v", state, err)
	}

	// 手改走 manual 候选：确认后旧确认失效、新确认生效。
	edited := structure
	edited.EndingDirection = "开放式：老周把空短信删掉（刻意保留发信人）"
	editedContent, _ := MarshalCanonical(edited)
	manual, err := ProposeManual(auth, TargetStructure, editedContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmTarget(auth, TargetStructure, manual.CandidateID, "手改"); err != nil {
		t.Fatal(err)
	}
	if state, _ = auth.AuthorApprovalForHead(TargetStructure); !state.Approved || state.Record.VersionID == version.VersionID {
		t.Fatalf("手改后确认应绑定新版本: %+v", state)
	}
}

func TestScenesGenerationValidatesBudgetDAGAndPromises(t *testing.T) {
	structure := &Structure{
		Conflict:        "司机 vs 乘客",
		Characters:      []StructureCharacter{{Name: "老周", Desire: "查明", Choice: "跟下去"}},
		EndingDirection: "开放式：空短信（刻意保留发信人）",
		Promises:        []string{"空短信的来源"},
	}
	scenes, total, err := ParseScenesResponse(scenesResponseJSON(), structure)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5000 || len(scenes) != 2 {
		t.Fatalf("scenes=%d total=%d", len(scenes), total)
	}

	// 承诺无兑现安排 → 显式拒绝。
	missingStructure := *structure
	missingStructure.Promises = []string{"完全无关的承诺"}
	if _, _, err := ParseScenesResponse(scenesResponseJSON(), &missingStructure); err == nil || !strings.Contains(err.Error(), "承诺") {
		t.Fatalf("承诺缺失未被拒绝: %v", err)
	}
	// DAG 非法：依赖不早于自身。
	badDAG := `[{"index":1,"pov":"a","goal":"g","obstacle":"o","causal_deps":[1],"word_budget":5000}]`
	if _, _, err := ParseScenesResponse(badDAG, structure); err == nil || !strings.Contains(err.Error(), "依赖") {
		t.Fatalf("非法依赖未被拒绝: %v", err)
	}
	// 预算越界显式报错（首批验证区间）。
	bigBudget := `[{"index":1,"pov":"a","goal":"g","obstacle":"o","word_budget":40000}]`
	if _, _, err := ParseScenesResponse(bigBudget, structure); err == nil || !strings.Contains(err.Error(), "验证区间") {
		t.Fatalf("预算越界未被拒绝: %v", err)
	}
}

func TestSceneDraftAssemblyGuaranteesAndOverflow(t *testing.T) {
	prompts := testPrompts()
	structure := &Structure{
		Promises:        []string{"空短信的来源"},
		EndingDirection: "开放式结局",
	}
	scenes, _, err := ParseScenesResponse(scenesResponseJSON(), structure)
	if err != nil {
		t.Fatal(err)
	}
	dcc := SceneDraftContext{
		Structure:   structure,
		Scenes:      scenes,
		Scene:       scenes[1],
		PriorDrafts: map[int]string{1: strings.Repeat("前文内容。", 1200)},
		Facts:       []string{"码头站十年前拆除", "乘客知道老周女儿小名"},
	}
	req, err := AssembleSceneDraftRequest(prompts, dcc, ContextBudgetRunes)
	if err != nil {
		t.Fatal(err)
	}
	// 硬保证项全部在提示词中。
	for _, must := range []string{"空短信的来源", "开放式结局", "causal_deps", "十年前拆除", "前文摘录"} {
		if !strings.Contains(req.UserPrompt, must) {
			t.Fatalf("草稿提示词缺少硬保证项 %q", must)
		}
	}
	if !strings.Contains(req.Purpose, "shortstory-draft-scene-2") {
		t.Fatalf("路由标识错误: %s", req.Purpose)
	}

	// 预算紧张：检索知识先被丢弃，前文截短，硬保证项保留。
	tight, err := AssembleSceneDraftRequest(prompts, dcc, 1000)
	if err != nil {
		t.Fatalf("分层裁剪后不应溢出: %v", err)
	}
	if strings.Contains(tight.UserPrompt, "码头站十年前拆除") {
		t.Fatal("预算紧张时检索知识应先被丢弃")
	}
	if !strings.Contains(tight.UserPrompt, "空短信的来源") || !strings.Contains(tight.UserPrompt, "前文摘录") {
		t.Fatal("分层裁剪不得丢弃硬保证项")
	}
	// 极小预算：显式溢出，不静默截断。
	if _, err := AssembleSceneDraftRequest(prompts, dcc, 50); !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("超预算未显式报错: %v", err)
	}
}

func TestSceneRewriteAssemblyKeepsFacts(t *testing.T) {
	prompts := testPrompts()
	scene := Scene{Index: 1, POV: "老周", WordBudget: 2000, Facts: []string{"接到去码头站的乘客"}}
	req, err := AssembleSceneRewriteRequest(prompts, scene, "原稿正文。", "把雨夜氛围加重")
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"原稿正文。", "把雨夜氛围加重", "接到去码头站的乘客"} {
		if !strings.Contains(req.UserPrompt, must) {
			t.Fatalf("重写提示词缺少 %q", must)
		}
	}
}

func TestSceneDraftRunViaMockModelAndAuthoring(t *testing.T) {
	auth := authoring.Open(t.TempDir())
	if err := EnsureTarget(auth, SceneTargetID(1)); err != nil {
		t.Fatal(err)
	}
	model := draftFakeModel(draftResponseText())
	req, err := AssembleSceneDraftRequest(testPrompts(), SceneDraftContext{
		Structure: &Structure{Promises: []string{"p"}, EndingDirection: "e"},
		Scenes:    []Scene{{Index: 1, POV: "老周", Goal: "g", Obstacle: "o", WordBudget: 2000}},
		Scene:     Scene{Index: 1, POV: "老周", Goal: "g", Obstacle: "o", WordBudget: 2000},
	}, ContextBudgetRunes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunGeneration(context.Background(), model, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != draftResponseText() {
		t.Fatalf("草稿正文不符: %q", result.Text)
	}
	// 局部重写生成候选 → 接受 → head 推进。
	candidate, err := ProposeGenerated(auth, SceneTargetID(1), "rewrite-heavy-rain", result.Text)
	if err != nil {
		t.Fatal(err)
	}
	version, err := ConfirmTarget(auth, SceneTargetID(1), candidate.CandidateID, "场景 1 草稿")
	if err != nil {
		t.Fatal(err)
	}
	if version.Content != draftResponseText() {
		t.Fatalf("草稿版本内容不符")
	}
	if state, _ := auth.AuthorApprovalForHead(SceneTargetID(1)); !state.Approved {
		t.Fatal("草稿确认未生效")
	}
}

func TestRetrieveKnowledgeRanksAndPacks(t *testing.T) {
	docs := []KnowledgeDoc{
		{ID: "a", Kind: "fact", Text: "码头站在十年前已经拆除，只剩生锈的栏杆。"},
		{ID: "b", Kind: "fact", Text: "老周的女儿小名叫豆豆。"},
		{ID: "c", Kind: "constraint", Text: "作者要求开放式结局。"},
	}
	// 中文 bigram 命中：查询"码头站"应把相关文档排前。
	got := RetrieveKnowledge("码头站 老周", docs, 1000)
	if len(got) == 0 || got[0].ID != "a" {
		t.Fatalf("检索排序错误: %+v", got)
	}
	// 整条打包：预算只够一条时不得截半条。
	tight := RetrieveKnowledge("码头站 老周", docs, 20)
	for _, doc := range tight {
		if len([]rune(doc.Text)) > 20 {
			t.Fatalf("预算打包出现截断: %q", doc.Text)
		}
	}
	if len(tight) == 0 {
		t.Fatal("预算内应有命中")
	}
	// 无命中词 → 空结果。
	if got := RetrieveKnowledge("zzzzqqqq", docs, 1000); len(got) != 0 {
		t.Fatalf("无命中应返回空: %+v", got)
	}
}
