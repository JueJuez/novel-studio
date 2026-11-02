package main

// 短篇生成驱动（P5-02 使能项）：把 internal/shortstory 既有 runner
// （组装→RunGeneration→Parse→ProposeGenerated→作者确认）接到产品命令面。
// 本文件零新业务逻辑：阶段语义、校验与落盘合同都在 internal/shortstory，
// CLI 只负责装配模型运行时、按状态机顺序调用并打印结果。模型调用走
// P1-04 的 LoadModelRuntime（现有 provider/配置体系，无第二套客户端）。
// 生成产物一律登记为 authoring 候选，不自动成为正式故事——确认走
// short-story confirm（作者确认点可由执行会话代为确认，须在报告中标注）。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/assets"
	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
	"github.com/chenhongyang/novel-studio/internal/shortstory"
	"github.com/voocel/agentcore"
)

// shortStoryContext 装配短篇驱动的全部实例；结构/场景按状态机阶段从
// 已确认正文读取（未确认即显式报错，不静默跳过阶段顺序）。
type shortStoryContext struct {
	runRoot   string
	manifest  *project.Manifest
	store     *shortstory.Store
	auth      *authoring.Store
	prompts   assets.Prompts
	stageNow  shortstory.Stage
	brief     *shortstory.Brief
	structure *shortstory.Structure
	scenes    []shortstory.Scene
}

func openShortStoryContext(runRoot string) (*shortStoryContext, error) {
	manifest, err := project.Load(runRoot)
	if err != nil {
		return nil, fmt.Errorf("加载作品 manifest 失败: %w", err)
	}
	if manifest.PipelineKind != project.PipelineShort {
		return nil, fmt.Errorf("作品 %s 不是短篇管线（kind=%s）", runRoot, manifest.PipelineKind)
	}
	store, err := shortstory.Open(runRoot)
	if err != nil {
		return nil, err
	}
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	brief, err := store.LoadBrief()
	if err != nil {
		return nil, err
	}
	sc := &shortStoryContext{
		runRoot:  runRoot,
		manifest: manifest,
		store:    store,
		auth:     authoring.Open(runRoot),
		prompts:  assets.Load("").Prompts,
		stageNow: state.Stage,
		brief:    brief,
	}
	if sc.stageNow != shortstory.StageBrief {
		structure, err := loadConfirmedStructure(sc.auth)
		if err != nil {
			return nil, err
		}
		sc.structure = structure
	}
	if sc.stageNow != shortstory.StageBrief && sc.stageNow != shortstory.StageStructure {
		scenes, err := loadConfirmedScenes(sc.auth)
		if err != nil {
			return nil, err
		}
		sc.scenes = scenes
	}
	return sc, nil
}

// loadConfirmedStructure 读取 structure 目标的当前确认版本。
func loadConfirmedStructure(auth *authoring.Store) (*shortstory.Structure, error) {
	raw, err := shortstory.TargetContent(auth, shortstory.TargetStructure)
	if err != nil {
		return nil, fmt.Errorf("结构尚未经作者确认（先 generate --stage structure 并 confirm）: %w", err)
	}
	var structure shortstory.Structure
	if err := json.Unmarshal([]byte(raw), &structure); err != nil {
		return nil, fmt.Errorf("已确认结构解析失败: %w", err)
	}
	return &structure, nil
}

// loadConfirmedScenes 读取 scenes 目标并按 index 升序。
func loadConfirmedScenes(auth *authoring.Store) ([]shortstory.Scene, error) {
	raw, err := shortstory.TargetContent(auth, shortstory.TargetScenes)
	if err != nil {
		return nil, fmt.Errorf("场景计划尚未经作者确认（先 generate --stage scenes 并 confirm）: %w", err)
	}
	var scenes []shortstory.Scene
	if err := json.Unmarshal([]byte(raw), &scenes); err != nil {
		return nil, fmt.Errorf("已确认场景计划解析失败: %w", err)
	}
	for i := 1; i < len(scenes); i++ {
		for j := i; j > 0 && scenes[j].Index < scenes[j-1].Index; j-- {
			scenes[j], scenes[j-1] = scenes[j-1], scenes[j]
		}
	}
	return scenes, nil
}

// printStageResult 打印每次模型调用的路由与用量/成本（P5-02 成本证据
// 的原始来源；Cost 的 unknown 语义由 models 层保证，不显示为零）。
func printStageResult(stage string, result *shortstory.StageResult) {
	fmt.Fprintf(os.Stdout, "[%s] provider=%s model=%s\n", stage, result.Provider, result.Model)
	fmt.Fprintf(os.Stdout, "[%s] usage=%+v cost=%+v\n", stage, result.Usage, result.Cost)
}

func runShortStoryGenerate(argv []string) int {
	fs := flag.NewFlagSet("short-story generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	stage := fs.String("stage", "", "structure|scenes|draft|facts|rewrite|review")
	instruction := fs.String("instruction", "", "rewrite 阶段的修改指令（必填）")
	scene := fs.Int("scene", 0, "draft 阶段场景号；0=依次生成全部未确认场景")
	budgetRange := fs.String("budget-range", "[5000, 30000]", "scenes 阶段全篇字数预算区间")
	configPath := fs.String("config", "", "模型配置路径（默认全局+项目级配置解析）")
	timeout := fs.Int("timeout-seconds", 900, "单次模型调用超时秒数")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" || strings.TrimSpace(*stage) == "" {
		fmt.Fprintln(os.Stderr, "short-story generate: --dir 与 --stage 必填")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story generate: %v\n", err)
		return 1
	}
	sc, err := openShortStoryContext(runRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story generate: %v\n", err)
		return 1
	}
	runtime, err := shortstory.LoadModelRuntime(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story generate: %v\n", err)
		return 1
	}
	model := runtime.ForRole("writer")
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	switch *stage {
	case "structure":
		err = generateStructure(ctx, sc, model)
	case "scenes":
		err = generateScenes(ctx, sc, model, *budgetRange)
	case "draft":
		err = generateDrafts(ctx, sc, model, *scene)
	case "facts":
		err = generateFacts(ctx, sc, model, *scene)
	case "rewrite":
		err = generateRewrite(ctx, sc, model, *scene, *instruction)
	case "review":
		err = generateReview(ctx, sc, model)
	default:
		err = fmt.Errorf("未知阶段 %q（structure|scenes|draft|facts|rewrite|review）", *stage)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story generate: %v\n", err)
		return 1
	}
	return 0
}

// generateStructure：brief → 结构候选。
func generateStructure(ctx context.Context, sc *shortStoryContext, model agentcore.ChatModel) error {
	req, err := shortstory.AssembleStructureRequest(sc.prompts, *sc.brief, nil)
	if err != nil {
		return err
	}
	result, err := shortstory.RunGeneration(ctx, model, req)
	if err != nil {
		return err
	}
	printStageResult("structure", result)
	structure, err := shortstory.ParseStructureResponse(result.Text)
	if err != nil {
		return err
	}
	if err := shortstory.EnsureTarget(sc.auth, shortstory.TargetStructure); err != nil {
		return err
	}
	content, err := shortstory.MarshalCanonical(structure)
	if err != nil {
		return err
	}
	candidate, err := shortstory.ProposeGenerated(sc.auth, shortstory.TargetStructure, "structure", content)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "结构候选已登记：%s（待作者确认后 short-story confirm --target structure）\n", candidate.CandidateID)
	return nil
}

// generateScenes：已确认结构 → 场景计划候选（预算/DAG/承诺校验在 Parse 内）。
func generateScenes(ctx context.Context, sc *shortStoryContext, model agentcore.ChatModel, budgetRange string) error {
	req, err := shortstory.AssembleScenesRequest(sc.prompts, sc.structure, budgetRange)
	if err != nil {
		return err
	}
	result, err := shortstory.RunGeneration(ctx, model, req)
	if err != nil {
		return err
	}
	printStageResult("scenes", result)
	scenes, total, err := shortstory.ParseScenesResponse(result.Text, sc.structure)
	if err != nil {
		return err
	}
	if err := shortstory.EnsureTarget(sc.auth, shortstory.TargetScenes); err != nil {
		return err
	}
	content, err := shortstory.MarshalCanonical(scenes)
	if err != nil {
		return err
	}
	candidate, err := shortstory.ProposeGenerated(sc.auth, shortstory.TargetScenes, "scenes", content)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "场景计划候选已登记：%s（%d 个场景，预算合计 %d rune；待作者确认）\n", candidate.CandidateID, len(scenes), total)
	return nil
}

// sceneKnowledge 把场景事实/约束/承诺组装为检索语料（P2-02 口径：
// 语料为场景事实/承诺/约束/知识条目）。
func sceneKnowledge(scene shortstory.Scene, structure *shortstory.Structure) []shortstory.KnowledgeDoc {
	docs := []shortstory.KnowledgeDoc{}
	for i, fact := range scene.Facts {
		docs = append(docs, shortstory.KnowledgeDoc{ID: fmt.Sprintf("fact-%d-%d", scene.Index, i), Kind: "fact", Text: fact})
	}
	for i, item := range structure.AuthorConstraints {
		docs = append(docs, shortstory.KnowledgeDoc{ID: fmt.Sprintf("constraint-%d", i), Kind: "constraint", Text: item})
	}
	for i, item := range structure.Promises {
		docs = append(docs, shortstory.KnowledgeDoc{ID: fmt.Sprintf("promise-%d", i), Kind: "promise", Text: item})
	}
	return docs
}

// generateDrafts：逐场景草稿候选 + 事实/伏笔提取登记。候选必须经作者
// 确认（short-story confirm）才进入正式正文。
func generateDrafts(ctx context.Context, sc *shortStoryContext, model agentcore.ChatModel, only int) error {
	if len(sc.scenes) == 0 {
		return fmt.Errorf("场景计划为空或未确认")
	}
	pending := 0
	for _, scene := range sc.scenes {
		if only != 0 && scene.Index != only {
			continue
		}
		pending++
		prior := map[int]string{}
		for _, done := range sc.scenes {
			if done.Index >= scene.Index {
				continue
			}
			if content, err := shortstory.TargetContent(sc.auth, shortstory.SceneTargetID(done.Index)); err == nil {
				prior[done.Index] = content
			}
		}
		dcc := shortstory.SceneDraftContext{
			Structure: sc.structure, Scenes: sc.scenes, Scene: scene,
			PriorDrafts: prior,
			Facts:       knowledgeTexts(shortstory.RetrieveKnowledge(scene.Goal+scene.Obstacle, sceneKnowledge(scene, sc.structure), 2000)),
		}
		req, err := shortstory.AssembleSceneDraftRequest(sc.prompts, dcc, shortstory.ContextBudgetRunes)
		if err != nil {
			return err
		}
		result, err := shortstory.RunGeneration(ctx, model, req)
		if err != nil {
			return fmt.Errorf("场景 %d 草稿生成失败: %w", scene.Index, err)
		}
		printStageResult(fmt.Sprintf("draft-%d", scene.Index), result)
		if err := shortstory.EnsureTarget(sc.auth, shortstory.SceneTargetID(scene.Index)); err != nil {
			return err
		}
		candidate, err := shortstory.ProposeGenerated(sc.auth, shortstory.SceneTargetID(scene.Index), "draft", result.Text)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "场景 %d 草稿候选已登记：%s（待作者确认；确认后用 --stage facts 提取事实）\n", scene.Index, candidate.CandidateID)
	}
	if pending == 0 {
		return fmt.Errorf("没有匹配的场景（--scene %d）", only)
	}
	return nil
}

// generateReview：组装全文 → 全文审阅报告（矛盾/因果/POV/兑现/文风）。
// 报告落盘绑定被审版本；blocker 由 FinalizeGateCheck 在 finalize 时阻断。
func generateReview(ctx context.Context, sc *shortStoryContext, model agentcore.ChatModel) error {
	if err := shortstory.EnsureTarget(sc.auth, shortstory.TargetFullText); err != nil {
		return err
	}
	fullText, err := shortstory.CommitFullText(sc.auth, sc.scenes)
	if err != nil {
		return err
	}
	req, err := shortstory.AssembleReviewRequest(sc.prompts, shortstory.TargetFullText, fullText.Content, sc.structure, sc.scenes)
	if err != nil {
		return err
	}
	result, err := shortstory.RunGeneration(ctx, model, req)
	if err != nil {
		return err
	}
	printStageResult("review", result)
	report, err := shortstory.ParseReviewResponse(result.Text, shortstory.TargetFullText, fullText, sc.scenes)
	if err != nil {
		return err
	}
	if err := shortstory.RecordReview(sc.auth, report); err != nil {
		return err
	}
	blockers := 0
	for _, issue := range report.Issues {
		if issue.Severity == "blocker" {
			blockers++
		}
	}
	fmt.Fprintf(os.Stdout, "全文终审报告已落盘（fulltext=%s）：issue=%d blocker=%d\n", fullText.VersionID, len(report.Issues), blockers)
	for _, issue := range report.Issues {
		fmt.Fprintf(os.Stdout, "  [%s/%s] %s\n", issue.Severity, issue.Type, issue.Description)
	}
	return nil
}

// generateFacts：对已确认的场景草稿运行事实/伏笔提取，登记为来源
// 候选（作者接受才转正）。提取绑定的是已确认版本——候选未确认时
// 显式报错，不绑定悬空内容。
func generateFacts(ctx context.Context, sc *shortStoryContext, model agentcore.ChatModel, only int) error {
	ran := 0
	for _, scene := range sc.scenes {
		if only != 0 && scene.Index != only {
			continue
		}
		targetID := shortstory.SceneTargetID(scene.Index)
		head, err := sc.auth.Head(targetID)
		if err != nil {
			return fmt.Errorf("场景 %d 尚未经作者确认，无法提取（先 confirm --target %s）: %w", scene.Index, targetID, err)
		}
		content, err := shortstory.TargetContent(sc.auth, targetID)
		if err != nil {
			return err
		}
		if _, _, err := shortstory.RunExtraction(ctx, model, sc.auth, sc.prompts, scene, targetID, head.VersionID, content); err != nil {
			return fmt.Errorf("场景 %d 事实提取失败: %w", scene.Index, err)
		}
		ran++
		fmt.Fprintf(os.Stdout, "场景 %d 事实/伏笔候选已登记（绑定版本 %s，待作者接受）\n", scene.Index, head.VersionID)
	}
	if ran == 0 {
		return fmt.Errorf("没有匹配的已确认场景（--scene %d）", only)
	}
	return nil
}

// generateRewrite：按作者指令对已确认场景做定向局部重写，登记为候选。
// 修稿循环（review→draft）的 AI 侧入口；确认后全文版本随之推进，
// 旧终审/旧确认自动失效（P3-04 合同）。
func generateRewrite(ctx context.Context, sc *shortStoryContext, model agentcore.ChatModel, sceneIndex int, instruction string) error {
	if strings.TrimSpace(instruction) == "" {
		return fmt.Errorf("rewrite 需要 --instruction 修改指令")
	}
	var scene *shortstory.Scene
	for i := range sc.scenes {
		if sc.scenes[i].Index == sceneIndex {
			scene = &sc.scenes[i]
		}
	}
	if scene == nil {
		return fmt.Errorf("场景计划中没有场景 %d", sceneIndex)
	}
	targetID := shortstory.SceneTargetID(sceneIndex)
	current, err := shortstory.TargetContent(sc.auth, targetID)
	if err != nil {
		return fmt.Errorf("场景 %d 尚无已确认草稿可重写: %w", sceneIndex, err)
	}
	req, err := shortstory.AssembleSceneRewriteRequest(sc.prompts, *scene, current, instruction)
	if err != nil {
		return err
	}
	result, err := shortstory.RunGeneration(ctx, model, req)
	if err != nil {
		return err
	}
	printStageResult(fmt.Sprintf("rewrite-%d", sceneIndex), result)
	candidate, err := shortstory.ProposeGenerated(sc.auth, targetID, "rewrite", result.Text)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "场景 %d 重写候选已登记：%s（待作者确认）\n", sceneIndex, candidate.CandidateID)
	return nil
}

// knowledgeTexts 把检索结果铺平为事实清单。
func knowledgeTexts(docs []shortstory.KnowledgeDoc) []string {
	out := make([]string, 0, len(docs))
	for _, doc := range docs {
		out = append(out, doc.Text)
	}
	return out
}

// runShortStoryConfirm：作者确认（Accept+确认绑定），并按目标推进短篇
// 状态机（structure/scenes 确认即推进对应阶段；场景草稿全部确认后推进
// draft 阶段）。
func runShortStoryConfirm(argv []string) int {
	fs := flag.NewFlagSet("short-story confirm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	target := fs.String("target", "", "目标 ID：structure|scenes|scene-<n>|fulltext")
	candidate := fs.String("candidate", "", "候选 ID（short-story candidates 列出）")
	note := fs.String("note", "", "确认备注（谁在确认、为什么）")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" || strings.TrimSpace(*target) == "" {
		fmt.Fprintln(os.Stderr, "short-story confirm: --dir/--target 必填（fulltext 之外还需 --candidate）")
		return 1
	}
	if *target != shortstory.TargetFullText && strings.TrimSpace(*candidate) == "" {
		fmt.Fprintln(os.Stderr, "short-story confirm: --candidate 必填（fulltext 终稿确认绑定当前 head）")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story confirm: %v\n", err)
		return 1
	}
	manifest, err := project.Load(runRoot)
	if err != nil || manifest.PipelineKind != project.PipelineShort {
		fmt.Fprintf(os.Stderr, "short-story confirm: 不是短篇作品（err=%v）\n", err)
		return 1
	}
	store, err := shortstory.Open(runRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story confirm: %v\n", err)
		return 1
	}
	auth := authoring.Open(runRoot)
	// fulltext 没有候选：全文版本由 CommitFullText 直接提交（内容寻址），
	// 终稿确认直接绑定当前 head 版本。
	if *target == shortstory.TargetFullText {
		head, err := auth.Head(shortstory.TargetFullText)
		if err != nil {
			fmt.Fprintf(os.Stderr, "short-story confirm: 全文版本不存在（先 generate --stage review）: %v\n", err)
			return 1
		}
		note := strings.TrimSpace(*note)
		if note == "" {
			note = "作者终稿确认"
		}
		if _, err := auth.RecordAuthorApproval(shortstory.TargetFullText, head.VersionID, "", "作者终稿确认", note); err != nil {
			fmt.Fprintf(os.Stderr, "short-story confirm: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "已确认 %s -> 版本 %s（作者终稿确认）\n", *target, head.VersionID)
		return 0
	}
	version, err := shortstory.ConfirmTarget(auth, *target, *candidate, strings.TrimSpace(*note))
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story confirm: %v\n", err)
		return 1
	}
	artifact := map[string]string{"version_id": version.VersionID, "candidate_id": *candidate}
	// advanceIfStage：仅在状态机停在 from 阶段时推进。重复确认（如重跑
	// 脚本、修稿循环里的再次确认）不构成阶段转移，静默跳过。
	advanceIfStage := func(from shortstory.Stage, to shortstory.Stage) error {
		state, err := store.Load()
		if err != nil {
			return err
		}
		if state.Stage != from {
			return nil
		}
		_, err = store.Advance(to, artifact)
		return err
	}
	switch *target {
	case shortstory.TargetStructure:
		err = advanceIfStage(shortstory.StageBrief, shortstory.StageStructure)
	case shortstory.TargetScenes:
		err = advanceIfStage(shortstory.StageStructure, shortstory.StageScenes)
	case shortstory.TargetFullText:
		// 终稿确认由 finalize 阶段统一推进状态机。
	default:
		if strings.HasPrefix(*target, shortstory.TargetSceneDraftPrefix) {
			if advanceErr := advanceDraftIfComplete(store, auth, *target); advanceErr != nil {
				err = advanceErr
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story confirm: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "已确认 %s -> 版本 %s\n", *target, version.VersionID)
	return 0
}

// advanceDraftIfComplete：场景草稿全部确认后推进 draft 阶段（与 P2 出口
// 演示的推进时点一致）。仅当状态机仍在 scenes 阶段才推进——修稿循环里
// 的重写确认发生在 draft/review 之后，重复推进是非法转移。
func advanceDraftIfComplete(store *shortstory.Store, auth *authoring.Store, confirmedTarget string) error {
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.Stage != shortstory.StageScenes {
		return nil
	}
	scenes, err := loadConfirmedScenes(auth)
	if err != nil {
		return err
	}
	confirmed := strings.TrimPrefix(confirmedTarget, shortstory.TargetSceneDraftPrefix)
	for _, scene := range scenes {
		if content, err := shortstory.TargetContent(auth, shortstory.SceneTargetID(scene.Index)); err != nil || strings.TrimSpace(content) == "" {
			return nil // 还有场景未确认，不推进。
		}
	}
	_, err = store.Advance(shortstory.StageDraft, map[string]string{"scenes": confirmed, "confirmed": confirmedTarget})
	return err
}

// runShortStoryReject：作者拒绝候选（不改正文，决定留痕可审计）。
func runShortStoryReject(argv []string) int {
	fs := flag.NewFlagSet("short-story reject", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	target := fs.String("target", "", "目标 ID")
	candidate := fs.String("candidate", "", "候选 ID")
	reason := fs.String("reason", "", "拒绝理由（必填，留痕）")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" || strings.TrimSpace(*target) == "" || strings.TrimSpace(*candidate) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, "short-story reject: --dir/--target/--candidate/--reason 必填")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story reject: %v\n", err)
		return 1
	}
	auth := authoring.Open(runRoot)
	if err := auth.Reject(*target, *candidate, strings.TrimSpace(*reason)); err != nil {
		fmt.Fprintf(os.Stderr, "short-story reject: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "已拒绝 %s/%s（正文不变）\n", *target, *candidate)
	return 0
}

// runShortStoryWaive：作者对 blocker 的终裁豁免——拒绝该 issue 来源
// （裁定不成立或不构成交付障碍）。决策留痕（来源+理由+时间），版本
// 推进后旧豁免按内容寻址自动失效。
func runShortStoryWaive(argv []string) int {
	fs := flag.NewFlagSet("short-story waive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	target := fs.String("target", string(shortstory.TargetFullText), "审阅目标（默认 fulltext）")
	source := fs.String("source", "", "issue 来源 ID（short-story issues 列出，必填）")
	reason := fs.String("reason", "", "终裁理由（必填，留痕）")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" || strings.TrimSpace(*source) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, "short-story waive: --dir/--source/--reason 必填")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story waive: %v\n", err)
		return 1
	}
	auth := authoring.Open(runRoot)
	if err := auth.RejectSource(*target, *source, strings.TrimSpace(*reason)); err != nil {
		fmt.Fprintf(os.Stderr, "short-story waive: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "已豁免 %s/%s（作者终裁留痕）\n", *target, *source)
	return 0
}

// runShortStoryIssues：列出当前全文版本的 blocker 及其来源 ID（供 waive）。
func runShortStoryIssues(argv []string) int {
	fs := flag.NewFlagSet("short-story issues", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "short-story issues: --dir 必填")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story issues: %v\n", err)
		return 1
	}
	auth := authoring.Open(runRoot)
	head, err := auth.Head(shortstory.TargetFullText)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story issues: 全文版本不存在: %v\n", err)
		return 1
	}
	report, err := shortstory.LoadLatestReview(auth, shortstory.TargetFullText)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story issues: %v\n", err)
		return 1
	}
	if report.ContentVersionID != head.VersionID {
		fmt.Fprintf(os.Stdout, "终审报告绑定旧版本 %s（当前 %s，正文已返工需重审）\n", report.ContentVersionID, head.VersionID)
		return 0
	}
	for _, issue := range report.Issues {
		if issue.Severity != "blocker" {
			continue
		}
		sourceID, err := shortstory.ReviewIssueSourceID(auth, report, issue)
		if err != nil {
			fmt.Fprintf(os.Stderr, "short-story issues: %v\n", err)
			return 1
		}
		decision := auth.SourceDecision(shortstory.TargetFullText, sourceID)
		fmt.Fprintf(os.Stdout, "%s	%s	%s	%s\n", sourceID, decision, issue.Type, issue.Description)
	}
	return 0
}

// runShortStoryCandidates：列出待决候选（供 confirm/reject 取 ID）。
func runShortStoryCandidates(argv []string) int {
	fs := flag.NewFlagSet("short-story candidates", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	target := fs.String("target", "", "只列该目标；缺省列全部")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "short-story candidates: --dir 必填")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story candidates: %v\n", err)
		return 1
	}
	auth := authoring.Open(runRoot)
	targets := []string{}
	if strings.TrimSpace(*target) != "" {
		targets = append(targets, *target)
	} else {
		for _, name := range []string{shortstory.TargetStructure, shortstory.TargetScenes, shortstory.TargetFullText} {
			targets = append(targets, name)
		}
		for i := 1; i <= 64; i++ {
			id := shortstory.SceneTargetID(i)
			if dirExists(filepath.Join(auth.Dir(), "targets", id)) {
				targets = append(targets, id)
			}
		}
	}
	found := 0
	for _, id := range targets {
		pending, err := listPending(auth, id)
		if err != nil {
			continue
		}
		for _, candidate := range pending {
			found++
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\n", id, candidate.CandidateID, candidate.Kind, candidate.Task, candidate.CreatedAt)
		}
	}
	if found == 0 {
		fmt.Fprintln(os.Stdout, "（无待决候选）")
	}
	return 0
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// listPending 列出目标上未决候选（Decision 为空即未决）。
func listPending(auth *authoring.Store, targetID string) ([]*authoring.Candidate, error) {
	entries, err := os.ReadDir(filepath.Join(auth.Dir(), "targets", targetID, "candidates"))
	if err != nil {
		return nil, err
	}
	var pending []*authoring.Candidate
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		candidateID := strings.TrimSuffix(entry.Name(), ".json")
		if auth.Decision(targetID, candidateID) != "" {
			continue
		}
		candidate, err := auth.LoadCandidate(targetID, candidateID)
		if err != nil {
			continue
		}
		pending = append(pending, candidate)
	}
	return pending, nil
}

// runShortStoryFinalize：终审门禁 → 推进 review/author_approval/finalize/
// delivered → 导出 Markdown/TXT+清单+备份。作者终稿确认由
// confirm --target fulltext 完成（或由执行会话代为确认并在报告中标注）。
func runShortStoryFinalize(argv []string) int {
	fs := flag.NewFlagSet("short-story finalize", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "作品根目录（必填）")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(os.Stderr, "short-story finalize: --dir 必填")
		return 1
	}
	runRoot, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: %v\n", err)
		return 1
	}
	manifest, err := project.Load(runRoot)
	if err != nil || manifest.PipelineKind != project.PipelineShort {
		fmt.Fprintf(os.Stderr, "short-story finalize: 不是短篇作品（err=%v）\n", err)
		return 1
	}
	store, err := shortstory.Open(runRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: %v\n", err)
		return 1
	}
	auth := authoring.Open(runRoot)
	if _, err := loadConfirmedStructure(auth); err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: %v\n", err)
		return 1
	}
	scenes, err := loadConfirmedScenes(auth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: %v\n", err)
		return 1
	}
	fullText, err := shortstory.CommitFullText(auth, scenes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: %v\n", err)
		return 1
	}
	if err := shortstory.FinalizeGateCheck(auth, scenes); err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: 终审门禁未过: %v\n", err)
		return 1
	}
	artifact := map[string]string{"fulltext_version": fullText.VersionID}
	for _, step := range []shortstory.Stage{shortstory.StageReview, shortstory.StageAuthorApproval, shortstory.StageFinalize, shortstory.StageDelivered} {
		if _, err := store.Advance(step, artifact); err != nil {
			fmt.Fprintf(os.Stderr, "short-story finalize: %v\n", err)
			return 1
		}
	}
	result, delivery, err := shortstory.Export(runRoot, manifest.Title, scenes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story finalize: 导出失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "已交付：%s\n", manifest.Title)
	fmt.Fprintf(os.Stdout, "  markdown=%s\n  txt=%s\n  backup=%s\n", result.MarkdownPath, result.TextPath, result.BackupDir)
	fmt.Fprintf(os.Stdout, "  fulltext=%s digest=%s approval=%s\n", fullText.VersionID, delivery.FullTextDigest, delivery.AuthorApprovalID)
	return 0
}
