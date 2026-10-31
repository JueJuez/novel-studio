package authoring

import (
	"errors"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

func blocksContent() string {
	return "第一段：雨夜的路口。\n\n第二段：老周把车停在路边，点了火。\n\n第三段：那位乘客报出了码头站。"
}

func TestSplitBlocksNormalizesAndStabilizesIDs(t *testing.T) {
	lf := SplitBlocks("第一段。\n\n第二段。")
	crlf := SplitBlocks("第一段。\r\n\r\n第二段。")
	if len(lf) != 2 || len(crlf) != 2 {
		t.Fatalf("切分数量错误: %d / %d", len(lf), len(crlf))
	}
	for i := range lf {
		if lf[i].ID != crlf[i].ID || lf[i].Text != crlf[i].Text {
			t.Fatalf("CRLF 归一失败: %+v vs %+v", lf[i], crlf[i])
		}
	}
	// 中文标点与段内单换行保留；空段落不产生块。
	mixed := SplitBlocks("问：车停在哪？\n答：路口。\n\n　\n\n第三段，全角标点不切分。")
	if len(mixed) != 2 {
		t.Fatalf("空段落/标点处理错误: %d", len(mixed))
	}
	if !strings.Contains(mixed[0].Text, "？") || !strings.Contains(mixed[1].Text, "。") {
		t.Fatalf("块内标点丢失: %+v", mixed)
	}
	// 同文本同 ID。
	if ComputeBlockID("一样。") != ComputeBlockID("一样。") {
		t.Fatal("块 ID 不稳定")
	}
}

func TestFindBlockAmbiguity(t *testing.T) {
	content := "重复段落。\n\n别的段落。\n\n重复段落。"
	if _, err := FindBlock(content, ComputeBlockID("重复段落。")); !errors.Is(err, ErrAmbiguousBlock) {
		t.Fatalf("重复段落未被判定不唯一: %v", err)
	}
	if _, err := FindBlock(content, ComputeBlockID("别的段落。")); err != nil {
		t.Fatalf("唯一段落定位失败: %v", err)
	}
	if _, err := FindBlock(content, ComputeBlockID("不存在的段落。")); err == nil {
		t.Fatal("不存在的块未被拒绝")
	}
}

func TestReviseBlockCandidateDeterministicReassembly(t *testing.T) {
	s := Open(t.TempDir())
	v1, err := s.CommitInitial("scene-1", blocksContent(), "author")
	if err != nil {
		t.Fatal(err)
	}
	anchor, block, err := s.AnchorBlockFromContent("scene-1", v1.VersionID, ComputeBlockID("第二段：老周把车停在路边，点了火。"))
	if err != nil {
		t.Fatal(err)
	}
	req := ModifyRequest{TargetID: "scene-1", BaseVersionID: v1.VersionID, AnchorID: anchor.AnchorID}
	candidate, diff, err := s.ReviseBlockCandidate("scene-1", req, block.ID, "第二段：老周把车滑进路边阴影，熄了火。", "ai", "重写第二段")
	if err != nil {
		t.Fatal(err)
	}
	if diff.OtherBlocks != 2 || diff.BlockID != block.ID {
		t.Fatalf("diff 不符: %+v", diff)
	}
	// 重组确定性：第一、三段逐字节不变，仅目标块替换。
	newBlocks := SplitBlocks(candidate.Content)
	if len(newBlocks) != 3 {
		t.Fatalf("重组后块数错误: %d", len(newBlocks))
	}
	if newBlocks[0].Text != "第一段：雨夜的路口。" || newBlocks[2].Text != "第三段：那位乘客报出了码头站。" {
		t.Fatalf("未选区受到影响: %+v", newBlocks)
	}
	if newBlocks[1].Text != "第二段：老周把车滑进路边阴影，熄了火。" {
		t.Fatalf("目标块未替换: %s", newBlocks[1].Text)
	}

	// 接受候选推进 head；随后过期基线（并发旧请求）→ 显式版本冲突。
	v2, err := s.Accept("scene-1", candidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	staleReq := ModifyRequest{TargetID: "scene-1", BaseVersionID: v1.VersionID, AnchorID: anchor.AnchorID}
	if _, _, err := s.ReviseBlockCandidate("scene-1", staleReq, block.ID, "再来一版", "ai", "x"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("过期基线未被拒绝: %v", err)
	}
	// 锚点摘要被改 → 块冲突（以当前 head 为基线才能命中锚点校验）。
	forgedAnchor, err := s.AnchorBlock("scene-1", v2.VersionID, "forged-block", domain.ComputeArcArtifactSHA256([]byte("伪造")))
	if err != nil {
		t.Fatal(err)
	}
	forgedReq := ModifyRequest{TargetID: "scene-1", BaseVersionID: v2.VersionID, AnchorID: forgedAnchor.AnchorID}
	if _, _, err := s.ReviseBlockCandidate("scene-1", forgedReq, "forged-block", "x", "ai", "x"); err == nil {
		t.Fatal("伪造锚点未被拒绝")
	}
}

func TestInsertAfterAcceptSequenceAndRevert(t *testing.T) {
	s := Open(t.TempDir())
	v1, err := s.CommitInitial("scene-1", blocksContent(), "author")
	if err != nil {
		t.Fatal(err)
	}
	anchor, block, err := s.AnchorBlockFromContent("scene-1", v1.VersionID, ComputeBlockID("第二段：老周把车停在路边，点了火。"))
	if err != nil {
		t.Fatal(err)
	}
	req := ModifyRequest{TargetID: "scene-1", BaseVersionID: v1.VersionID, AnchorID: anchor.AnchorID}

	// 续写：在第二段后插入新块。
	insertCandidate, _, err := s.InsertBlockAfterCandidate("scene-1", req, block.ID, "第二段半：雨更大了。", "ai", "续写")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.Accept("scene-1", insertCandidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(v2.Content, "\n\n") + 1; got != 4 {
		t.Fatalf("插入后块数错误: %d", got)
	}

	// 逐项接受：候选按 head 演进顺序提议；序列中已接受项幂等返回、
	// 待决项正常应用。
	head, _ := s.Head("scene-1")
	anchor2, block2, err := s.AnchorBlockFromContent("scene-1", head.VersionID, ComputeBlockID("第一段：雨夜的路口。"))
	if err != nil {
		t.Fatal(err)
	}
	candA, _, err := s.ReviseBlockCandidate("scene-1", ModifyRequest{TargetID: "scene-1", BaseVersionID: head.VersionID, AnchorID: anchor2.AnchorID},
		block2.ID, "第一段：雨夜更深的路口。", "ai", "a")
	if err != nil {
		t.Fatal(err)
	}
	afterA, err := s.Accept("scene-1", candA.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	head2, _ := s.Head("scene-1")
	anchor3, block3, err := s.AnchorBlockFromContent("scene-1", head2.VersionID, ComputeBlockID("第三段：那位乘客报出了码头站。"))
	if err != nil {
		t.Fatal(err)
	}
	candB, _, err := s.ReviseBlockCandidate("scene-1", ModifyRequest{TargetID: "scene-1", BaseVersionID: head2.VersionID, AnchorID: anchor3.AnchorID},
		block3.ID, "第三段：乘客报出的码头站十年前就拆了。", "ai", "b")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.AcceptSequence("scene-1", []string{candA.CandidateID, candB.CandidateID})
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].VersionID != afterA.VersionID {
		t.Fatalf("逐项接受版本数/幂等不符: %+v", versions)
	}
	final, _ := s.Head("scene-1")
	if final.VersionID != versions[1].VersionID || !strings.Contains(final.Content, "雨夜更深的路口") || !strings.Contains(final.Content, "十年前就拆了") {
		t.Fatalf("逐项接受最终状态错误: %s", final.Content)
	}

	// 乱序接受（基线已落后）→ 冲突：直接以旧版本为基线登记候选
	//（Propose 只要求基线存在；过期在 Accept 处显式拒绝）。
	candStale, err := s.Propose("scene-1", v2.VersionID, "ai", "stale", "第一段：过期基线的修改。")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptSequence("scene-1", []string{candStale.CandidateID}); err == nil {
		t.Fatal("过期基线逐项接受未被拒绝")
	}

	// 版本回退成新草稿：内容回到 v1，版本全新，历史不覆盖。
	reverted, err := s.RevertToVersion("scene-1", v1.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Content != v1.Content || reverted.VersionID == v1.VersionID {
		t.Fatalf("回退结果错误: %+v", reverted)
	}
	chain, err := s.AncestorVersions("scene-1", reverted.VersionID)
	if err != nil || len(chain) < 4 {
		t.Fatalf("回退后历史链不完整: %d err=%v", len(chain), err)
	}
	// 幂等：回退到当前版本原样返回。
	again, err := s.RevertToVersion("scene-1", reverted.VersionID)
	if err != nil || again.VersionID != reverted.VersionID {
		t.Fatalf("重复回退不幂等: %v", err)
	}
}

func TestDiffParagraphsOps(t *testing.T) {
	oldContent := "第一段。\n\n第二段。\n\n第三段。"
	newContent := "第一段。\n\n第二段改了。\n\n第三段。\n\n新增段。"
	ops := DiffParagraphs(oldContent, newContent)
	summary := SummarizeDiff(ops)
	if summary.Kept != 2 || summary.Deleted != 1 || summary.Inserted != 2 {
		t.Fatalf("diff 汇总错误: %+v ops=%+v", summary, ops)
	}
	if summary.Unchanged {
		t.Fatal("有差异却报 unchanged")
	}
	// CRLF 与 LF 等价：diff 为空。
	if ops := DiffParagraphs("第一段。\r\n\r\n第二段。", "第一段。\n\n第二段。"); !SummarizeDiff(ops).Unchanged {
		t.Fatalf("CRLF 归一失败: %+v", ops)
	}
	// 相同内容 → unchanged。
	if ops := DiffParagraphs(oldContent, oldContent); !SummarizeDiff(ops).Unchanged {
		t.Fatal("相同内容不应有差异")
	}
}
