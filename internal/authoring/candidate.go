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

// Candidate 是一次对某个基线版本的拟议修改。候选本身不可变；接受/拒绝
// 记录在 decisions/ 下（no-clobber），拒绝不改变当前正文。
type Candidate struct {
	SchemaVersion string `json:"schema_version"`
	CandidateID   string `json:"candidate_id"`
	TargetID      string `json:"target_id"`
	BaseVersionID string `json:"base_version_id"`
	Kind          string `json:"kind"` // ai | manual
	Task          string `json:"task"` // 规则/计划摘要
	Content       string `json:"content"`
	ContentDigest string `json:"content_digest"`
	CreatedAt     string `json:"created_at"`
}

// ComputeCandidateID 由基线版本、任务与内容决定候选地址：同一次提交
// 重试得到同一候选，天然幂等。
func ComputeCandidateID(targetID, baseVersionID, kind, task, content string) string {
	digest := domain.ComputeArcArtifactSHA256([]byte(targetID + "\x00" + baseVersionID + "\x00" + kind + "\x00" + task + "\x00" + content))
	return "cand-" + strings.TrimPrefix(digest, "sha256-")
}

// Propose 登记一个候选。同内容重试返回既有候选（幂等）。
func (s *Store) Propose(targetID, baseVersionID, kind, task, content string) (*Candidate, error) {
	if kind != "ai" && kind != "manual" {
		return nil, fmt.Errorf("authoring: 候选 kind 必须是 ai/manual，得到 %q", kind)
	}
	if _, err := s.LoadVersion(targetID, baseVersionID); err != nil {
		return nil, err
	}
	candidate := &Candidate{
		SchemaVersion: SchemaVersionV1,
		CandidateID:   ComputeCandidateID(targetID, baseVersionID, kind, task, content),
		TargetID:      targetID,
		BaseVersionID: baseVersionID,
		Kind:          kind,
		Task:          task,
		Content:       content,
		ContentDigest: domain.ComputeArcArtifactSHA256([]byte(content)),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	path := s.candidatePath(targetID, candidate.CandidateID)
	if existing, statErr := os.ReadFile(path); statErr == nil {
		var have Candidate
		if err := json.Unmarshal(existing, &have); err != nil || have.ContentDigest != candidate.ContentDigest {
			return nil, fmt.Errorf("authoring: 候选 %s 已存在且内容不同", candidate.CandidateID)
		}
		return &have, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := s.writeRecord(path, candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

// LoadCandidate 读取一个候选。
func (s *Store) LoadCandidate(targetID, candidateID string) (*Candidate, error) {
	raw, err := os.ReadFile(s.candidatePath(targetID, candidateID))
	if err != nil {
		return nil, fmt.Errorf("authoring: 候选 %s 读取失败: %w", candidateID, err)
	}
	var candidate Candidate
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return nil, fmt.Errorf("authoring: 候选 %s 解析失败: %w", candidateID, err)
	}
	if candidate.TargetID != targetID || candidate.CandidateID != candidateID {
		return nil, fmt.Errorf("authoring: 候选 %s 身份不一致", candidateID)
	}
	if candidate.ContentDigest != domain.ComputeArcArtifactSHA256([]byte(candidate.Content)) {
		return nil, fmt.Errorf("authoring: 候选 %s 内容摘要不一致（恢复校验失败）", candidateID)
	}
	return &candidate, nil
}

// Accept 接受候选：只有当候选的基线版本仍是当前正文时才允许（旧版本
// 冲突显式报错）。接受会生成新版本并推进 head；重复接受幂等返回既有
// 结果，不重复推进；崩溃窗口（版本/决定已写而 head 未推进）重试自动
// 收敛。被拒绝的候选不能接受。
func (s *Store) Accept(targetID, candidateID string) (*TextVersion, error) {
	candidate, err := s.LoadCandidate(targetID, candidateID)
	if err != nil {
		return nil, err
	}
	acceptedPath, rejectedPath := s.decisionPaths(targetID, candidateID)
	if _, err := os.Stat(rejectedPath); err == nil {
		return nil, fmt.Errorf("authoring: 候选 %s 已被拒绝，不能接受: %w", candidateID, ErrCandidateConflict)
	}
	producedID := ComputeVersionID(targetID, candidate.BaseVersionID, candidate.Content)
	if _, err := os.Stat(acceptedPath); err == nil {
		// 已接受：幂等返回该候选产生的版本；若崩溃停在 head 推进之前
		//（head 仍等于基线），先补齐推进。
		version, err := s.LoadVersion(targetID, producedID)
		if err != nil {
			return nil, fmt.Errorf("authoring: 候选 %s 已接受但其版本缺失: %w", candidateID, err)
		}
		head, err := s.Head(targetID)
		if err != nil {
			return nil, err
		}
		if head.VersionID == candidate.BaseVersionID && head.VersionID != producedID {
			if err := s.updateHead(targetID, producedID); err != nil {
				return nil, err
			}
		}
		return version, nil
	}
	head, err := s.Head(targetID)
	if err != nil {
		return nil, err
	}
	if candidate.BaseVersionID != head.VersionID {
		return nil, fmt.Errorf("authoring: 候选基线 %s 已落后于当前版本 %s: %w", candidate.BaseVersionID, head.VersionID, ErrVersionConflict)
	}
	// 崩溃窗口：版本文件已写而决定未写。同字节版本直接复用。
	version := &TextVersion{
		SchemaVersion:   SchemaVersionV1,
		TargetID:        targetID,
		VersionID:       producedID,
		ParentVersionID: candidate.BaseVersionID,
		Content:         candidate.Content,
		ContentDigest:   candidate.ContentDigest,
		Origin:          OriginCandidate + ":" + candidateID,
		Author:          "ai:" + candidate.Kind,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if existing, loadErr := s.LoadVersion(targetID, producedID); loadErr == nil {
		version = existing
	}
	if err := s.writeRecord(s.versionPath(targetID, version.VersionID), version); err != nil {
		return nil, err
	}
	if err := s.writeDecision(acceptedPath, targetID, candidateID, "accepted"); err != nil {
		return nil, err
	}
	if err := s.updateHead(targetID, version.VersionID); err != nil {
		return nil, err
	}
	return version, nil
}

// Reject 拒绝候选并记录原因；当前正文不发生任何变化。重复拒绝幂等。
// 已接受的候选不能改为拒绝。基线已落后时仍允许拒绝（决定独立于正文推进）。
func (s *Store) Reject(targetID, candidateID, reason string) error {
	if _, err := s.LoadCandidate(targetID, candidateID); err != nil {
		return err
	}
	acceptedPath, rejectedPath := s.decisionPaths(targetID, candidateID)
	if _, err := os.Stat(acceptedPath); err == nil {
		return fmt.Errorf("authoring: 候选 %s 已被接受，不能改为拒绝: %w", candidateID, ErrCandidateConflict)
	}
	payload := map[string]string{
		"schema_version": SchemaVersionV1,
		"target_id":      targetID,
		"candidate_id":   candidateID,
		"decision":       "rejected",
		"reason":         reason,
		"at":             time.Now().UTC().Format(time.RFC3339),
	}
	return s.writeRecord(rejectedPath, payload)
}

// Decision 返回候选的决定状态："accepted" / "rejected" / ""（未决定）。
func (s *Store) Decision(targetID, candidateID string) string {
	acceptedPath, rejectedPath := s.decisionPaths(targetID, candidateID)
	if _, err := os.Stat(acceptedPath); err == nil {
		return "accepted"
	}
	if _, err := os.Stat(rejectedPath); err == nil {
		return "rejected"
	}
	return ""
}

func (s *Store) candidatePath(targetID, candidateID string) string {
	return filepath.Join(s.dir, "targets", targetID, "candidates", candidateID+".json")
}

func (s *Store) decisionPaths(targetID, candidateID string) (accepted, rejected string) {
	base := filepath.Join(s.dir, "targets", targetID, "decisions", candidateID)
	return base + ".accepted.json", base + ".rejected.json"
}

func (s *Store) writeDecision(path, targetID, candidateID, decision string) error {
	payload := map[string]string{
		"schema_version": SchemaVersionV1,
		"target_id":      targetID,
		"candidate_id":   candidateID,
		"decision":       decision,
		"at":             time.Now().UTC().Format(time.RFC3339),
	}
	return s.writeRecord(path, payload)
}
