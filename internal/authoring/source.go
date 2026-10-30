package authoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

// SourceKind 区分来源引用的业务类别。
const (
	SourceFact        = "fact"         // 已确认事实
	SourceForeshadow  = "foreshadow"   // 伏笔
	SourceReviewIssue = "review_issue" // 审核意见
)

// SourceRef 把事实/伏笔/审核意见绑定到确切版本与原文位置。修改正文后
// 由 SourceStale 判定陈旧（P1 口径：版本不一致即陈旧；按内容的精确定位
// 刷新在 P2-03/P3-04 落地）。
type SourceRef struct {
	SchemaVersion string `json:"schema_version"`
	SourceID      string `json:"source_id"`
	TargetID      string `json:"target_id"`
	VersionID     string `json:"version_id"`
	Kind          string `json:"kind"`
	SceneIndex    int    `json:"scene_index,omitempty"`
	BlockID       string `json:"block_id,omitempty"`
	Text          string `json:"text"`
	TextDigest    string `json:"text_digest"`
	CreatedAt     string `json:"created_at"`
}

// ComputeSourceID 由绑定对象决定来源地址：同一来源重复登记幂等。
func ComputeSourceID(targetID, versionID, kind string, sceneIndex int, blockID, text string) string {
	digest := domain.ComputeArcArtifactSHA256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%s", targetID, versionID, kind, sceneIndex, blockID, text)))
	return "src-" + strings.TrimPrefix(digest, "sha256-")
}

// RecordSource 登记一条来源引用。要求绑定版本真实存在。
func (s *Store) RecordSource(targetID, versionID, kind string, sceneIndex int, blockID, text string) (*SourceRef, error) {
	switch kind {
	case SourceFact, SourceForeshadow, SourceReviewIssue:
	default:
		return nil, fmt.Errorf("authoring: 来源 kind %q 不受支持", kind)
	}
	if _, err := s.LoadVersion(targetID, versionID); err != nil {
		return nil, err
	}
	ref := &SourceRef{
		SchemaVersion: SchemaVersionV1,
		SourceID:      ComputeSourceID(targetID, versionID, kind, sceneIndex, blockID, text),
		TargetID:      targetID,
		VersionID:     versionID,
		Kind:          kind,
		SceneIndex:    sceneIndex,
		BlockID:       blockID,
		Text:          text,
		TextDigest:    domain.ComputeArcArtifactSHA256([]byte(text)),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	path := s.sourcePath(targetID, ref.SourceID)
	if existing, statErr := os.ReadFile(path); statErr == nil {
		var have SourceRef
		if err := json.Unmarshal(existing, &have); err != nil || have.TextDigest != ref.TextDigest {
			return nil, fmt.Errorf("authoring: 来源 %s 已存在且内容不同", ref.SourceID)
		}
		return &have, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := s.writeRecord(path, ref); err != nil {
		return nil, err
	}
	return ref, nil
}

// LoadSource 读取一条来源引用并校验文本摘要。
func (s *Store) LoadSource(targetID, sourceID string) (*SourceRef, error) {
	raw, err := os.ReadFile(s.sourcePath(targetID, sourceID))
	if err != nil {
		return nil, fmt.Errorf("authoring: 来源 %s 读取失败: %w", sourceID, err)
	}
	var ref SourceRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, fmt.Errorf("authoring: 来源 %s 解析失败: %w", sourceID, err)
	}
	if ref.TargetID != targetID || ref.SourceID != sourceID {
		return nil, fmt.Errorf("authoring: 来源 %s 身份不一致", sourceID)
	}
	if ref.TextDigest != domain.ComputeArcArtifactSHA256([]byte(ref.Text)) {
		return nil, fmt.Errorf("authoring: 来源 %s 文本摘要不一致（恢复校验失败）", sourceID)
	}
	return &ref, nil
}

// StaleReport 报告一条来源相对当前 head 的状态。
type StaleReport struct {
	Stale          bool   `json:"stale"`
	Reason         string `json:"reason,omitempty"`
	CurrentVersion string `json:"current_version"`
	BoundVersion   string `json:"bound_version"`
}

// SourceStale 判定来源是否陈旧。P1 口径：来源绑定的版本不再是当前
// head 即陈旧（Reason=version_superseded）；正文级精确定位与按影响刷新
// 在 P2-03/P3-04 实现。
func (s *Store) SourceStale(targetID, sourceID string) (*StaleReport, error) {
	ref, err := s.LoadSource(targetID, sourceID)
	if err != nil {
		return nil, err
	}
	head, err := s.Head(targetID)
	if err != nil {
		return nil, err
	}
	report := &StaleReport{CurrentVersion: head.VersionID, BoundVersion: ref.VersionID}
	if ref.VersionID != head.VersionID {
		report.Stale = true
		report.Reason = "version_superseded"
	}
	return report, nil
}

func (s *Store) sourcePath(targetID, sourceID string) string {
	return filepath.Join(s.dir, "targets", targetID, "sources", sourceID+".json")
}
