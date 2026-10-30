package authoring

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

// ModifyRequest 是一次定向修改请求的合同（§6.3 约束 1）：绑定作品内
// 文本目标、来源版本与稳定块锚点。版本不匹配与锚点失效都显式报错，
// 绝不静默退化成整章改写。块内容的精确定位与选区唯一性校验在 P3-02
// 的块模型落地；本合同先固定请求必须携带的绑定字段。
type ModifyRequest struct {
	TargetID      string `json:"target_id"`
	BaseVersionID string `json:"base_version_id"` // 请求所见正文版本
	AnchorID      string `json:"anchor_id,omitempty"`
}

// BlockAnchor 把一个稳定块 ID 绑定到确切版本与块内容摘要。请求方携带
// 旧锚点时被拒绝（对应“旧浏览器页面”“段落漂移”冲突）。
type BlockAnchor struct {
	SchemaVersion string `json:"schema_version"`
	AnchorID      string `json:"anchor_id"`
	TargetID      string `json:"target_id"`
	VersionID     string `json:"version_id"`
	BlockID       string `json:"block_id"`
	Digest        string `json:"digest"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// AnchorBlock 登记一个块锚点。锚点绑定（目标，块 ID，版本，摘要）四元组：
// 同一绑定重放幂等；正文推进后同一块 ID 对新版本重新锚定会产生新锚点，
// 旧锚点保留用于冲突检测（记录不可变）。
func (s *Store) AnchorBlock(targetID, versionID, blockID, digest string) (*BlockAnchor, error) {
	if strings.TrimSpace(blockID) == "" || strings.TrimSpace(digest) == "" {
		return nil, fmt.Errorf("authoring: 块锚点需要 block_id 与 digest")
	}
	if _, err := s.LoadVersion(targetID, versionID); err != nil {
		return nil, err
	}
	anchorID := "blk-" + strings.TrimPrefix(domain.ComputeArcArtifactSHA256([]byte(targetID+"\x00"+blockID+"\x00"+versionID)), "sha256-")
	anchor := &BlockAnchor{
		SchemaVersion: SchemaVersionV1,
		AnchorID:      anchorID,
		TargetID:      targetID,
		VersionID:     versionID,
		BlockID:       blockID,
		Digest:        digest,
	}
	path := s.anchorPath(targetID, anchor.AnchorID)
	if err := s.writeRecord(path, anchor); err != nil {
		return nil, err
	}
	return anchor, nil
}

// ValidateModification 校验一次修改请求：来源版本必须是当前正文；
// 携带锚点时锚点必须存在、属于同一目标、且绑定版本就是当前版本。
func (s *Store) ValidateModification(req ModifyRequest) error {
	head, err := s.Head(req.TargetID)
	if err != nil {
		return err
	}
	if req.BaseVersionID != head.VersionID {
		return fmt.Errorf("%w: 请求基线 %s，当前正文 %s", ErrVersionConflict, req.BaseVersionID, head.VersionID)
	}
	if req.AnchorID == "" {
		return nil
	}
	anchor, err := s.loadAnchor(req.TargetID, req.AnchorID)
	if err != nil {
		return err
	}
	if anchor.VersionID != head.VersionID {
		return fmt.Errorf("%w: 锚点 %s 绑定版本 %s，当前正文 %s", ErrBlockConflict, req.AnchorID, anchor.VersionID, head.VersionID)
	}
	return nil
}

func (s *Store) anchorPath(targetID, anchorID string) string {
	return filepath.Join(s.dir, "targets", targetID, "anchors", anchorID+".json")
}

func (s *Store) loadAnchor(targetID, anchorID string) (*BlockAnchor, error) {
	return readJSON[BlockAnchor](s.anchorPath(targetID, anchorID))
}
