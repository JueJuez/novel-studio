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

// AuthorApproval 是作者对某一确切版本的确认。确认绑定版本地址、候选
// 来源与规则/计划摘要；正文推进后旧确认自动失效（Validity 由当前 head
// 动态判定，不靠标记字段）。model_accepted 与 author_approved 分别记账。
type AuthorApproval struct {
	SchemaVersion   string `json:"schema_version"`
	ApprovalID      string `json:"approval_id"`
	TargetID        string `json:"target_id"`
	VersionID       string `json:"version_id"`
	CandidateID     string `json:"candidate_id,omitempty"`
	ParentVersionID string `json:"parent_version_id,omitempty"`
	RulesSummary    string `json:"rules_summary,omitempty"`
	ApprovedBy      string `json:"approved_by"`
	Note            string `json:"note,omitempty"`
	CreatedAt       string `json:"created_at"`
}

// ModelAccept 是模型侧接受（审核通过）的独立记录，与作者确认互不替代。
type ModelAccept struct {
	SchemaVersion string `json:"schema_version"`
	RecordID      string `json:"record_id"`
	TargetID      string `json:"target_id"`
	VersionID     string `json:"version_id"`
	Model         string `json:"model"`
	Verdict       string `json:"verdict"`
	ReviewDigest  string `json:"review_digest,omitempty"`
	CreatedAt     string `json:"created_at"`
}

// ComputeApprovalID 由确认对象决定地址：对同一版本同一候选的重复确认
// 幂等。
func ComputeApprovalID(targetID, versionID, candidateID, approvedBy string) string {
	digest := domain.ComputeArcArtifactSHA256([]byte(targetID + "\x00" + versionID + "\x00" + candidateID + "\x00" + approvedBy))
	return "appr-" + strings.TrimPrefix(digest, "sha256-")
}

func computeModelAcceptID(targetID, versionID, model, verdict string) string {
	digest := domain.ComputeArcArtifactSHA256([]byte(targetID + "\x00" + versionID + "\x00" + model + "\x00" + verdict))
	return "macpt-" + strings.TrimPrefix(digest, "sha256-")
}

// RecordAuthorApproval 记录一次作者确认。要求被确认版本存在且是当前
// head 或其祖先链上的真实版本；同一确认重放幂等。
func (s *Store) RecordAuthorApproval(targetID, versionID, candidateID, rulesSummary, note string) (*AuthorApproval, error) {
	if _, err := s.LoadVersion(targetID, versionID); err != nil {
		return nil, err
	}
	approval := &AuthorApproval{
		SchemaVersion: SchemaVersionV1,
		ApprovalID:    ComputeApprovalID(targetID, versionID, candidateID, ApprovedByAuthor),
		TargetID:      targetID,
		VersionID:     versionID,
		CandidateID:   candidateID,
		RulesSummary:  rulesSummary,
		ApprovedBy:    ApprovedByAuthor,
		Note:          note,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	path := s.approvalPath(targetID, approval.ApprovalID)
	if existing, statErr := os.ReadFile(path); statErr == nil {
		var have AuthorApproval
		if err := json.Unmarshal(existing, &have); err != nil || have.VersionID != approval.VersionID {
			return nil, fmt.Errorf("authoring: 确认 %s 已存在且内容不同", approval.ApprovalID)
		}
		return &have, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := s.writeRecord(path, approval); err != nil {
		return nil, err
	}
	return approval, nil
}

// RecordModelAccept 记录模型侧接受。与作者确认分开存放、分开查询。
func (s *Store) RecordModelAccept(targetID, versionID, model, reviewDigest string) (*ModelAccept, error) {
	if _, err := s.LoadVersion(targetID, versionID); err != nil {
		return nil, err
	}
	record := &ModelAccept{
		SchemaVersion: SchemaVersionV1,
		RecordID:      computeModelAcceptID(targetID, versionID, model, "accept"),
		TargetID:      targetID,
		VersionID:     versionID,
		Model:         model,
		Verdict:       "accept",
		ReviewDigest:  reviewDigest,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	path := s.modelAcceptPath(targetID, record.RecordID)
	if existing, statErr := os.ReadFile(path); statErr == nil {
		var have ModelAccept
		if err := json.Unmarshal(existing, &have); err != nil || have.VersionID != record.VersionID {
			return nil, fmt.Errorf("authoring: 模型接受 %s 已存在且内容不同", record.RecordID)
		}
		return &have, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := s.writeRecord(path, record); err != nil {
		return nil, err
	}
	return record, nil
}

// AuthorApprovalState 报告目标当前版本的作者确认状态。
type AuthorApprovalState struct {
	Approved bool
	Record   *AuthorApproval
}

// AuthorApprovalForHead 判定当前 head 是否已被作者确认：只有确认记录
// 绑定的版本 == head 版本才算有效；head 推进后旧确认自动失效。
func (s *Store) AuthorApprovalForHead(targetID string) (*AuthorApprovalState, error) {
	head, err := s.Head(targetID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.approvalsDir(targetID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &AuthorApprovalState{}, nil
		}
		return nil, err
	}
	var latest *AuthorApproval
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.approvalsDir(targetID), entry.Name()))
		if err != nil {
			return nil, err
		}
		var approval AuthorApproval
		if err := json.Unmarshal(raw, &approval); err != nil {
			return nil, fmt.Errorf("authoring: 确认记录 %s 解析失败: %w", entry.Name(), err)
		}
		if approval.TargetID != targetID || approval.VersionID != head.VersionID {
			continue
		}
		if latest == nil || approval.CreatedAt > latest.CreatedAt {
			snapshot := approval
			latest = &snapshot
		}
	}
	if latest == nil {
		return &AuthorApprovalState{}, nil
	}
	return &AuthorApprovalState{Approved: true, Record: latest}, nil
}

// HasModelAccept 报告指定版本是否有模型接受记录（与作者确认无关）。
func (s *Store) HasModelAccept(targetID, versionID string) (bool, error) {
	entries, err := os.ReadDir(s.modelAcceptsDir(targetID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(s.modelAcceptsDir(targetID), entry.Name()))
		if err != nil {
			return false, err
		}
		var record ModelAccept
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, fmt.Errorf("authoring: 模型接受 %s 解析失败: %w", entry.Name(), err)
		}
		if record.TargetID == targetID && record.VersionID == versionID && record.Verdict == "accept" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) approvalsDir(targetID string) string {
	return filepath.Join(s.dir, "targets", targetID, "approvals")
}

func (s *Store) approvalPath(targetID, approvalID string) string {
	return filepath.Join(s.approvalsDir(targetID), approvalID+".json")
}

func (s *Store) modelAcceptsDir(targetID string) string {
	return filepath.Join(s.dir, "targets", targetID, "model_accepts")
}

func (s *Store) modelAcceptPath(targetID, recordID string) string {
	return filepath.Join(s.modelAcceptsDir(targetID), recordID+".json")
}
