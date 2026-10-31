package shortstory

import (
	"fmt"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/authoring"
)

// 本文件实现 P3-04 的修改影响预览：在接受一个候选之前，机械列出它会
// 使哪些已确认资料/审阅失效、哪些下游场景需要重审、全文是否需要重新
// 终审。预览只读，不改变任何状态。

// ImpactPreview 是一次候选接受的影响预览。
type ImpactPreview struct {
	TargetID            string   `json:"target_id"`
	CurrentVersion      string   `json:"current_version"`
	CandidateBase       string   `json:"candidate_base"`
	NewContentDigest    string   `json:"new_content_digest"`
	SourcesToStale      []string `json:"sources_to_stale"`
	ReviewsInvalidated  int      `json:"reviews_invalidated"`
	SceneReviewRequired bool     `json:"scene_review_required"`
	FullTextInvalidated bool     `json:"fulltext_invalidated"`
	LaterSceneDeps      []int    `json:"later_scene_deps"`
	Blocked             bool     `json:"blocked"`
	BlockReason         string   `json:"block_reason,omitempty"`
}

// PreviewAcceptImpact 预览接受候选的影响。场景目标会连带标记全文失效
// 与依赖本场景的后续场景重审；基线落后的候选直接给出阻断原因。
func PreviewAcceptImpact(auth *authoring.Store, targetID, candidateID string, scenes []Scene) (*ImpactPreview, error) {
	preview := &ImpactPreview{TargetID: targetID, SourcesToStale: []string{}, LaterSceneDeps: []int{}}
	candidate, err := auth.LoadCandidate(targetID, candidateID)
	if err != nil {
		return nil, err
	}
	preview.CandidateBase = candidate.BaseVersionID
	preview.NewContentDigest = candidate.ContentDigest
	head, err := auth.Head(targetID)
	if err != nil {
		return nil, err
	}
	preview.CurrentVersion = head.VersionID
	if candidate.BaseVersionID != head.VersionID {
		preview.Blocked = true
		preview.BlockReason = fmt.Sprintf("候选基线 %s 已落后于当前版本 %s，接受将被拒绝", candidate.BaseVersionID, head.VersionID)
		return preview, nil
	}
	// 已确认资料：接受后 head 推进，当前版本上绑定的来源全部转陈旧。
	confirmed, err := auth.ConfirmedSources(targetID)
	if err != nil {
		return nil, err
	}
	for _, ref := range confirmed {
		if ref.VersionID == head.VersionID {
			preview.SourcesToStale = append(preview.SourcesToStale, ref.SourceID)
		}
	}
	// 当前版本上的审阅将失效（需要重审）。
	requires, err := auth.RequiresReview(targetID)
	if err != nil {
		return nil, err
	}
	if !requires {
		preview.ReviewsInvalidated = 1
	}
	preview.SceneReviewRequired = true
	// 场景目标：全文将失效 + 下游依赖场景需要关注。
	if strings.HasPrefix(targetID, TargetSceneDraftPrefix) {
		preview.FullTextInvalidated = true
		sceneIndex := 0
		if _, err := fmt.Sscanf(targetID, TargetSceneDraftPrefix+"%d", &sceneIndex); err == nil {
			for _, scene := range scenes {
				for _, dep := range scene.CausalDeps {
					if dep == sceneIndex {
						preview.LaterSceneDeps = append(preview.LaterSceneDeps, scene.Index)
					}
				}
			}
		}
	}
	return preview, nil
}
