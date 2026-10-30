package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Project 是作品发现的结果：带 manifest 的新作品，或无 manifest 的
// 旧作品（只读，永不写回、永不重分管线）。
type Project struct {
	Root string // 作品根目录（data/runs/<名>/）

	// EngineDir 是管线引擎目录：long 沿用 output/novel 原路径；
	// short 是 <root>/short（尚未创建时也返回目标路径）。
	EngineDir string

	// Manifest 仅新作品非空；legacy 作品为 nil，且本包不提供任何
	// 为 legacy 作品补写 manifest 的 API。
	Manifest *Manifest

	// Legacy 为 true 表示无 manifest 的旧作品：只能只读查看，
	// 管线类型未知，不得由字数或旧 planning_tier 推断。
	Legacy bool
}

// Kind 返回显式管线类型；legacy 作品返回空字符串（未知，不推断）。
func (p *Project) Kind() PipelineKind {
	if p == nil || p.Manifest == nil {
		return ""
	}
	return p.Manifest.PipelineKind
}

// DiscoverRun 发现单个作品根目录下的项目。目录既无 manifest 也无旧作品
// 特征时返回 found=false。manifest 存在但格式未知时返回错误（明确拒绝，
// 不静默跳过）。
func DiscoverRun(runDir string) (*Project, bool, error) {
	info, err := os.Stat(runDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, false, nil
	}
	manifestPath := filepath.Join(runDir, ManifestFileName)
	if _, statErr := os.Stat(manifestPath); statErr == nil {
		manifest, err := Load(runDir)
		if err != nil {
			return nil, false, err
		}
		return &Project{
			Root:      runDir,
			EngineDir: engineDirFor(runDir, manifest.PipelineKind),
			Manifest:  manifest,
		}, true, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, false, statErr
	}
	engineDir, ok, err := legacyEngineDir(runDir)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &Project{Root: runDir, EngineDir: engineDir, Legacy: true}, true, nil
}

// DiscoverRuns 扫描 runs 根下全部作品，按目录名排序。任何一个目录的
// manifest 无法解析都会返回错误：未知格式必须显式暴露，不能静默丢弃。
func DiscoverRuns(runsRoot string) ([]*Project, error) {
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Project
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		project, found, err := DiscoverRun(filepath.Join(runsRoot, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("发现作品 %s: %w", entry.Name(), err)
		}
		if found {
			out = append(out, project)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out, nil
}

// engineDirFor 给出管线引擎目录。long 保持原 output/novel 路径不变
// （计划 §6.3：现有长篇路径保持原位）；short 使用 <root>/short。
func engineDirFor(runDir string, kind PipelineKind) string {
	switch kind {
	case PipelineLong:
		return filepath.Join(runDir, "output", "novel")
	case PipelineShort:
		return filepath.Join(runDir, ShortStateDirName)
	default:
		return ""
	}
}

// legacyEngineDir 复刻 cmd 层 resolveNovelDir 的旧作品布局探测：
// output/novel → novel → 作品根，依次按 meta/progress.json、premise.md、
// brainstorm.md 特征判定（brainstorm.md 在循环内按作品根判定，命中即返回
// 当前候选，与 cmd 层口径一致）。只读，不创建任何文件。
func legacyEngineDir(runDir string) (string, bool, error) {
	candidates := []string{
		filepath.Join(runDir, "output", "novel"),
		filepath.Join(runDir, "novel"),
		runDir,
	}
	for _, cand := range candidates {
		if nonEmptyRegular(filepath.Join(cand, "meta", "progress.json")) ||
			nonEmptyRegular(filepath.Join(cand, "premise.md")) ||
			nonEmptyRegular(filepath.Join(runDir, "brainstorm.md")) {
			return cand, true, nil
		}
	}
	// 待生成项目：只有任务书/种子，产物尚未生成。
	if nonEmptyRegular(filepath.Join(runDir, "prompt.md")) || hasLegacySeedDir(runDir) {
		return filepath.Join(runDir, "output", "novel"), true, nil
	}
	return "", false, nil
}

// hasLegacySeedDir 判断 run 目录下是否有种子材料（legacy_seed_* 目录）。
func hasLegacySeedDir(runDir string) bool {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "legacy_seed") {
			return true
		}
	}
	return false
}

// legacyMarkers 报告目录是否带有旧作品特征。Create 用它在创建 manifest
// 前拒绝旧工程，避免把旧作品重分进任何管线。
func legacyMarkers(runDir string) (bool, error) {
	_, found, err := legacyEngineDir(runDir)
	if err != nil {
		return false, err
	}
	if found {
		return true, nil
	}
	// brainstorm.md 单独出现也是旧作品特征（novels_list 的探测口径）。
	if nonEmptyRegular(filepath.Join(runDir, "brainstorm.md")) {
		return true, nil
	}
	return false, nil
}

func nonEmptyRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}
