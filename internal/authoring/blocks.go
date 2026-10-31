package authoring

import (
	"fmt"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

// 本文件实现 P3-01：从 Show Me The Story 选择性适配的块级编辑合同
//（移植记录见 docs/dual-pipeline-porting-audit.md R1/R3，源仓库
// Nigh/show-me-the-story @ f790187，MIT）。
//
// 关键边界：
//   - 块按段落切分、内容寻址；同一文本多次出现时锚定显式报
//     ErrAmbiguousBlock（选区不唯一），绝不静默选一个。
//   - 块级修改只替换目标块、其余块逐字节不变（确定性重组），不存在
//     "模型顺手整章改写" 的回退路径。
//   - 不复制 SMTS 的全局配置、项目状态与存储；仅采纳块寻址与按块
//     revise 的合同形状。

// ErrAmbiguousBlock 表示目标块文本在正文中出现多次，选区不唯一。
var ErrAmbiguousBlock = fmt.Errorf("authoring: 块文本在正文中出现多次，选区不唯一")

// Block 是一个段落块：内容寻址的稳定 ID + 原文。
type Block struct {
	ID   string
	Text string
}

// ComputeBlockID 由块文本决定稳定地址。
func ComputeBlockID(text string) string {
	return "block-" + strings.TrimPrefix(domain.ComputeArcArtifactSHA256([]byte(text)), "sha256-")
}

// SplitBlocks 把正文按段落切分：\r\n 归一为 \n，按空行分隔；块内保留
// 原文（含中文标点与单换行）。空段落不产生块。
func SplitBlocks(content string) []Block {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	paragraphs := strings.Split(normalized, "\n\n")
	blocks := make([]Block, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		text := strings.TrimSpace(paragraph)
		if text == "" {
			continue
		}
		blocks = append(blocks, Block{ID: ComputeBlockID(text), Text: text})
	}
	return blocks
}

// FindBlock 在版本内容中定位块：必须恰好出现一次（选区唯一）。
func FindBlock(content string, blockID string) (*Block, error) {
	blocks := SplitBlocks(content)
	for i := range blocks {
		if blocks[i].ID == blockID {
			// 相同文本多次出现 → 同一内容 ID 命中多次 → 选区不唯一。
			count := 0
			for j := range blocks {
				if blocks[j].ID == blockID {
					count++
				}
			}
			if count > 1 {
				return nil, fmt.Errorf("%w: block=%s 出现 %d 次", ErrAmbiguousBlock, blockID, count)
			}
			return &blocks[i], nil
		}
	}
	return nil, fmt.Errorf("authoring: 当前正文中不存在块 %s", blockID)
}

// BlockDiff 是块级修改的对照：哪些块保持、哪些块被替换。
type BlockDiff struct {
	BlockID     string `json:"block_id"`
	OldText     string `json:"old_text"`
	NewText     string `json:"new_text"`
	OtherBlocks int    `json:"other_blocks_unchanged"`
}

// ReviseBlockCandidate 执行块级定向修改：校验修改请求（版本+锚点），
// 只替换目标块并确定性重组整篇正文，登记为候选（AI 或手改）。其余块
// 逐字节不变——由重组过程构造保证，并在此复核。
func (s *Store) ReviseBlockCandidate(targetID string, req ModifyRequest, blockID, newBlockText, kind, task string) (*Candidate, *BlockDiff, error) {
	if strings.TrimSpace(newBlockText) == "" {
		return nil, nil, fmt.Errorf("authoring: 块级修改的新文本不能为空")
	}
	if kind != "ai" && kind != "manual" {
		return nil, nil, fmt.Errorf("authoring: 块级修改 kind 必须是 ai/manual")
	}
	if err := s.ValidateModification(req); err != nil {
		return nil, nil, err
	}
	head, err := s.Head(targetID)
	if err != nil {
		return nil, nil, err
	}
	blocks := SplitBlocks(head.Content)
	old, err := FindBlock(head.Content, blockID)
	if err != nil {
		return nil, nil, err
	}
	// 锚点必须就是目标块（防错位修改）。
	if req.AnchorID != "" {
		anchor, err := s.loadAnchor(targetID, req.AnchorID)
		if err != nil {
			return nil, nil, err
		}
		if anchor.BlockID != blockID {
			return nil, nil, fmt.Errorf("%w: 锚点对应块 %s，与请求块 %s 不一致", ErrBlockConflict, anchor.BlockID, blockID)
		}
		if anchor.Digest != domain.ComputeArcArtifactSHA256([]byte(old.Text)) {
			return nil, nil, fmt.Errorf("%w: 锚点摘要与当前块内容不一致", ErrBlockConflict)
		}
	}
	newText := strings.TrimSpace(newBlockText)
	rebuilt := make([]string, 0, len(blocks))
	unchanged := 0
	for _, block := range blocks {
		if block.ID == blockID {
			rebuilt = append(rebuilt, newText)
			continue
		}
		rebuilt = append(rebuilt, block.Text)
		unchanged++
	}
	newContent := strings.Join(rebuilt, "\n\n")
	// 复核：除目标块外，新内容包含的其余块逐一与旧块相同。
	for _, block := range SplitBlocks(newContent) {
		if block.ID == ComputeBlockID(newText) {
			continue
		}
		if _, err := FindBlock(head.Content, block.ID); err != nil {
			return nil, nil, fmt.Errorf("authoring: 块级修改意外影响了其它块: %w", err)
		}
	}
	diff := &BlockDiff{BlockID: blockID, OldText: old.Text, NewText: newText, OtherBlocks: unchanged}
	candidate, err := s.Propose(targetID, head.VersionID, kind, "block-revise:"+blockID+"\x00"+task, newContent)
	if err != nil {
		return nil, nil, err
	}
	return candidate, diff, nil
}

// AnchorBlockFromContent 用当前 head 内容锚定一个块：块必须存在且唯一，
// 摘要取自块内容。选区不唯一时显式失败。
func (s *Store) AnchorBlockFromContent(targetID, versionID, blockID string) (*BlockAnchor, *Block, error) {
	version, err := s.LoadVersion(targetID, versionID)
	if err != nil {
		return nil, nil, err
	}
	block, err := FindBlock(version.Content, blockID)
	if err != nil {
		return nil, nil, err
	}
	anchor, err := s.AnchorBlock(targetID, versionID, blockID, domain.ComputeArcArtifactSHA256([]byte(block.Text)))
	if err != nil {
		return nil, nil, err
	}
	return anchor, block, nil
}

// InsertBlockAfterCandidate 在指定块之后插入新块（"续写"语义）：校验
// 修改请求，确定性重组（其余块逐字节不变、新块紧跟目标块），登记候选。
func (s *Store) InsertBlockAfterCandidate(targetID string, req ModifyRequest, afterBlockID, newBlockText, kind, task string) (*Candidate, *BlockDiff, error) {
	if strings.TrimSpace(newBlockText) == "" {
		return nil, nil, fmt.Errorf("authoring: 插入块的新文本不能为空")
	}
	if kind != "ai" && kind != "manual" {
		return nil, nil, fmt.Errorf("authoring: 插入块 kind 必须是 ai/manual")
	}
	if err := s.ValidateModification(req); err != nil {
		return nil, nil, err
	}
	head, err := s.Head(targetID)
	if err != nil {
		return nil, nil, err
	}
	blocks := SplitBlocks(head.Content)
	after, err := FindBlock(head.Content, afterBlockID)
	if err != nil {
		return nil, nil, err
	}
	if req.AnchorID != "" {
		anchor, err := s.loadAnchor(targetID, req.AnchorID)
		if err != nil {
			return nil, nil, err
		}
		if anchor.BlockID != afterBlockID {
			return nil, nil, fmt.Errorf("%w: 锚点对应块 %s，与插入位置块 %s 不一致", ErrBlockConflict, anchor.BlockID, afterBlockID)
		}
	}
	newText := strings.TrimSpace(newBlockText)
	var rebuilt []string
	for _, block := range blocks {
		rebuilt = append(rebuilt, block.Text)
		if block.ID == afterBlockID {
			rebuilt = append(rebuilt, newText)
		}
	}
	newContent := strings.Join(rebuilt, "\n\n")
	// 复核：原 blocks 全部原样保留。
	for _, block := range blocks {
		if _, err := FindBlock(newContent, block.ID); err != nil {
			return nil, nil, fmt.Errorf("authoring: 插入意外影响了既有块: %w", err)
		}
	}
	diff := &BlockDiff{BlockID: afterBlockID, OldText: after.Text, NewText: newText, OtherBlocks: len(blocks)}
	candidate, err := s.Propose(targetID, head.VersionID, kind, "block-insert-after:"+afterBlockID+"\x00"+task, newContent)
	if err != nil {
		return nil, nil, err
	}
	return candidate, diff, nil
}
