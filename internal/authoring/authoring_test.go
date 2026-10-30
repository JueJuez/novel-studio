package authoring

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

const testTarget = "scene-1"

func openStore(t *testing.T) *Store {
	t.Helper()
	return Open(t.TempDir())
}

func commitRoot(t *testing.T, s *Store, content string) *TextVersion {
	t.Helper()
	version, err := s.CommitInitial(testTarget, content, "author")
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestVersionChainCommitHeadAndTamperDetection(t *testing.T) {
	s := openStore(t)
	v1 := commitRoot(t, s, "第一版正文。")

	head, err := s.Head(testTarget)
	if err != nil || head.VersionID != v1.VersionID {
		t.Fatalf("head=%v err=%v", head, err)
	}
	// 同内容重试幂等。
	retry, err := s.CommitInitial(testTarget, "第一版正文。", "author")
	if err != nil || retry.VersionID != v1.VersionID {
		t.Fatalf("初始提交重试不幂等: %v %v", retry, err)
	}
	// 不同初始内容冲突。
	if _, err := s.CommitInitial(testTarget, "另一个开头。", "author"); err == nil || !strings.Contains(err.Error(), "拒绝不同内容") {
		t.Fatalf("异内容初始提交未被拒绝: %v", err)
	}
	// 篡改版本文件 → 恢复校验失败。
	versionPath := filepath.Join(s.Dir(), "targets", testTarget, "versions", v1.VersionID+".json")
	if err := os.WriteFile(versionPath, []byte(`{"schema_version":"authoring.v1","target_id":"scene-1","version_id":"ver-x","content":"篡改"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(testTarget); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("篡改版本未被恢复校验拦截: %v", err)
	}
}

func TestCandidateAcceptRejectVersionConflictAndIdempotency(t *testing.T) {
	s := openStore(t)
	v1 := commitRoot(t, s, "第一版正文。")

	// 候选登记幂等：同内容重试同一候选 ID。
	candA, err := s.Propose(testTarget, v1.VersionID, "ai", "rewrite-scene-1-turn", "第二版正文，加了转折。")
	if err != nil {
		t.Fatal(err)
	}
	candA2, err := s.Propose(testTarget, v1.VersionID, "ai", "rewrite-scene-1-turn", "第二版正文，加了转折。")
	if err != nil || candA2.CandidateID != candA.CandidateID {
		t.Fatalf("候选登记不幂等: %s vs %s err=%v", candA2.CandidateID, candA.CandidateID, err)
	}

	v2, err := s.Accept(testTarget, candA.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if v2.ParentVersionID != v1.VersionID || v2.Origin != OriginCandidate+":"+candA.CandidateID {
		t.Fatalf("接受产生的版本链不正确: %+v", v2)
	}
	// 重复接受幂等：head 不再推进、版本数不变。
	again, err := s.Accept(testTarget, candA.CandidateID)
	if err != nil || again.VersionID != v2.VersionID {
		t.Fatalf("重复接受不幂等: %v err=%v", again, err)
	}
	chain, err := s.AncestorVersions(testTarget, v2.VersionID)
	if err != nil || len(chain) != 2 {
		t.Fatalf("祖先链=%v err=%v", chain, err)
	}

	// 旧版本冲突：以 v1 为基线的新候选在 head=v2 时拒绝接受。
	candB, err := s.Propose(testTarget, v1.VersionID, "manual", "late-edit", "基于旧版本的修改。")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(testTarget, candB.CandidateID); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("旧基线接受未被拒绝: %v", err)
	}

	// 候选拒绝：不改变正文；重复拒绝幂等；拒绝后接受被拒。
	candC, err := s.Propose(testTarget, v2.VersionID, "ai", "polish", "第三版润色。")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(testTarget, candC.CandidateID, "语气不对"); err != nil {
		t.Fatal(err)
	}
	if head, _ := s.Head(testTarget); head.VersionID != v2.VersionID {
		t.Fatalf("拒绝改变了正文: %s", head.VersionID)
	}
	if err := s.Reject(testTarget, candC.CandidateID, "重复拒绝"); err != nil {
		t.Fatalf("重复拒绝不幂等: %v", err)
	}
	if _, err := s.Accept(testTarget, candC.CandidateID); !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("拒绝后接受未被拒绝: %v", err)
	}
	if got := s.Decision(testTarget, candC.CandidateID); got != "rejected" {
		t.Fatalf("decision=%q", got)
	}
}

func TestAuthorApprovalInvalidatesOnAdvanceAndModelAcceptIndependent(t *testing.T) {
	s := openStore(t)
	v1 := commitRoot(t, s, "第一版正文。")

	// 模型通过 ≠ 作者确认：两者分别记账。
	if _, err := s.RecordModelAccept(testTarget, v1.VersionID, "deepseek/deepseek-v4-flash", "rev-digest"); err != nil {
		t.Fatal(err)
	}
	state, err := s.AuthorApprovalForHead(testTarget)
	if err != nil || state.Approved {
		t.Fatalf("模型通过不应使作者确认生效: %+v err=%v", state, err)
	}
	hasAccept, err := s.HasModelAccept(testTarget, v1.VersionID)
	if err != nil || !hasAccept {
		t.Fatalf("模型接受记录缺失: %v err=%v", hasAccept, err)
	}

	// 作者确认 v1 生效；重复确认幂等。
	approval, err := s.RecordAuthorApproval(testTarget, v1.VersionID, "", "规则摘要 v1", "作者通过")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.RecordAuthorApproval(testTarget, v1.VersionID, "", "规则摘要 v1", "作者通过")
	if err != nil || replay.ApprovalID != approval.ApprovalID {
		t.Fatalf("确认重放不幂等: %v err=%v", replay, err)
	}
	if state, err = s.AuthorApprovalForHead(testTarget); err != nil || !state.Approved {
		t.Fatalf("v1 确认未生效: %+v err=%v", state, err)
	}

	// 正文推进后旧确认自动失效，但模型接受的历史记录仍在。
	cand, err := s.Propose(testTarget, v1.VersionID, "ai", "task", "第二版正文。")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.Accept(testTarget, cand.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err = s.AuthorApprovalForHead(testTarget); err != nil || state.Approved {
		t.Fatalf("head 推进后旧确认未失效: %+v err=%v", state, err)
	}
	if ok, _ := s.HasModelAccept(testTarget, v1.VersionID); !ok {
		t.Fatal("历史模型接受记录不应消失")
	}
	// v2 重新确认后恢复生效；未确认的版本不可确认不存在的内容。
	if _, err := s.RecordAuthorApproval(testTarget, "ver-不存在", "", "", ""); err == nil {
		t.Fatal("不存在的版本被确认")
	}
	if _, err := s.RecordAuthorApproval(testTarget, v2.VersionID, cand.CandidateID, "规则摘要 v2", ""); err != nil {
		t.Fatal(err)
	}
	if state, err = s.AuthorApprovalForHead(testTarget); err != nil || !state.Approved || state.Record.VersionID != v2.VersionID {
		t.Fatalf("v2 确认未生效: %+v err=%v", state, err)
	}
}

func TestAuthoringCrashRecoveryConvergence(t *testing.T) {
	s := openStore(t)
	v1 := commitRoot(t, s, "第一版正文。")
	cand, err := s.Propose(testTarget, v1.VersionID, "ai", "task", "第二版正文。")
	if err != nil {
		t.Fatal(err)
	}

	// 崩溃点 1：版本文件已写、决定与 head 未写。重试 Accept 收敛。
	versionID := ComputeVersionID(testTarget, v1.VersionID, cand.Content)
	crashed := &TextVersion{
		SchemaVersion:   SchemaVersionV1,
		TargetID:        testTarget,
		VersionID:       versionID,
		ParentVersionID: v1.VersionID,
		Content:         cand.Content,
		ContentDigest:   domain.ComputeArcArtifactSHA256([]byte(cand.Content)),
		Origin:          OriginCandidate + ":" + cand.CandidateID,
		Author:          "ai:ai",
	}
	if err := s.writeRecord(s.versionPath(testTarget, versionID), crashed); err != nil {
		t.Fatal(err)
	}
	v2, err := s.Accept(testTarget, cand.CandidateID)
	if err != nil || v2.VersionID != versionID {
		t.Fatalf("崩溃点 1 未收敛: %v err=%v", v2, err)
	}
	if head, _ := s.Head(testTarget); head.VersionID != versionID {
		t.Fatalf("崩溃点 1 后 head 错误: %s", head.VersionID)
	}

	// 崩溃点 2：决定与版本已写、head 未推进（人为构造该中间态）。
	cand2, err := s.Propose(testTarget, versionID, "ai", "task2", "第三版正文。")
	if err != nil {
		t.Fatal(err)
	}
	v3ID := ComputeVersionID(testTarget, versionID, cand2.Content)
	crashed2 := &TextVersion{
		SchemaVersion:   SchemaVersionV1,
		TargetID:        testTarget,
		VersionID:       v3ID,
		ParentVersionID: versionID,
		Content:         cand2.Content,
		ContentDigest:   domain.ComputeArcArtifactSHA256([]byte(cand2.Content)),
		Origin:          OriginCandidate + ":" + cand2.CandidateID,
		Author:          "ai:ai",
	}
	if err := s.writeRecord(s.versionPath(testTarget, v3ID), crashed2); err != nil {
		t.Fatal(err)
	}
	acceptedPath, _ := s.decisionPaths(testTarget, cand2.CandidateID)
	if err := s.writeDecision(acceptedPath, testTarget, cand2.CandidateID, "accepted"); err != nil {
		t.Fatal(err)
	}
	if head, _ := s.Head(testTarget); head.VersionID != versionID {
		t.Fatalf("崩溃点 2 前置状态错误: head=%s", head.VersionID)
	}
	v3, err := s.Accept(testTarget, cand2.CandidateID)
	if err != nil {
		t.Fatalf("崩溃点 2 未收敛: %v", err)
	}
	if head, _ := s.Head(testTarget); head.VersionID != v3.VersionID || v3.VersionID != v3ID {
		t.Fatalf("崩溃点 2 后 head 错误: head=%s v3=%s", head.VersionID, v3.VersionID)
	}
}

func TestSourceStalenessFollowsHead(t *testing.T) {
	s := openStore(t)
	v1 := commitRoot(t, s, "第一版正文。")
	ref, err := s.RecordSource(testTarget, v1.VersionID, SourceForeshadow, 1, "blk-1", "伞骨上的划痕")
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SourceStale(testTarget, ref.SourceID)
	if err != nil || report.Stale {
		t.Fatalf("新来源不应陈旧: %+v err=%v", report, err)
	}
	// 来源重复登记幂等。
	dup, err := s.RecordSource(testTarget, v1.VersionID, SourceForeshadow, 1, "blk-1", "伞骨上的划痕")
	if err != nil || dup.SourceID != ref.SourceID {
		t.Fatalf("来源登记不幂等: %v err=%v", dup, err)
	}
	cand, err := s.Propose(testTarget, v1.VersionID, "ai", "task", "第二版正文。")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(testTarget, cand.CandidateID); err != nil {
		t.Fatal(err)
	}
	report, err = s.SourceStale(testTarget, ref.SourceID)
	if err != nil || !report.Stale || report.Reason != "version_superseded" {
		t.Fatalf("推进后来源未标记陈旧: %+v err=%v", report, err)
	}
}

func TestModifyContractConflicts(t *testing.T) {
	s := openStore(t)
	v1 := commitRoot(t, s, "第一版正文。")
	anchor, err := s.AnchorBlock(testTarget, v1.VersionID, "para-3", "sha256-abc")
	if err != nil {
		t.Fatal(err)
	}
	req := ModifyRequest{TargetID: testTarget, BaseVersionID: v1.VersionID, AnchorID: anchor.AnchorID}
	if err := s.ValidateModification(req); err != nil {
		t.Fatalf("合法修改请求被拒: %v", err)
	}

	// 版本漂移：旧基线显式冲突。
	cand, err := s.Propose(testTarget, v1.VersionID, "ai", "task", "第二版正文。")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.Accept(testTarget, cand.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateModification(ModifyRequest{TargetID: testTarget, BaseVersionID: v1.VersionID}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("旧基线未被拒绝: %v", err)
	}
	// 锚点失效：显式块冲突，不静默整章改写。
	if err := s.ValidateModification(ModifyRequest{TargetID: testTarget, BaseVersionID: v2.VersionID, AnchorID: anchor.AnchorID}); !errors.Is(err, ErrBlockConflict) {
		t.Fatalf("旧锚点未被拒绝: %v", err)
	}
	newAnchor, err := s.AnchorBlock(testTarget, v2.VersionID, "para-3", "sha256-def")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateModification(ModifyRequest{TargetID: testTarget, BaseVersionID: v2.VersionID, AnchorID: newAnchor.AnchorID}); err != nil {
		t.Fatalf("新锚点校验失败: %v", err)
	}
}
