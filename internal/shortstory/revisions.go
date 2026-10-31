package shortstory

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhongyang/novel-studio/assets"
	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/voocel/agentcore"
)

// 本文件实现 P3-02 的块级 AI 修订 runner：改写（rewrite）、扩写
//（extend）、续写（continue）三种指令都走块级候选——只影响目标块
//（续写在其后插入新块），其余内容逐字节不变；候选经确认流转正。
// 提示词复用 shortstory-rewrite.md 的合同（原稿+指令+必须保留）。

// RevisionKind 是块级修订类型。
const (
	RevisionRewrite  = "rewrite"
	RevisionExtend   = "extend"
	RevisionContinue = "continue"
)

// revisionInstruction 把修订类型翻译成显式指令（写进提示词，保证模型
// 知道这是块级操作）。
func revisionInstruction(kind, instruction string) (string, error) {
	switch kind {
	case RevisionRewrite:
		return "改写下面这一段（只输出改写后的这一段，不要输出其它段落）：" + instruction, nil
	case RevisionExtend:
		return "扩写下面这一段（只输出扩写后的这一段，保留全部既有信息与事实，不要输出其它段落）：" + instruction, nil
	case RevisionContinue:
		return "从下面这一段的结尾继续写（只输出新的续写段落，不要重复原文，不要输出其它段落）：" + instruction, nil
	default:
		return "", fmt.Errorf("shortstory: 未知修订类型 %q", kind)
	}
}

// AssembleBlockRevisionRequest 组装块级修订调用。
func AssembleBlockRevisionRequest(prompts assets.Prompts, blockText, kind, instruction string) (StageRequest, error) {
	expanded, err := revisionInstruction(kind, instruction)
	if err != nil {
		return StageRequest{}, err
	}
	userPrompt := fmt.Sprintf("## 目标段落\n\n%s\n\n## 修改指令\n\n%s", strings.TrimSpace(blockText), expanded)
	return StageRequest{
		Purpose:      fmt.Sprintf("shortstory-block-%s", kind),
		SystemPrompt: prompts.ShortStoryRewrite,
		UserPrompt:   userPrompt,
		MaxTokens:    2048,
	}, nil
}

// RunBlockRevision 执行一次块级修订：锚定目标块（选区唯一性在此强制）
// → 模型生成 → 确定性重组为候选。返回候选与块对照；转正走确认流。
func RunBlockRevision(ctx context.Context, model agentcore.ChatModel, auth *authoring.Store, prompts assets.Prompts, targetID, blockID, kind, instruction string) (*authoring.Candidate, *authoring.BlockDiff, error) {
	head, err := auth.Head(targetID)
	if err != nil {
		return nil, nil, err
	}
	anchor, block, err := auth.AnchorBlockFromContent(targetID, head.VersionID, blockID)
	if err != nil {
		return nil, nil, err
	}
	req, err := AssembleBlockRevisionRequest(prompts, block.Text, kind, instruction)
	if err != nil {
		return nil, nil, err
	}
	result, err := Generate(ctx, model, req)
	if err != nil {
		return nil, nil, err
	}
	newText := strings.TrimSpace(result.Text)
	if newText == "" {
		return nil, nil, fmt.Errorf("shortstory: 块级修订返回空文本")
	}
	modifyReq := authoring.ModifyRequest{TargetID: targetID, BaseVersionID: head.VersionID, AnchorID: anchor.AnchorID}
	switch kind {
	case RevisionRewrite, RevisionExtend:
		return auth.ReviseBlockCandidate(targetID, modifyReq, blockID, newText, "ai", kind)
	case RevisionContinue:
		return auth.InsertBlockAfterCandidate(targetID, modifyReq, blockID, newText, "ai", kind)
	default:
		return nil, nil, fmt.Errorf("shortstory: 未知修订类型 %q", kind)
	}
}
