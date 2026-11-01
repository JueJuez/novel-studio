package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
)

// longGateWaitMessagePattern 是 requireLongAuthorGate 等待消息的形状，
// 与 internal/workbench/advance.go 的 longGateWaitPattern 保持一致：
// workbench 依据该片段识别「等待作者」并提取门禁目标/版本。
var longGateWaitMessagePattern = regexp.MustCompile(`目标 (\S+) 版本 (\S+) 尚无作者确认`)

func setupGateFixture(t *testing.T, collaborative bool, withManifest bool) string {
	t.Helper()
	base := t.TempDir()
	engineDir := filepath.Join(base, "output", "novel")
	if err := os.MkdirAll(engineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if withManifest {
		if _, err := project.Create(base, project.CreateOptions{
			Title:         "协作长篇",
			PipelineKind:  project.PipelineLong,
			Collaborative: collaborative,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func TestLongSealAndPromoteGateKeys(t *testing.T) {
	// 封存目标键随代次 ID 变化；同代次稳定。
	if longSealGateTarget("gen-a") == longSealGateTarget("gen-b") {
		t.Fatal("不同封存代次不应共享目标键")
	}
	if longSealGateTarget("gen-a") != longSealGateTarget("gen-a") {
		t.Fatal("同代次目标键应稳定")
	}
	// 提升目标键绑定代次与章节。
	if longPromoteGateTarget("gen-a", 3) == longPromoteGateTarget("gen-a", 4) ||
		longPromoteGateTarget("gen-a", 3) == longPromoteGateTarget("gen-b", 3) {
		t.Fatal("提升目标键应区分代次与章节")
	}
	// 确认版本键确定性：同输入同键，bundle 或计划任一变化即换键。
	if longPromoteGateVersion("b1", "p1") != longPromoteGateVersion("b1", "p1") {
		t.Fatal("版本键应确定")
	}
	if longPromoteGateVersion("b1", "p1") == longPromoteGateVersion("b2", "p1") ||
		longPromoteGateVersion("b1", "p1") == longPromoteGateVersion("b1", "p2") {
		t.Fatal("bundle 或计划变化后确认版本键必须变化（旧确认失效）")
	}
}

func TestDeriveLongProjectRootLayouts(t *testing.T) {
	// 标准布局 + manifest。
	root := setupGateFixture(t, true, true)
	got, ok := deriveLongProjectRoot(filepath.Join(root, "output", "novel"))
	if !ok || got != root {
		t.Fatalf("标准布局推导失败: %q ok=%v", got, ok)
	}
	// 非 novel 叶子目录。
	if _, ok := deriveLongProjectRoot(filepath.Join(root, "output", "other")); ok {
		t.Fatal("非 novel 目录不应启用门禁")
	}
	// 无 manifest。
	noManifest := setupGateFixture(t, true, false)
	if _, ok := deriveLongProjectRoot(filepath.Join(noManifest, "output", "novel")); ok {
		t.Fatal("无 manifest 不应启用门禁")
	}
	// 自定义 OutputDir（根目录直接当引擎目录）。
	if _, ok := deriveLongProjectRoot(root); ok {
		t.Fatal("非标准布局不应启用门禁")
	}
}

func TestRequireLongAuthorGateScenarios(t *testing.T) {
	const target = "long-chapter-3"
	const version = "render-body-test"

	// 旧工程（无 manifest）：行为不变，门禁不启用。
	legacyRoot := setupGateFixture(t, true, false)
	if err := requireLongAuthorGate(filepath.Join(legacyRoot, "output", "novel"), target, version); err != nil {
		t.Fatalf("旧工程不应触发门禁: %v", err)
	}

	// 非协作策略：行为不变。
	plainRoot := setupGateFixture(t, false, true)
	if err := requireLongAuthorGate(filepath.Join(plainRoot, "output", "novel"), target, version); err != nil {
		t.Fatalf("非协作策略不应触发门禁: %v", err)
	}

	// 协作长篇：未确认 → 哨兵错误（等待作者，不占锁）。
	collabRoot := setupGateFixture(t, true, true)
	engineDir := filepath.Join(collabRoot, "output", "novel")
	err := requireLongAuthorGate(engineDir, target, version)
	if !errors.Is(err, ErrLongAuthorConfirmationRequired) {
		t.Fatalf("协作未确认应返回哨兵错误: %v", err)
	}
	// 双向钉住 workbench 的等待作者识别契约（internal/workbench/advance.go
	// 的 longGateWaitPattern 依赖本消息片段提取门禁目标/版本）。
	if match := longGateWaitMessagePattern.FindStringSubmatch(err.Error()); match == nil ||
		match[1] != target || match[2] != version {
		t.Fatalf("门禁消息格式偏离 workbench 识别契约: %q", err.Error())
	}

	// 确认落盘 → 放行；换版本（正文变化）→ 再次等待。
	auth := authoring.Open(collabRoot)
	if _, err := auth.RecordExternalAuthorApproval(target, version, "P4-03 确认"); err != nil {
		t.Fatal(err)
	}
	if err := requireLongAuthorGate(engineDir, target, version); err != nil {
		t.Fatalf("确认后仍被拦截: %v", err)
	}
	if err := requireLongAuthorGate(engineDir, target, version+"-reworked"); !errors.Is(err, ErrLongAuthorConfirmationRequired) {
		t.Fatalf("版本变化后旧确认不应命中: %v", err)
	}

	// 门禁读取失败 → fail-closed（拒绝推进）。
	if err := os.RemoveAll(filepath.Join(collabRoot, "authoring")); err != nil {
		t.Fatal(err)
	}
	// 删除后确认记录不存在 → 回到等待态（而非错误）；再制造读取失败：
	// approvals 目录变成文件使 ReadDir 报错。
	if err := os.MkdirAll(filepath.Join(collabRoot, "authoring", "targets", target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collabRoot, "authoring", "targets", target, "approvals"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := requireLongAuthorGate(engineDir, target, version); err == nil || errors.Is(err, ErrLongAuthorConfirmationRequired) {
		t.Fatalf("门禁读取失败应 fail-closed: %v", err)
	}
}
