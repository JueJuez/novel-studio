package shortstory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/project"
)

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func shortManifest(t *testing.T, runRoot string) *project.Manifest {
	t.Helper()
	manifest, err := project.Create(runRoot, project.CreateOptions{Title: "夜航西飞", PipelineKind: project.PipelineShort})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func sampleBrief() Brief {
	return Brief{Idea: "深夜出租车司机接到一个去往已拆除码头站的乘客。", AuthorConstraints: []string{"开放式结局", "第一人称"}}
}

func sampleStructure() Structure {
	return Structure{
		Conflict:        "司机想查明乘客身份，乘客只想在消失前完成一次告别",
		Characters:      []StructureCharacter{{Name: "老周", Desire: "弄清乘客的秘密", Choice: "在码头点燃最后一根烟"}},
		EndingDirection: "开放式：乘客消失，老周收到一条空短信",
		Promises:        []string{"空短信的来源必须在结尾可解释或被作者刻意保留"},
		TemplateName:    "",
	}
}

func sampleScenes() []Scene {
	return []Scene{
		{Index: 1, POV: "老周", Goal: "接到这单生意", Obstacle: "乘客出发点含糊不清", WordBudget: 1200, ContinuityNext: "乘客上车后报出码头站"},
		{Index: 2, POV: "老周", Goal: "套出乘客的目的", Obstacle: "乘客只回答反问", CausalDeps: []int{1}, Turn: "乘客说出老周已故女儿的小名", WordBudget: 1600, ContinuityPrev: "码头站在十年前已拆除"},
		{Index: 3, POV: "老周", Goal: "决定是否跟下去", Obstacle: "码头废址无路可走", CausalDeps: []int{2}, WordBudget: 1400},
	}
}

func TestShortStoryFullLifecycleWithReviseLoop(t *testing.T) {
	runRoot := t.TempDir()
	manifest := shortManifest(t, runRoot)
	store, state, err := Create(runRoot, manifest, sampleBrief())
	if err != nil {
		t.Fatal(err)
	}
	if state.Stage != StageBrief || state.RunState != RunActive {
		t.Fatalf("初始状态: stage=%s run=%s", state.Stage, state.RunState)
	}
	if state.History[0].Stage != StageBrief || state.History[0].Artifact == "" {
		t.Fatalf("brief 工件未随初始化落盘: %+v", state.History)
	}

	// 全正向路径 + 一次修稿循环。
	steps := []struct {
		next     Stage
		artifact any
	}{
		{StageStructure, sampleStructure()},
		{StageScenes, sampleScenes()},
		{StageDraft, SceneDraft{SceneIndex: 1, Body: "深夜十一点，老周把车停在路口。"}},
		{StageReview, ReviewReport{Target: "scene-1", Summary: "节奏偏慢"}},
		{StageDraft, SceneDraft{SceneIndex: 1, Body: "深夜十一点差一刻，老周把车滑进路口的阴影里。"}},
		{StageReview, ReviewReport{Target: "scene-1", Summary: "通过"}},
		{StageAuthorApproval, AuthorApproval{ApprovedDigest: domain.ComputeArcArtifactSHA256([]byte("scene-1-final")), Note: "作者确认"}},
		{StageFinalize, FinalizeRecord{FullTextDigest: domain.ComputeArcArtifactSHA256([]byte("scene-1-final"))}},
		{StageDelivered, DeliveryRecord{FullTextDigest: domain.ComputeArcArtifactSHA256([]byte("scene-1-final")), ExportPath: "export/夜航西飞.md"}},
	}
	for i, step := range steps {
		state, err = store.Advance(step.next, step.artifact)
		if err != nil {
			t.Fatalf("step %d advance to %s: %v", i, step.next, err)
		}
		if state.Stage != step.next {
			t.Fatalf("step %d stage=%s want %s", i, state.Stage, step.next)
		}
		// 每步之后随机做一次恢复，验证 Load 即恢复入口。
		reloaded, err := store.Load()
		if err != nil {
			t.Fatalf("step %d 恢复失败: %v", i, err)
		}
		if reloaded.Stage != step.next || len(reloaded.History) != i+2 {
			t.Fatalf("step %d 恢复不一致: stage=%s history=%d", i, reloaded.Stage, len(reloaded.History))
		}
	}

	// 修稿循环产生独立修订号工件。
	draft2 := filepath.Join(store.Dir(), "artifacts", "draft", "draft.2.json")
	if _, err := os.Stat(draft2); err != nil {
		t.Fatalf("修稿工件 draft.2.json 缺失: %v", err)
	}

	// 终态不可再推进。
	if _, err := store.Advance(StageFinalize, FinalizeRecord{}); err == nil || !strings.Contains(err.Error(), "不能从 delivered") {
		t.Fatalf("delivered 终态未被强制: %v", err)
	}
}

func TestShortStoryRejectsIllegalTransitions(t *testing.T) {
	runRoot := t.TempDir()
	manifest := shortManifest(t, runRoot)
	store, _, err := Create(runRoot, manifest, sampleBrief())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(StageScenes, sampleScenes()); err == nil {
		t.Fatal("brief→scenes 跳步未被拒绝")
	}
	if _, err := store.Advance(StageStructure, sampleStructure()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(StageStructure, sampleStructure()); err == nil {
		t.Fatal("structure→structure 未被拒绝")
	}
	if _, err := store.Advance(StageBrief, sampleBrief()); err == nil {
		t.Fatal("structure→brief 回退未被拒绝")
	}
}

func TestShortStoryTamperedArtifactFailsRecovery(t *testing.T) {
	runRoot := t.TempDir()
	manifest := shortManifest(t, runRoot)
	store, _, err := Create(runRoot, manifest, sampleBrief())
	if err != nil {
		t.Fatal(err)
	}
	briefArtifact := filepath.Join(store.Dir(), "artifacts", "brief", "brief.1.json")
	if err := os.WriteFile(briefArtifact, []byte(`{"idea":"被篡改的创意"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "恢复校验失败") {
		t.Fatalf("篡改工件未被恢复校验拦截: %v", err)
	}
}

func TestShortStoryRunStateSeparateFromStage(t *testing.T) {
	runRoot := t.TempDir()
	manifest := shortManifest(t, runRoot)
	store, _, err := Create(runRoot, manifest, sampleBrief())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRunState(RunWaitingAuthor, "等待作者确认结构"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRunState(RunFailed, "provider 限流"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.RunState != RunFailed || state.Stage != StageBrief {
		t.Fatalf("运行状态污染了正文阶段: run=%s stage=%s", state.RunState, state.Stage)
	}
	if len(state.RunEvents) != 2 || state.RunEvents[1].Reason != "provider 限流" {
		t.Fatalf("运行事件记录不完整: %+v", state.RunEvents)
	}
	if _, err := store.SetRunState(RunState("paused"), ""); err == nil {
		t.Fatal("未知运行状态未被拒绝")
	}
}

func TestShortStoryCreateGuardsAndCrashCompletion(t *testing.T) {
	runRoot := t.TempDir()
	manifest := shortManifest(t, runRoot)

	// 长篇 manifest 拒绝以短篇启动。
	longRoot := t.TempDir()
	longManifest, err := project.Create(longRoot, project.CreateOptions{Title: "长篇", PipelineKind: project.PipelineLong})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(longRoot, longManifest, sampleBrief()); err == nil || !strings.Contains(err.Error(), "拒绝以短篇启动") {
		t.Fatalf("long manifest 未被拒绝: %v", err)
	}

	store, _, err := Create(runRoot, manifest, sampleBrief())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(runRoot, manifest, sampleBrief()); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("重复初始化未被拒绝: %v", err)
	}

	// 模拟崩溃：工件已落盘、状态未推进。先以异字节重试 → 冲突；
	// 再以同字节重试 → 幂等收尾。
	if _, err := store.Advance(StageStructure, sampleStructure()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(StageScenes, sampleScenes()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.Dir(), "artifacts", "draft"), 0o755); err != nil {
		t.Fatal(err)
	}
	draft1 := filepath.Join(store.Dir(), "artifacts", "draft", "draft.1.json")
	if err := os.WriteFile(draft1, mustJSON(t, SceneDraft{SceneIndex: 1, Body: "崩溃前的草稿"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(StageDraft, SceneDraft{SceneIndex: 1, Body: "不同的草稿"}); err == nil || !strings.Contains(err.Error(), "内容不同") {
		t.Fatalf("异字节重试未被拒绝: %v", err)
	}
	state, err := store.Advance(StageDraft, SceneDraft{SceneIndex: 1, Body: "崩溃前的草稿"})
	if err != nil {
		t.Fatalf("同字节重试应幂等收尾: %v", err)
	}
	if state.Stage != StageDraft || len(state.History) != 4 {
		t.Fatalf("崩溃收尾后状态不一致: stage=%s history=%d", state.Stage, len(state.History))
	}
}

func TestShortStoryPipelineIsolation(t *testing.T) {
	runRoot := t.TempDir()
	manifest := shortManifest(t, runRoot)
	store, _, err := Create(runRoot, manifest, sampleBrief())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(StageStructure, sampleStructure()); err != nil {
		t.Fatal(err)
	}

	// 短篇工程里不存在长篇引擎目录：长篇入口（output/novel）无状态可加载。
	if _, err := os.Stat(filepath.Join(runRoot, "output", "novel")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("短篇工程不应有长篇引擎目录: %v", err)
	}
	// 项目发现把该作品分类为 short，EngineDir 指向 short/ 而非 output/novel。
	found, ok, err := project.DiscoverRun(runRoot)
	if err != nil || !ok {
		t.Fatalf("发现失败: ok=%v err=%v", ok, err)
	}
	if found.Kind() != project.PipelineShort || found.EngineDir != store.Dir() {
		t.Fatalf("short 作品映射错误: kind=%s engine=%s", found.Kind(), found.EngineDir)
	}

	// 反向：旧长篇工程没有短篇状态。
	longRoot := t.TempDir()
	longMeta := filepath.Join(longRoot, "output", "novel", "meta")
	if err := os.MkdirAll(longMeta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(longMeta, "progress.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(longRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧长篇工程的短篇恢复入口应返回 NotExist: %v", err)
	}
}
