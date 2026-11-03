package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/store"
)

// physicalBaselineFixture 落一份「已验收基线 + 隔离工作区 manifest」。
// baseCanonChapter 必须 >=1：FinalizeProjectAllAcceptedCharacterBaseline 拒绝
// BaseCanonChapter<=0，绑定 nextChapter=1 的 baseline 文件在本合同下无法存在
// （章 1 的合法形态就是文件不存在，回退由无文件路径覆盖）。
func physicalBaselineFixture(t *testing.T, dir, generationID string, baseCanonChapter int, stateRoot string, physical *domain.WorldPhysicalStateV2) {
	t.Helper()
	baseline := store.ProjectAllAcceptedCharacterBaseline{
		Version:             store.ProjectAllAcceptedCharacterBaselineVersion,
		TargetGenerationID:  generationID,
		BaseCanonChapter:    baseCanonChapter,
		BaseStateRoot:       stateRoot,
		SourceGenerationID:  generationID,
		SourceBundleDigest:  domain.ComputeArcArtifactSHA256([]byte("bundle")),
		SourceOutcomeDigest: domain.ComputeArcArtifactSHA256([]byte("outcome")),
		PhysicalState:       physical,
	}
	finalized, err := store.FinalizeProjectAllAcceptedCharacterBaseline(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta", "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(finalized)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "runtime", "accepted_character_baseline.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"version":                           "project-all-workspace.v3",
		"generation_id":                     generationID,
		"base_chapter":                      baseCanonChapter,
		"workspace":                         dir,
		"isolated_writes":                   true,
		"accepted_character_baseline_digest": finalized.Digest,
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "project_all_workspace_manifest.json"), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func foundationCharacters() []domain.Character {
	return []domain.Character{{
		Name: "甲", Role: "值守员", Tier: "core",
		InitialState: &domain.CharacterInitialState{
			Location: "值班室", CurrentGoal: "保留证据", Pressure: "时间有限",
			ResourceBalances: []domain.InitialCharacterResourceV2{{
				ResourceID:    "res_1111111111111111",
				Name:          "既有账本",
				PerceivedName: "账本", PerceivedLabel: "账本",
				Access:        "exclusive",
				Perception:    domain.ResourcePerceptionV2{Kind: "unknown"},
				ReadableFacts: []domain.ResourceReadableFactV2{{ID: "f1", Text: "既有记录原文"}},
			}},
		},
	}}
}

func registeredBaselineState(t *testing.T, characters []domain.Character) domain.WorldPhysicalStateV2 {
	t.Helper()
	registry := &domain.CharacterAgentRegistry{Version: domain.CharacterAgentRegistryVersion}
	for _, character := range characters {
		updated, _, err := registry.UpsertCharacter(character.Name, character.Aliases, character.Tier, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		registry = &updated
	}
	state, err := domain.BuildWorldPhysicalStateFromInitialV2(characters, *registry)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// projectedContext 构造带合法 context_digest 的投影规划上下文（与
// DeriveProjectedPlanningContextV2 的身份字段同构，无 CumulativeState）。
func projectedContext(t *testing.T, generationID string, nextChapter int, stateRoot string) domain.ProjectedPlanningContextV2 {
	t.Helper()
	context := domain.ProjectedPlanningContextV2{
		Version:        domain.ProjectedPlanningContextV2Version,
		GenerationID:   generationID,
		NextChapter:    nextChapter,
		ThroughChapter: nextChapter - 1,
		StateRoot:      stateRoot,
	}
	digest, err := domain.ComputeProjectedPlanningContextV2Digest(context)
	if err != nil {
		t.Fatal(err)
	}
	context.ContextDigest = digest
	return context
}

// P5-02 长篇窗口回归：存在 projected planning context（rebase/outline-all
// 之后的必然形态）且投影与已验收基线都不携带物理负载时，章 1 的预物理态
// 必须回退到 authoring foundation（characters.json 的 initial_state）构建，
// 而不是把空当作权威——否则整弧预演看到零角色零资源，永远 ready 不了。
// 章 1 无已验收基线文件是唯一合法形态（BaseCanonChapter=0 无法通过
// Finalize/Load 合同）。
func TestLoadCharacterPhysicalPreStateFallsBackToFoundationOnEmptyBaseline(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Characters.Save(foundationCharacters()); err != nil {
		t.Fatal(err)
	}
	generationID := "pg2_physical-fixture"
	stateRoot := domain.ComputeArcArtifactSHA256([]byte("state-root"))

	context := projectedContext(t, generationID, 1, stateRoot)

	state, err := loadCharacterPhysicalPreState(st, 1, context)
	if err != nil {
		t.Fatalf("空投影且无基线应回退 foundation 构建: %v", err)
	}
	if domain.IsEmptyWorldPhysicalStateV2(state) {
		t.Fatalf("回退后物理态仍为空: %+v", state)
	}
	if len(state.Actors) != 1 || state.Actors[0].Character != "甲" || state.Actors[0].Location != "值班室" {
		t.Fatalf("角色未从 foundation 构建: %+v", state.Actors)
	}
	if len(state.Resources) != 1 || state.Resources[0].ResourceID != "res_1111111111111111" || len(state.Resources[0].ReadableFacts) != 1 {
		t.Fatalf("资源未从 foundation 构建: %+v", state.Resources)
	}
}

// 非空基线仍然是权威：预物理态原样返回，不回退、不重排，也不被盘面上
// 并存的 authoring foundation 覆盖（foundation 上是甲，基线上是乙）。
func TestLoadCharacterPhysicalPreStateKeepsNonEmptyBaseline(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Characters.Save(foundationCharacters()); err != nil {
		t.Fatal(err)
	}
	generationID := "pg2_physical-fixture-filled"
	stateRoot := domain.ComputeArcArtifactSHA256([]byte("state-root"))
	baselineState := registeredBaselineState(t, []domain.Character{{
		Name: "乙", Role: "前任值守员", Tier: "core",
		InitialState: &domain.CharacterInitialState{
			Location: "档案室",
			ResourceBalances: []domain.InitialCharacterResourceV2{{
				ResourceID:    "res_2222222222222222",
				Name:          "档案室钥匙",
				PerceivedName: "钥匙", PerceivedLabel: "钥匙",
				Access:        "exclusive",
				Perception:    domain.ResourcePerceptionV2{Kind: "unknown"},
				ReadableFacts: []domain.ResourceReadableFactV2{{ID: "g1", Text: "档案室交接记录"}},
			}},
		},
	}})
	physicalBaselineFixture(t, dir, generationID, 1, stateRoot, &baselineState)

	context := projectedContext(t, generationID, 2, stateRoot)

	state, err := loadCharacterPhysicalPreState(st, 2, context)
	if err != nil {
		t.Fatalf("非空基线应原样生效: %v", err)
	}
	if len(state.Actors) != 1 || state.Actors[0].Character != "乙" || state.Actors[0].Location != "档案室" {
		t.Fatalf("预物理态未取自已验收基线: %+v", state.Actors)
	}
	if len(state.Resources) != 1 || state.Resources[0].ResourceID != "res_2222222222222222" {
		t.Fatalf("基线资源未保留: %+v", state.Resources)
	}
}
