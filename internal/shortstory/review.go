package shortstory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/assets"
	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/voocel/agentcore"
)

// 本文件实现 P2-03：结构化审阅（矛盾/因果/POV/兑现/文风）与事实/伏笔
// 候选提取。审阅意见的 quote 必须与被审正文逐字匹配（原文绑定的机械
// 校验），语义疑点进 summary 留给作者评审；模型提取的事实/伏笔登记为
// 来源候选，作者接受后才进入已确认资料（计划 §6.1）。

// ReviewIssueType 是审阅意见的类别。
const (
	IssueContradiction = "contradiction"
	IssueCausality     = "causality"
	IssuePOV           = "pov"
	IssuePromise       = "promise"
	IssueStyle         = "style"
)

// ReviewIssue 是一条绑定原文的审阅意见。
type ReviewIssue struct {
	Type        string `json:"type"`
	SceneIndex  int    `json:"scene_index"`
	Quote       string `json:"quote"`
	QuoteDigest string `json:"quote_digest"`
	Severity    string `json:"severity"` // blocker | major | minor
	Description string `json:"description"`
}

// ReviewReport 是一次结构化审阅的完整报告，绑定被审版本。
type ReviewReport struct {
	SchemaVersion          string        `json:"schema_version"`
	TargetID               string        `json:"target_id"`
	ContentVersionID       string        `json:"content_version_id"`
	ContentDigest          string        `json:"content_digest"`
	Summary                string        `json:"summary"`
	Issues                 []ReviewIssue `json:"issues"`
	SemanticReviewRequired bool          `json:"semantic_review_required"`
	ReviewedAt             string        `json:"reviewed_at"`
}

// AssembleReviewRequest 组装审阅调用：正文 + 场景计划 + 承诺/结局。
func AssembleReviewRequest(prompts assets.Prompts, targetID, content string, structure *Structure, scenes []Scene) (StageRequest, error) {
	if strings.TrimSpace(content) == "" {
		return StageRequest{}, fmt.Errorf("shortstory: 审阅目标 %s 内容为空", targetID)
	}
	scenesJSON, err := MarshalCanonical(scenes)
	if err != nil {
		return StageRequest{}, err
	}
	userPrompt := fmt.Sprintf("## 正文\n\n%s\n\n## 场景计划\n\n%s\n\n## 结构承诺\n\n%s\n\n## 结局方向\n\n%s",
		content, scenesJSON, joinOrNone(structure.Promises), structure.EndingDirection)
	return StageRequest{
		Purpose:      "shortstory-review-" + targetID,
		SystemPrompt: prompts.ShortStoryReview,
		UserPrompt:   userPrompt,
		// 五类 issue 报告含逐字引用，4K 输出上限偏紧（P5-02 实测经验）。
		MaxTokens: 8192,
	}, nil
}

// ParseReviewResponse 解析审阅响应并做原文绑定校验：每条 issue 的
// quote 必须是正文逐字连续子串，否则该条无效（模型幻觉定位直接拒绝）。
// 发现矛盾（blocker）时强制 SemanticReviewRequired。
func ParseReviewResponse(text, targetID string, head *authoring.TextVersion, scenes []Scene) (*ReviewReport, error) {
	payload, err := decodeJSONLoose[struct {
		Summary string        `json:"summary"`
		Issues  []ReviewIssue `json:"issues"`
	}](text)
	if err != nil {
		return nil, fmt.Errorf("shortstory: 审阅响应解析失败: %w", err)
	}
	content := head.Content
	report := &ReviewReport{
		SchemaVersion:    StateSchemaVersion,
		TargetID:         targetID,
		ContentVersionID: head.VersionID,
		ContentDigest:    head.ContentDigest,
		Summary:          strings.TrimSpace(payload.Summary),
		Issues:           []ReviewIssue{},
	}
	sceneIndexes := map[int]bool{}
	for _, scene := range scenes {
		sceneIndexes[scene.Index] = true
	}
	for _, issue := range payload.Issues {
		quote := strings.TrimSpace(issue.Quote)
		if quote == "" || !strings.Contains(content, quote) {
			// 原文绑定失败：无效意见直接丢弃并要求语义评审兜底。
			report.SemanticReviewRequired = true
			continue
		}
		switch issue.Type {
		case IssueContradiction, IssueCausality, IssuePOV, IssuePromise, IssueStyle:
		default:
			report.SemanticReviewRequired = true
			continue
		}
		if issue.Severity != "blocker" && issue.Severity != "major" && issue.Severity != "minor" {
			report.SemanticReviewRequired = true
			continue
		}
		if issue.SceneIndex != 0 && len(sceneIndexes) > 0 && !sceneIndexes[issue.SceneIndex] {
			report.SemanticReviewRequired = true
			continue
		}
		issue.Quote = quote
		issue.QuoteDigest = domain.ComputeArcArtifactSHA256([]byte(quote))
		if issue.Type == IssueContradiction {
			report.SemanticReviewRequired = true
		}
		report.Issues = append(report.Issues, issue)
	}
	if len(report.Issues) == 0 && strings.TrimSpace(report.Summary) == "" {
		report.SemanticReviewRequired = true
	}
	report.ReviewedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return report, nil
}

// RecordReview 把审阅报告落盘为 authoring 来源（kind=review_issue，绑定
// 被审版本），保证审阅意见可恢复、可追溯。
func RecordReview(auth *authoring.Store, report *ReviewReport) error {
	content, err := MarshalCanonical(report)
	if err != nil {
		return err
	}
	ref, err := auth.RecordSource(report.TargetID, report.ContentVersionID, authoring.SourceReviewIssue, 0, "", content)
	if err != nil {
		return err
	}
	if err := auth.AcceptSource(report.TargetID, ref.SourceID); err != nil {
		return err
	}
	// blocker 级 issue 逐条登记为「未决来源」：交付门禁要求作者对每条
	// blocker 终裁——拒绝（作者裁定不成立/不构成交付障碍）即豁免，接受
	// 或保持未决都阻断交付。作者权力显式留痕，机械门禁本身不变。
	for _, issue := range report.Issues {
		if issue.Severity != "blocker" {
			continue
		}
		issueText, err := MarshalCanonical(issue)
		if err != nil {
			return err
		}
		if _, err := auth.RecordSource(report.TargetID, report.ContentVersionID, authoring.SourceReviewIssue, issue.SceneIndex, "", issueText); err != nil {
			return err
		}
	}
	return nil
}

// ReviewIssueSourceID 重导某条 blocker issue 的来源 ID（与 RecordReview
// 的登记口径字节一致），供工作台/CLI 列出待裁项与作者豁免。
func ReviewIssueSourceID(auth *authoring.Store, report *ReviewReport, issue ReviewIssue) (string, error) {
	issueText, err := MarshalCanonical(issue)
	if err != nil {
		return "", err
	}
	return authoring.ComputeSourceID(report.TargetID, report.ContentVersionID, authoring.SourceReviewIssue, issue.SceneIndex, "", issueText), nil
}

// ReviewIssueWaived 报告某条 blocker 是否已被作者豁免（其 issue 来源被
// 作者拒绝）。来源按内容寻址重导：版本推进后旧豁免自动失效。
func ReviewIssueWaived(auth *authoring.Store, report *ReviewReport, issue ReviewIssue) (bool, error) {
	sourceID, err := ReviewIssueSourceID(auth, report, issue)
	if err != nil {
		return false, err
	}
	// SourceDecision 对读取失败/未决都返回空串——空串即阻断，天然 fail-closed。
	return auth.SourceDecision(report.TargetID, sourceID) == "rejected", nil
}

// LoadLatestReview 读取目标上绑定当前 head 版本的最近审阅报告。
func LoadLatestReview(auth *authoring.Store, targetID string) (*ReviewReport, error) {
	head, err := auth.Head(targetID)
	if err != nil {
		return nil, err
	}
	confirmed, err := auth.ConfirmedSources(targetID)
	if err != nil {
		return nil, err
	}
	var latest *ReviewReport
	for _, ref := range confirmed {
		if ref.Kind != authoring.SourceReviewIssue || ref.VersionID != head.VersionID {
			continue
		}
		var report ReviewReport
		if err := json.Unmarshal([]byte(ref.Text), &report); err != nil {
			return nil, fmt.Errorf("shortstory: 审阅报告解析失败: %w", err)
		}
		if latest == nil || report.ReviewedAt > latest.ReviewedAt {
			snapshot := report
			latest = &snapshot
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("shortstory: 目标 %s 在当前版本 %s 上没有审阅报告", targetID, head.VersionID)
	}
	return latest, nil
}

// ExtractFactsRequest 组装事实/伏笔提取调用。
func ExtractFactsRequest(prompts assets.Prompts, scene Scene, draftContent string) (StageRequest, error) {
	return StageRequest{
		Purpose:      fmt.Sprintf("shortstory-facts-scene-%d", scene.Index),
		SystemPrompt: prompts.ShortStoryFacts,
		UserPrompt:   fmt.Sprintf("## 场景计划\n\n%s\n\n## 正文\n\n%s", mustSceneJSON(scene), draftContent),
		// P5-02 实测：长草稿的 facts JSON 在 4K 输出被截断。
		MaxTokens: 8192,
	}, nil
}

// ExtractedFacts 是模型提取的事实/伏笔候选。
type ExtractedFacts struct {
	Facts       []string `json:"facts"`
	Foreshadows []string `json:"foreshadows"`
}

// ParseFactsResponse 解析提取结果并登记为来源候选（未接受状态）。
// 作者接受后进入已确认资料。
func ParseFactsResponse(text string, auth *authoring.Store, targetID, versionID string, sceneIndex int) (*ExtractedFacts, []*authoring.SourceRef, error) {
	extracted, err := decodeJSONLoose[ExtractedFacts](text)
	if err != nil {
		return nil, nil, fmt.Errorf("shortstory: 事实提取响应解析失败: %w", err)
	}
	var refs []*authoring.SourceRef
	for _, fact := range extracted.Facts {
		if strings.TrimSpace(fact) == "" {
			continue
		}
		ref, err := auth.RecordSource(targetID, versionID, authoring.SourceFact, sceneIndex, "", fact)
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, ref)
	}
	for _, foreshadow := range extracted.Foreshadows {
		if strings.TrimSpace(foreshadow) == "" {
			continue
		}
		ref, err := auth.RecordSource(targetID, versionID, authoring.SourceForeshadow, sceneIndex, "", foreshadow)
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, ref)
	}
	return &extracted, refs, nil
}

// RunExtraction 用模型适配器跑一次事实提取并登记候选（测试传 mock）。
func RunExtraction(ctx context.Context, model agentcore.ChatModel, auth *authoring.Store, prompts assets.Prompts, scene Scene, targetID, versionID, draftContent string) (*ExtractedFacts, []*authoring.SourceRef, error) {
	req, err := ExtractFactsRequest(prompts, scene, draftContent)
	if err != nil {
		return nil, nil, err
	}
	result, err := Generate(ctx, model, req)
	if err != nil {
		return nil, nil, err
	}
	facts, refs, err := ParseFactsResponse(result.Text, auth, targetID, versionID, scene.Index)
	if err != nil {
		// 带上原始响应摘录与用量，便于区分「推理耗尽输出」与格式漂移
		// （P5-02 实测：reasoning 模型可能在长思考后输出为空）。
		snippet := result.Text
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, nil, fmt.Errorf("%w（usage=%+v resp=%q）", err, result.Usage, snippet)
	}
	return facts, refs, nil
}
