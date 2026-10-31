package authoring

import (
	"fmt"
)

// 本文件实现 P3-02 的差异与批量接受/回退合同（行为规格参考 Pagekin
// 修稿交互，独立实现，见 docs/dual-pipeline-porting-audit.md P1 记录）：
// 段落级 diff（rune 安全、中文标点/换行不敏感）、逐项接受、版本回退成
// 新草稿（历史不可变，回退产生全新版本）。

// DiffOp 是段落级差异操作。
const (
	DiffKeep   = "keep"
	DiffDelete = "delete"
	DiffInsert = "insert"
)

// DiffOp 是一段保持/删除/插入的文本。
type DiffOp struct {
	Kind string
	Text string
}

// DiffParagraphs 对两版正文做段落级 LCS 差异。段落切分与 SplitBlocks
// 同口径（\r\n 归一、空行分隔），中文标点在段内不参与切分。
func DiffParagraphs(oldContent, newContent string) []DiffOp {
	oldBlocks := blockTexts(oldContent)
	newBlocks := blockTexts(newContent)
	// LCS DP：段落规模（短篇数十段）下 O(n*m) 足够。
	n, m := len(oldBlocks), len(newBlocks)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldBlocks[i] == newBlocks[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	ops := []DiffOp{}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldBlocks[i] == newBlocks[j]:
			ops = append(ops, DiffOp{Kind: DiffKeep, Text: oldBlocks[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, DiffOp{Kind: DiffDelete, Text: oldBlocks[i]})
			i++
		default:
			ops = append(ops, DiffOp{Kind: DiffInsert, Text: newBlocks[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, DiffOp{Kind: DiffDelete, Text: oldBlocks[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, DiffOp{Kind: DiffInsert, Text: newBlocks[j]})
	}
	return ops
}

// DiffSummary 统计差异规模。
type DiffSummary struct {
	Kept      int  `json:"kept"`
	Deleted   int  `json:"deleted"`
	Inserted  int  `json:"inserted"`
	Unchanged bool `json:"unchanged"`
}

// SummarizeDiff 汇总 diff。
func SummarizeDiff(ops []DiffOp) DiffSummary {
	summary := DiffSummary{}
	for _, op := range ops {
		switch op.Kind {
		case DiffKeep:
			summary.Kept++
		case DiffDelete:
			summary.Deleted++
		case DiffInsert:
			summary.Inserted++
		}
	}
	summary.Unchanged = summary.Deleted == 0 && summary.Inserted == 0
	return summary
}

func blockTexts(content string) []string {
	blocks := SplitBlocks(content)
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		texts = append(texts, block.Text)
	}
	return texts
}

// AcceptSequence 逐项接受多个候选：候选按给出顺序接受，每一项都必须
// 以当前 head 为基线（前一项接受后的版本），基线不匹配显式冲突。
// 返回每项接受产生的版本；最终 head 只推进一次状态链（每项各一版）。
func (s *Store) AcceptSequence(targetID string, candidateIDs []string) ([]*TextVersion, error) {
	versions := make([]*TextVersion, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		version, err := s.Accept(targetID, candidateID)
		if err != nil {
			return versions, fmt.Errorf("authoring: 逐项接受 %s 中断: %w", candidateID, err)
		}
		versions = append(versions, version)
	}
	return versions, nil
}

// RevertToVersion 把目标回退到某个历史版本的内容：以当前 head 为基线
// 登记 manual 候选并接受——head 内容回到历史版本，但版本 ID 全新，
// 历史链不可变、不覆盖（"版本回退成新草稿"）。回退到当前版本幂等返回。
func (s *Store) RevertToVersion(targetID, versionID string) (*TextVersion, error) {
	target, err := s.LoadVersion(targetID, versionID)
	if err != nil {
		return nil, err
	}
	head, err := s.Head(targetID)
	if err != nil {
		return nil, err
	}
	if head.VersionID == target.VersionID {
		return head, nil
	}
	candidate, err := s.Propose(targetID, head.VersionID, "manual", "revert-to:"+target.VersionID, target.Content)
	if err != nil {
		return nil, err
	}
	return s.Accept(targetID, candidate.CandidateID)
}
