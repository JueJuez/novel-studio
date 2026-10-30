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
		MaxTokens:    4096,
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
	report.ReviewedAt = time.Now().UTC().Format(time.RFC3339)
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
	return auth.AcceptSource(report.TargetID, ref.SourceID)
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
		MaxTokens:    2048,
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
	return ParseFactsResponse(result.Text, auth, targetID, versionID, scene.Index)
}
