package shortstory

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/voocel/agentcore"
)

func blockRevisionModel(text string) *fakeChatModel {
	return &fakeChatModel{response: agentcore.Message{
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: text}},
		Usage:   &agentcore.Usage{Provider: "mock", Model: "mock-probe", Input: 15, Output: 8},
	}}
}

func TestBlockRevisionRunnerRewriteAndContinue(t *testing.T) {
	auth := authoring.Open(t.TempDir())
	content := "第一段：雨夜的路口。\n\n第二段：老周把车停在路边，点了火。\n\n第三段：那位乘客报出了码头站。"
	seedContent(t, auth, SceneTargetID(1), content)
	prompts := testPrompts()
	ctx := context.Background()

	// 改写：只影响目标块。
	rewriteResult, _, err := RunBlockRevision(ctx, blockRevisionModel("第二段：老周把车滑进路边阴影，熄了火。"), auth, prompts, SceneTargetID(1), authoring.ComputeBlockID("第二段：老周把车停在路边，点了火。"), RevisionRewrite, "氛围更冷")
	if err != nil {
		t.Fatal(err)
	}
	blocks := authoring.SplitBlocks(rewriteResult.Content)
	if len(blocks) != 3 || blocks[1].Text != "第二段：老周把车滑进路边阴影，熄了火。" ||
		blocks[0].Text != "第一段：雨夜的路口。" || blocks[2].Text != "第三段：那位乘客报出了码头站。" {
		t.Fatalf("改写影响了未选区: %+v", blocks)
	}
	if _, err := ConfirmTarget(auth, SceneTargetID(1), rewriteResult.CandidateID, "改写第二段"); err != nil {
		t.Fatal(err)
	}

	// 续写：在最后一段后插入新块，既有内容不变。
	continueResult, _, err := RunBlockRevision(ctx, blockRevisionModel("第四段：老周收到了一条空短信。"), auth, prompts, SceneTargetID(1), authoring.ComputeBlockID("第三段：那位乘客报出了码头站。"), RevisionContinue, "引出空短信")
	if err != nil {
		t.Fatal(err)
	}
	blocks = authoring.SplitBlocks(continueResult.Content)
	if len(blocks) != 4 || blocks[3].Text != "第四段：老周收到了一条空短信。" {
		t.Fatalf("续写插入位置错误: %+v", blocks)
	}

	// 扩写 + 未知类型 + 空输出守卫。
	extendResult, _, err := RunBlockRevision(ctx, blockRevisionModel("第一段：雨夜更深，路口的灯忽明忽暗。"), auth, prompts, SceneTargetID(1), authoring.ComputeBlockID("第一段：雨夜的路口。"), RevisionExtend, "细节更足")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extendResult.Content, "忽明忽暗") {
		t.Fatalf("扩写结果不符: %s", extendResult.Content)
	}
	if _, _, err := RunBlockRevision(ctx, blockRevisionModel("x"), auth, prompts, SceneTargetID(1), authoring.ComputeBlockID("第一段：雨夜更深，路口的灯忽明忽暗。"), "magic", "x"); err == nil {
		t.Fatal("未知修订类型未被拒绝")
	}
	if _, _, err := RunBlockRevision(ctx, blockRevisionModel("   "), auth, prompts, SceneTargetID(1), authoring.ComputeBlockID("第一段：雨夜更深，路口的灯忽明忽暗。"), RevisionRewrite, "x"); err == nil {
		t.Fatal("空模型输出未被拒绝")
	}
	if _, err := auth.Head(SceneTargetID(1)); err != nil {
		t.Fatal(err)
	}
}
