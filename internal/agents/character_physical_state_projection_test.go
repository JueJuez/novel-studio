package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/store"
)

func physicalBaselineFixture(t *testing.T, dir, generationID, stateRoot string, physical *domain.WorldPhysicalStateV2) {
	t.Helper()
	baseline := store.ProjectAllAcceptedCharacterBaseline{
		Version:             store.ProjectAllAcceptedCharacterBaselineVersion,
		TargetGenerationID:  generationID,
		BaseCanonChapter:    0,
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
		"Version":                           "project-all-workspace.v3",
		"GenerationID":                      generationID,
		"BaseChapter":                       0,
		"Workspace":                         dir,
		"IsolatedWrites":                    true,
		"AcceptedCharacterBaselineDigest":   finalized.Digest,
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

// P5-02 长篇窗口回归：存在 projected planning context（rebase/outline-all
// 之后的必然形态）且其绑定的 accepted baseline 物理负载为空时，章 1 的
// 预物理态必须回退到 authoring foundation（characters.json 的
// initial_state）构建，而不是把空基线当作权威——否则整弧预演看到零角色
// 零资源，永远 ready 不了。
func TestLoadCharacterPhysicalPreStateFallsBackToFoundationOnEmptyBaseline(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Characters.Save(foundationCharacters()); err != nil {
		t.Fatal(err)
	}
	generationID := "pg2_physical-fixture"
	stateRoot := domain.ComputeArcArtifactSHA256([]byte("state-root"))
	physicalBaselineFixture(t, dir, generationID, stateRoot,
		&domain.WorldPhysicalStateV2{Version: domain.WorldPhysicalStateV2Version})

	context := domain.ProjectedPlanningContextV2{
		Version:        domain.ProjectedPlanningContextV2Version,
		GenerationID:   generationID,
		NextChapter:    1,
		ThroughChapter: 0,
		StateRoot:      stateRoot,
	}

	state, err := loadCharacterPhysicalPreState(st, 1, context)
	if err != nil {
		t.Fatalf("空基线应回退 foundation 构建: %v", err)
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

// 非空基线仍然是权威：预物理态原样返回，不回退、不重排。
func TestLoadCharacterPhysicalPreStateKeepsNonEmptyBaseline(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Characters.Save(foundationCharacters()); err != nil {
		t.Fatal(err)
	}
	generationID := "pg2_physical-fixture-filled"
	stateRoot := domain.ComputeArcArtifactSHA256([]byte("state-root-filled"))
	filled := domain.WorldPhysicalStateV2{
		Version:   domain.WorldPhysicalStateV2Version,
		Resources: []domain.WorldResourceBalanceV2{{ResourceID: "res_2222222222222222", Name: "上章产出", Unit: "份"}},
		Actors:    []domain.CharacterPhysicalStateV2{{AgentID: "agent-x", Character: "甲", Location: "档案舱"}},
	}
	physicalBaselineFixture(t, dir, generationID, stateRoot, &filled)

	context := domain.ProjectedPlanningContextV2{
		Version:        domain.ProjectedPlanningContextV2Version,
		GenerationID:   generationID,
		NextChapter:    1,
		ThroughChapter: 0,
		StateRoot:      stateRoot,
	}
	state, err := loadCharacterPhysicalPreState(st, 1, context)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Resources) != 1 || state.Resources[0].ResourceID != "res_2222222222222222" || len(state.Actors) != 1 || state.Actors[0].AgentID != "agent-x" {
		t.Fatalf("非空基线应原样生效: %+v", state)
	}
}
