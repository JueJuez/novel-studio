package shortstory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/authoring"
)

// 短篇管线在 authoring 层的内容目标（计划 §6.3：候选、版本与确认记录
// 存 authoring/）。场景草稿按场景一个目标；fulltext 是合并后的全文目标。
const (
	TargetStructure        = "structure"
	TargetScenes           = "scenes"
	TargetSceneDraftPrefix = "scene-"
	TargetFullText         = "fulltext"
)

// SceneTargetID 返回第 index 个场景草稿的目标 ID。
func SceneTargetID(index int) string {
	return fmt.Sprintf("%s%d", TargetSceneDraftPrefix, index)
}

const (
	// ShortWordBudgetMin/Max 是短篇首批验证的字数区间（计划 §4，rune 口径）。
	ShortWordBudgetMin = 5000
	ShortWordBudgetMax = 30000
	// ContextBudgetRunes 是单次草稿调用的上下文预算（rune 口径）。
	// 超限显式报错走分层审核，绝不静默丢弃结局义务与前文。
	ContextBudgetRunes = 24000
)

// EnsureTarget 为内容目标建立空根版本（幂等）：AI 生成与手改都以候选
// 形式挂在它之上，作者确认后才推进。
func EnsureTarget(auth *authoring.Store, targetID string) error {
	if _, err := auth.Head(targetID); err == nil {
		return nil
	}
	_, err := auth.CommitInitial(targetID, "", "system")
	return err
}

// ProposeGenerated 把 AI 产出登记为候选。生成候选不自动成为正式故事：
// 必须经 Accept（+作者确认）才推进（计划 §6.1）。
func ProposeGenerated(auth *authoring.Store, targetID, task, content string) (*authoring.Candidate, error) {
	head, err := auth.Head(targetID)
	if err != nil {
		return nil, fmt.Errorf("shortstory: 目标 %s 未初始化: %w", targetID, err)
	}
	return auth.Propose(targetID, head.VersionID, "ai", task, content)
}

// ProposeManual 把作者手改登记为 manual 候选（手改同样走候选/确认，
// 不直改正文）。
func ProposeManual(auth *authoring.Store, targetID, content string) (*authoring.Candidate, error) {
	head, err := auth.Head(targetID)
	if err != nil {
		return nil, fmt.Errorf("shortstory: 目标 %s 未初始化: %w", targetID, err)
	}
	return auth.Propose(targetID, head.VersionID, "manual", "author-manual-edit", content)
}

// ConfirmTarget 接受候选并落作者确认：候选内容成为新 head，作者确认
// 绑定该版本。这是结构/场景/正文各确认点的统一收口。
func ConfirmTarget(auth *authoring.Store, targetID, candidateID, rulesSummary string) (*authoring.TextVersion, error) {
	version, err := auth.Accept(targetID, candidateID)
	if err != nil {
		return nil, err
	}
	if _, err := auth.RecordAuthorApproval(targetID, version.VersionID, candidateID, rulesSummary, ""); err != nil {
		return nil, err
	}
	return version, nil
}

// TargetContent 返回目标当前 head 的内容；空根版本返回空串。
func TargetContent(auth *authoring.Store, targetID string) (string, error) {
	head, err := auth.Head(targetID)
	if err != nil {
		return "", err
	}
	return head.Content, nil
}

// MarshalCanonical 把工件序列化为稳定字节（缩进 + 换行），作为候选内容
// 与摘要的统一口径。
func MarshalCanonical(value any) (string, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

// ValidateSceneBudgets 校验场景字数预算：每景 >0、依赖不越界，并给出
// 合计与计划验证区间（5,000—30,000 rune，计划 §4：这是首批验证预算，
// 不是普适文学分类）的比较结果。越界返回 error，由调用方决定是否以
// 显式覆盖继续——不做静默放行。
func ValidateSceneBudgets(scenes []Scene) (int, error) {
	total := 0
	for i, scene := range scenes {
		if scene.WordBudget <= 0 {
			return total, fmt.Errorf("shortstory: 场景 %d word_budget 必须大于 0", scene.Index)
		}
		if scene.Index != i+1 {
			return total, fmt.Errorf("shortstory: 场景 index 必须从 1 连续递增，得到 %d（位置 %d）", scene.Index, i+1)
		}
		for _, dep := range scene.CausalDeps {
			if dep <= 0 || dep >= scene.Index {
				return total, fmt.Errorf("shortstory: 场景 %d 依赖 %d 非法（必须引用更早场景）", scene.Index, dep)
			}
		}
		total += scene.WordBudget
	}
	if len(scenes) == 0 {
		return 0, fmt.Errorf("shortstory: 场景计划为空")
	}
	if total < ShortWordBudgetMin || total > ShortWordBudgetMax {
		return total, fmt.Errorf("shortstory: 字数预算合计 %d 超出首批验证区间 [%d, %d]（rune 口径）", total, ShortWordBudgetMin, ShortWordBudgetMax)
	}
	return total, nil
}

// 场景依赖闭包检查：从指定场景沿 causal_deps 回溯，必须能覆盖该场景
// 引用的全部事实来源（无环由“只能依赖更早场景”保证）。
func checkSceneDAG(scenes []Scene) error {
	byIndex := map[int]Scene{}
	for _, scene := range scenes {
		byIndex[scene.Index] = scene
	}
	visited := map[int]bool{}
	var visit func(index int) error
	visit = func(index int) error {
		if visited[index] {
			return nil
		}
		scene, ok := byIndex[index]
		if !ok {
			return fmt.Errorf("shortstory: 场景依赖了不存在的场景 %d", index)
		}
		visited[index] = true
		for _, dep := range scene.CausalDeps {
			if dep >= index {
				return fmt.Errorf("shortstory: 场景 %d 依赖了不早于自身的场景 %d", index, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, scene := range scenes {
		if err := visit(scene.Index); err != nil {
			return err
		}
	}
	return nil
}

// PromisesCoverage 检查结构承诺在场景计划中的兑现安排（计划 §6.1）：
// 每条承诺必须被某场景的 facts 显式安排，或字面出现在结局方向里
// （由结局刻意保留的承诺在结构里写明）；逐条判定，不允许全局豁免。
func PromisesCoverage(structure Structure, scenes []Scene) []string {
	missing := []string{}
	facts := strings.Builder{}
	for _, scene := range scenes {
		for _, fact := range scene.Facts {
			facts.WriteString(fact)
			facts.WriteString("\n")
		}
	}
	for _, promise := range structure.Promises {
		if strings.Contains(facts.String(), promise) || strings.Contains(structure.EndingDirection, promise) {
			continue
		}
		missing = append(missing, promise)
	}
	return missing
}
