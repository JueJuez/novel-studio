// Package authoring 实现双管线共享的文本版本、候选、作者确认与来源引用
// 合同（dual-pipeline plan §6.3 关键约束 1—3），首先服务短篇管线。
//
// 存储模型：一切记录皆不可变文件（no-clobber 写入），唯一可变指针是
// head.json（临时文件+rename 原子替换）。任何一步崩溃都可以通过重放
// 同一操作收敛，不会产生半份正文或半份确认。
//
// 本包只依赖 stdlib 与 internal/domain 的哈希工具，不导入长篇引擎。
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
	"github.com/chenhongyang/novel-studio/internal/project"
)

const (
	// SchemaVersionV1 是 authoring 记录的格式版本。
	SchemaVersionV1 = "authoring.v1"

	OriginInitial   = "initial"
	OriginCandidate = "candidate"
	OriginManual    = "manual"

	ApprovedByAuthor = "author"
)

// ErrVersionConflict 表示请求绑定的来源版本不再是当前正文：必须重新
// 基于最新版本发起，绝不静默退化成整章改写（§6.3 约束 1）。
var ErrVersionConflict = errors.New("authoring: 来源版本已不是当前正文")

// ErrBlockConflict 表示块锚点失效或选区不唯一（P3-02 接入块模型后细化）。
var ErrBlockConflict = errors.New("authoring: 块锚点与当前正文不一致")

// ErrCandidateConflict 表示同一候选收到互斥的决定（拒绝后又接受等）。
var ErrCandidateConflict = errors.New("authoring: 候选决定冲突")

// Store 管理 <runRoot>/authoring/ 下的版本、候选、确认与来源记录。
type Store struct {
	dir string
}

// DirFor 返回作品根对应的 authoring 目录（未创建也返回）。
func DirFor(runRoot string) string {
	return filepath.Join(runRoot, project.AuthoringDirName)
}

// Open 打开已有 authoring 存储；目录不存在时按需惰性创建由各写操作负责。
func Open(runRoot string) *Store {
	return &Store{dir: DirFor(runRoot)}
}

// Dir 返回存储目录路径。
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// TextVersion 是一个文本目标（场景/全文）的不可变版本：内容寻址，
// 父指针构成版本链。
type TextVersion struct {
	SchemaVersion   string `json:"schema_version"`
	TargetID        string `json:"target_id"`
	VersionID       string `json:"version_id"`
	ParentVersionID string `json:"parent_version_id,omitempty"`
	Content         string `json:"content"`
	ContentDigest   string `json:"content_digest"`
	Origin          string `json:"origin"`
	Author          string `json:"author"`
	CreatedAt       string `json:"created_at"`
}

// ComputeVersionID 由目标、父版本与内容决定版本地址。
func ComputeVersionID(targetID, parentVersionID, content string) string {
	digest := domain.ComputeArcArtifactSHA256([]byte(targetID + "\x00" + parentVersionID + "\x00" + content))
	return "ver-" + strings.TrimPrefix(digest, "sha256-")
}

func (v *TextVersion) validate() error {
	if v.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("authoring: 版本 schema_version %q 不支持", v.SchemaVersion)
	}
	if v.TargetID == "" || v.VersionID == "" {
		return fmt.Errorf("authoring: 版本缺少 target_id/version_id")
	}
	if want := ComputeVersionID(v.TargetID, v.ParentVersionID, v.Content); v.VersionID != want {
		return fmt.Errorf("authoring: 版本地址 %s 与内容不一致", v.VersionID)
	}
	if v.ContentDigest != domain.ComputeArcArtifactSHA256([]byte(v.Content)) {
		return fmt.Errorf("authoring: 版本 %s 内容摘要不一致（恢复校验失败）", v.VersionID)
	}
	return nil
}

// CommitInitial 为文本目标写入首个版本。同内容重试幂等；同目标不同
// 初始内容冲突。
func (s *Store) CommitInitial(targetID, content, author string) (*TextVersion, error) {
	if strings.TrimSpace(targetID) == "" {
		return nil, fmt.Errorf("authoring: target_id 不能为空")
	}
	version := &TextVersion{
		SchemaVersion: SchemaVersionV1,
		TargetID:      targetID,
		VersionID:     ComputeVersionID(targetID, "", content),
		Content:       content,
		ContentDigest: domain.ComputeArcArtifactSHA256([]byte(content)),
		Origin:        OriginInitial,
		Author:        author,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	// 一个目标只允许一次初始版本：已有任何版本时，同内容幂等返回，
	// 不同内容显式冲突，绝不静默分叉版本链。
	if head, headErr := s.Head(targetID); headErr == nil && head.VersionID != version.VersionID {
		return nil, fmt.Errorf("authoring: 目标 %s 已有初始版本 %s，拒绝不同内容的初始提交", targetID, head.VersionID)
	}
	path := s.versionPath(targetID, version.VersionID)
	if existing, statErr := os.ReadFile(path); statErr == nil {
		var have TextVersion
		if err := json.Unmarshal(existing, &have); err != nil || have.VersionID != version.VersionID {
			return nil, fmt.Errorf("authoring: 初始版本 %s 已存在且内容不同", version.VersionID)
		}
		return &have, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := s.writeRecord(path, version); err != nil {
		return nil, err
	}
	if err := s.updateHead(targetID, version.VersionID); err != nil {
		return nil, err
	}
	return version, nil
}

// Head 返回文本目标当前版本；无任何版本时返回 os.ErrNotExist 包裹错误。
func (s *Store) Head(targetID string) (*TextVersion, error) {
	raw, err := os.ReadFile(s.headPath(targetID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("authoring: 目标 %s 尚无版本: %w", targetID, os.ErrNotExist)
		}
		return nil, err
	}
	var head struct {
		VersionID string `json:"version_id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("authoring: head.json 解析失败: %w", err)
	}
	version, err := s.LoadVersion(targetID, head.VersionID)
	if err != nil {
		return nil, fmt.Errorf("authoring: head 指向的版本不可恢复: %w", err)
	}
	return version, nil
}

// LoadVersion 读取并校验一个版本记录。
func (s *Store) LoadVersion(targetID, versionID string) (*TextVersion, error) {
	raw, err := os.ReadFile(s.versionPath(targetID, versionID))
	if err != nil {
		return nil, fmt.Errorf("authoring: 版本 %s 读取失败: %w", versionID, err)
	}
	var version TextVersion
	if err := json.Unmarshal(raw, &version); err != nil {
		return nil, fmt.Errorf("authoring: 版本 %s 解析失败: %w", versionID, err)
	}
	if err := version.validate(); err != nil {
		return nil, err
	}
	if version.TargetID != targetID {
		return nil, fmt.Errorf("authoring: 版本 %s 属于目标 %s，与请求 %s 不符", versionID, version.TargetID, targetID)
	}
	return &version, nil
}

// AncestorVersions 返回从指定版本回溯到根的版本 ID 链（含自身）。
// 链上任一环损坏都显式报错。
func (s *Store) AncestorVersions(targetID, versionID string) ([]string, error) {
	chain := []string{}
	current := versionID
	for current != "" {
		version, err := s.LoadVersion(targetID, current)
		if err != nil {
			return nil, err
		}
		chain = append(chain, current)
		current = version.ParentVersionID
	}
	return chain, nil
}

func (s *Store) versionPath(targetID, versionID string) string {
	return filepath.Join(s.dir, "targets", targetID, "versions", versionID+".json")
}

func (s *Store) headPath(targetID string) string {
	return filepath.Join(s.dir, "targets", targetID, "head.json")
}

// updateHead 原子替换 head 指针。
func (s *Store) updateHead(targetID, versionID string) error {
	if _, err := s.LoadVersion(targetID, versionID); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(map[string]string{"version_id": versionID}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := s.headPath(targetID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readJSON 读取并解析一个 JSON 记录文件。
func readJSON[T any](path string) (*T, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("authoring: %s 读取失败: %w", filepath.Base(path), err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("authoring: %s 解析失败: %w", filepath.Base(path), err)
	}
	return &value, nil
}

// writeRecord 以 no-clobber 语义写入不可变 JSON 记录。
func (s *Store) writeRecord(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", raw, 0o644); err != nil {
		return err
	}
	if err := os.Link(path+".tmp", path); err != nil {
		os.Remove(path + ".tmp")
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	return os.Remove(path + ".tmp")
}
