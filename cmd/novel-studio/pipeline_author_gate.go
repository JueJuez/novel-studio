package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
)

// 本文件实现 P4-03：把 P0-03 定位的作者确认边界实施到长篇发布链。
//
// 设计（对齐计划 §6.2 与 docs/design-audits/author-confirmation-hook-audit-20261031.md）：
//   - 只有「存在合法 project.json 且 pipeline_kind=long 且 collaborative=true」
//     的作品启用门禁；非协作策略与无 manifest 的旧工程行为逐字节不变
//     （原默认策略和旧 receipt 不静默改变）。
//   - 门禁在持久化的作者确认记录（authoring 层 AuthorApproval，绑定
//     目标与确切正文事务标识）之上机械强制：任何路径（CLI/API/恢复）
//     都必须穿过同一函数，未确认无法旁路。
//   - 等待确认不占执行锁：门禁以错误中止当前调用，正文事务停在原阶段；
//     作者确认落盘后重跑即可继续（恢复路径重验版本）。
//   - 本批已接线：正文发布前（pipelineAdvanceChapterRenderPublished，
//     全部发布调用点的唯一咽喉）与最终交付前（completePipelineBook，
//     正文.md 导出）。封存前与初始化产物确认两个边界在 P0-03 已定位，
//     留待下一批与封存事务一起接线。

// ErrLongAuthorConfirmationRequired 是协作长篇等待作者确认的哨兵错误：
// 调用方可据此区分「等待作者」与一般失败。
var ErrLongAuthorConfirmationRequired = errors.New("长篇协作边界：等待作者确认")

// deriveLongProjectRoot 从引擎目录推导作品根。仅当布局为标准
// projectDir/output/novel 且根目录存在合法 manifest 文件时返回根；
// 其余情形（自定义 OutputDir、非标准布局）一律不启用门禁。
func deriveLongProjectRoot(liveOutputDir string) (string, bool) {
	engineAbs, err := filepath.Abs(liveOutputDir)
	if err != nil {
		return "", false
	}
	if filepath.Base(engineAbs) != "novel" {
		return "", false
	}
	outputDir := filepath.Dir(engineAbs)
	if filepath.Base(outputDir) != "output" {
		return "", false
	}
	root := filepath.Dir(outputDir)
	if root == string(filepath.Separator) {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(root, project.ManifestFileName)); err != nil {
		return "", false
	}
	return root, true
}

// requireLongAuthorGate 校验协作长篇在某个持久化边界上的作者确认。
// targetID/versionID 绑定确认对象（如章节正文事务标识、交付稿摘要）。
// 返回 nil 表示放行；返回 ErrLongAuthorConfirmationRequired 表示等待作者；
// 其余错误为门禁读取失败（fail-closed：无法核实确认状态时拒绝推进）。
func requireLongAuthorGate(liveOutputDir, targetID, versionID string) error {
	root, ok := deriveLongProjectRoot(liveOutputDir)
	if !ok {
		return nil
	}
	manifest, err := project.Load(root)
	if err != nil {
		// manifest 存在但不可解析：协作开关无法核实。不静默启用门禁，
		// 保持既有行为；manifest 本身的问题由 project.Load 的严格校验
		// 在其它入口暴露。
		return nil
	}
	if !manifest.Collaborative || manifest.PipelineKind != project.PipelineLong {
		return nil
	}
	auth := authoring.Open(root)
	approved, err := auth.HasAuthorApproval(targetID, versionID)
	if err != nil {
		return fmt.Errorf("作者确认门禁读取失败（fail-closed，拒绝推进）: %w", err)
	}
	if !approved {
		return fmt.Errorf("%w: 目标 %s 版本 %s 尚无作者确认；确认落盘后重跑即可继续，等待期间不占执行锁", ErrLongAuthorConfirmationRequired, targetID, versionID)
	}
	return nil
}

// longChapterGateTarget 是章节正文发布边界的门禁目标键。
func longChapterGateTarget(chapter int) string {
	return "long-chapter-" + strconv.Itoa(chapter)
}

// longDeliverGateTarget 是最终交付边界（正文.md）的门禁目标键。
const longDeliverGateTarget = "long-deliver-manuscript"
