package shortstory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chenhongyang/novel-studio/assets"
	"github.com/voocel/agentcore"
)

// 本文件实现 P2-01/P2-02 的生成阶段 runner：组装提示词（assets/prompts/
// shortstory-*.md）、调用 P1-04 的模型适配器、把响应解析为领域工件。
// 生成结果一律以候选形式返回，由调用方走确认流；本文件不发起真实调用
// （测试用 mock model）。

// AssembleStructureRequest 组装结构生成调用：作者约束、创意、检索到的
// 人物/设定知识全部进入提示词（计划 P2-01：作者规则与人物知识进入结构）。
func AssembleStructureRequest(prompts assets.Prompts, brief Brief, knowledge []string) (StageRequest, error) {
	briefJSON, err := MarshalCanonical(map[string]any{
		"idea":               brief.Idea,
		"author_constraints": brief.AuthorConstraints,
		"knowledge":          knowledge,
	})
	if err != nil {
		return StageRequest{}, err
	}
	return StageRequest{
		Purpose:      "shortstory-structure",
		SystemPrompt: prompts.ShortStoryStructure,
		UserPrompt:   "## 创意与约束\n\n" + briefJSON,
		// P5-02 实测：结构 JSON（含长承诺）在 4K 输出被截断（样稿 B）。
		MaxTokens: 8192,
	}, nil
}

// ParseStructureResponse 从模型响应中提取并校验结构 JSON。
func ParseStructureResponse(text string) (*Structure, error) {
	decoded, err := decodeJSONLoose[Structure](text)
	if err != nil {
		return nil, fmt.Errorf("shortstory: 结构响应解析失败: %w", err)
	}
	structure := &decoded
	if strings.TrimSpace(structure.Conflict) == "" || strings.TrimSpace(structure.EndingDirection) == "" {
		return nil, fmt.Errorf("shortstory: 结构缺少 conflict/ending_direction")
	}
	if len(structure.Characters) == 0 {
		return nil, fmt.Errorf("shortstory: 结构缺少人物")
	}
	return structure, nil
}

// AssembleScenesRequest 组装场景计划生成调用。
func AssembleScenesRequest(prompts assets.Prompts, structure *Structure, sceneRange string) (StageRequest, error) {
	structureJSON, err := MarshalCanonical(structure)
	if err != nil {
		return StageRequest{}, err
	}
	return StageRequest{
		Purpose:      "shortstory-scenes",
		SystemPrompt: prompts.ShortStoryScenes,
		UserPrompt: fmt.Sprintf("## 已确认结构\n\n%s\n\n## 字数要求\n\n所有场景 word_budget 之和必须落在 %s（rune 口径）。\n",
			structureJSON, sceneRange),
		// 真实多场景计划的完整 JSON（含每景 facts/continuity，且承诺
		// 原句需逐字复制）实测超过 4K 与 8K 输出上限（P5-02 样稿 A 两度
		// 截断致解析失败）；scenes 是全管线最长的 JSON 产物，放开到 16384。
		MaxTokens: 16384,
	}, nil
}

// ParseScenesResponse 解析并校验场景计划：DAG、预算、承诺覆盖。
func ParseScenesResponse(text string, structure *Structure) ([]Scene, int, error) {
	scenes, err := decodeJSONLoose[[]Scene](text)
	if err != nil {
		return nil, 0, fmt.Errorf("shortstory: 场景计划响应解析失败: %w", err)
	}
	if err := checkSceneDAG(scenes); err != nil {
		return nil, 0, err
	}
	total, err := ValidateSceneBudgets(scenes)
	if err != nil {
		return nil, total, err
	}
	if missing := PromisesCoverage(*structure, scenes); len(missing) > 0 {
		return nil, total, fmt.Errorf("shortstory: 以下承诺在场景计划中没有兑现安排：%s", strings.Join(missing, "；"))
	}
	return scenes, total, nil
}

// SceneDraftContext 是一个场景草稿的全部上下文。
type SceneDraftContext struct {
	Structure   *Structure
	Scenes      []Scene
	Scene       Scene
	PriorDrafts map[int]string // 场景 index → 已完成草稿
	Facts       []string       // 检索到的相关设定/事实
}

// ErrContextOverflow 表示草稿上下文超出单次调用预算：必须显式走分层
// 审核（计划 P2-02），本函数族绝不静默截断结局义务与前文。
var ErrContextOverflow = fmt.Errorf("shortstory: 草稿上下文超出预算，需要分层审核")

// AssembleSceneDraftRequest 组装逐场景草稿调用。分层裁剪策略（计划 P2-02：
// 超上下文显式提示，不静默丢掉结局/前文）：
//  1. 全量：硬保证项（结构承诺、结局方向、场景计划）+ 检索知识 + 前文摘录；
//  2. 先丢弃检索知识；
//  3. 再把前文摘录截短到每场景 300 rune；
//  4. 只剩硬保证项；
//  5. 仍超限 → 显式返回 ErrContextOverflow（走分层审核），硬保证项永不丢弃。
func AssembleSceneDraftRequest(prompts assets.Prompts, dcc SceneDraftContext, budgetRunes int) (StageRequest, error) {
	if dcc.Scene.WordBudget <= 0 {
		return StageRequest{}, fmt.Errorf("shortstory: 场景 %d 缺少字数预算", dcc.Scene.Index)
	}
	hard := []string{
		"## 结构承诺（必须尊重，不得提前揭开）\n" + joinOrNone(dcc.Structure.Promises),
		"## 结局方向\n" + dcc.Structure.EndingDirection,
		"## 当前场景计划\n" + mustSceneJSON(dcc.Scene),
	}
	knowledgeSection := ""
	if len(dcc.Facts) > 0 {
		var builder strings.Builder
		for _, fact := range dcc.Facts {
			builder.WriteString("- " + fact + "\n")
		}
		knowledgeSection = "## 相关设定与事实\n" + builder.String()
	}
	tailSection := ""
	if tail := priorDraftTail(dcc.Scenes, dcc.PriorDrafts, dcc.Scene.Index); tail != "" {
		tailSection = "## 前文摘录\n" + tail
	}
	fits := func(s string) bool {
		return budgetRunes <= 0 || len([]rune(s)) <= budgetRunes
	}
	join := func(sections []string) string {
		nonEmpty := make([]string, 0, len(sections))
		for _, section := range sections {
			if section != "" {
				nonEmpty = append(nonEmpty, section)
			}
		}
		return strings.Join(nonEmpty, "\n\n")
	}
	prompt := join(append(append([]string{}, hard...), knowledgeSection, tailSection))
	if !fits(prompt) && knowledgeSection != "" {
		prompt = join(append(append([]string{}, hard...), tailSection))
	}
	if !fits(prompt) && tailSection != "" {
		if shortened := shortenTail(tailSection, 300); shortened != tailSection {
			prompt = join(append(append([]string{}, hard...), shortened))
		}
	}
	if !fits(prompt) {
		prompt = join(hard)
	}
	if !fits(prompt) {
		return StageRequest{}, fmt.Errorf("%w（场景 %d）", ErrContextOverflow, dcc.Scene.Index)
	}
	return StageRequest{
		Purpose:      fmt.Sprintf("shortstory-draft-scene-%d", dcc.Scene.Index),
		SystemPrompt: prompts.ShortStoryDraft,
		UserPrompt:   prompt,
		MaxTokens:    sceneMaxTokens(dcc.Scene.WordBudget),
	}, nil
}

// shortenTail 把前文摘录段截短到保留结尾 limit rune（含段标题）。
func shortenTail(section string, limit int) string {
	runes := []rune(section)
	if len(runes) <= limit {
		return section
	}
	header := "## 前文摘录（截短）\n"
	return header + string(runes[len(runes)-(limit-len([]rune(header))):])
}

// priorDraftTail 返回按因果依赖组织的既有草稿摘录：每个依赖场景保留
// 结尾片段（前文上下文），直接前置场景保留更长结尾。
func priorDraftTail(scenes []Scene, drafts map[int]string, currentIndex int) string {
	var scene *Scene
	for i := range scenes {
		if scenes[i].Index == currentIndex {
			scene = &scenes[i]
			break
		}
	}
	if scene == nil {
		return ""
	}
	var builder strings.Builder
	deps := scene.CausalDeps
	if len(deps) == 0 && currentIndex > 1 {
		deps = []int{currentIndex - 1}
	}
	for _, dep := range deps {
		draft, ok := drafts[dep]
		if !ok {
			continue
		}
		runes := []rune(strings.TrimSpace(draft))
		take := 600
		if len(deps) == 1 {
			take = 1200
		}
		if len(runes) > take {
			runes = runes[len(runes)-take:]
		}
		builder.WriteString(fmt.Sprintf("—— 场景 %d 结尾 ——\n%s\n", dep, string(runes)))
	}
	return strings.TrimSpace(builder.String())
}

func mustSceneJSON(scene Scene) string {
	raw, err := MarshalCanonical(scene)
	if err != nil {
		return fmt.Sprintf(`{"index":%d}`, scene.Index)
	}
	return raw
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "（无）"
	}
	return "- " + strings.Join(values, "\n- ")
}

func sceneMaxTokens(wordBudget int) int {
	// P5-02 实测（deepseek-v4-flash，样稿 A scene-2：677 rune 用满 1024
	// token 被截断）：该模型中文 ≈1.5 token/rune，旧系数 2/3 低估约 2.2 倍。
	// 取 3/2 换算 + 收尾余量，上不封顶到 32768。
	tokens := wordBudget * 3 / 2
	tokens += 512
	if tokens < 1024 {
		tokens = 1024
	}
	if tokens > 32768 {
		tokens = 32768
	}
	return tokens
}

// AssembleSceneRewriteRequest 组装局部重写调用：原稿 + 指令 + 必须保留
// 的事实（场景 facts）。
func AssembleSceneRewriteRequest(prompts assets.Prompts, scene Scene, currentDraft, instruction string) (StageRequest, error) {
	var keep strings.Builder
	for _, fact := range scene.Facts {
		keep.WriteString("- " + fact + "\n")
	}
	userPrompt := fmt.Sprintf("## 当前正文\n\n%s\n\n## 修改指令\n\n%s\n\n## 必须保留\n\n%s",
		strings.TrimSpace(currentDraft), strings.TrimSpace(instruction), joinOrNone(scene.Facts))
	if keep.Len() > 0 {
		userPrompt = fmt.Sprintf("## 当前正文\n\n%s\n\n## 修改指令\n\n%s\n\n## 必须保留\n\n%s",
			strings.TrimSpace(currentDraft), strings.TrimSpace(instruction), keep.String())
	}
	return StageRequest{
		Purpose:      fmt.Sprintf("shortstory-rewrite-scene-%d", scene.Index),
		SystemPrompt: prompts.ShortStoryRewrite,
		UserPrompt:   userPrompt,
		MaxTokens:    sceneMaxTokens(scene.WordBudget),
	}, nil
}

// RunGeneration 用模型适配器执行一次生成调用并返回结果（测试传 mock）。
func RunGeneration(ctx context.Context, model agentcore.ChatModel, req StageRequest) (*StageResult, error) {
	return Generate(ctx, model, req)
}

// decodeJSONLoose 提取响应中的 JSON（容忍 ``` 围栏与前后说明文字）。
func decodeJSONLoose[T any](text string) (T, error) {
	var value T
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	start := strings.IndexAny(trimmed, "[{")
	if start < 0 {
		return value, fmt.Errorf("响应中没有 JSON")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed[start:]))
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	return value, nil
}
