package authoring

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

// 本文件实现 P3-03 的"长篇接到共享版本/来源视图"：把长篇引擎已确认的
// 章节正文以只读方式投影成内容寻址的版本视图，供统一界面查看。
//
// 硬边界（计划 §5/§6.2）：
//   - 投影只读：不写长篇目录任何文件、不写 manifest、不新增审批点；
//   - 长篇旧数据/摘要/RAG/正史事务仍由原 Store/封存合同管理，本包不
//     导入 internal/store（依赖方向见 boundaries_test.go 的契约测试）；
//   - 长篇修改继续走原 rebase/返工路径，本视图不提供写入口。

// LongChapterView 是长篇一个已存在章节的只读版本视图。
type LongChapterView struct {
	Chapter       int    `json:"chapter"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	ContentDigest string `json:"content_digest"`
	// Canonical 表示该正文来自长篇正史目录（output/novel/chapters）。
	Canonical bool `json:"canonical"`
}

// LongPipelineView 是一个长篇引擎目录的只读投影。
type LongPipelineView struct {
	EngineDir string            `json:"engine_dir"`
	Chapters  []LongChapterView `json:"chapters"`
}

// ProjectLongPipeline 把长篇引擎目录下的 chapters/*.md 投影为版本视图。
// 章节号取文件名前导数字（01.md → 1），按章号排序；正文哈希按现有
// rune/字节口径计算，供统一界面与短篇视图对齐展示。任何读取失败都
// 显式报错，不静默跳过损坏章节。
func ProjectLongPipeline(engineDir string) (*LongPipelineView, error) {
	chaptersDir := filepath.Join(engineDir, "chapters")
	entries, err := os.ReadDir(chaptersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &LongPipelineView{EngineDir: engineDir}, nil
		}
		return nil, fmt.Errorf("authoring: 长篇章节目录读取失败: %w", err)
	}
	view := &LongPipelineView{EngineDir: engineDir}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		chapter, parseErr := chapterNumberFromName(entry.Name())
		if parseErr != nil {
			return nil, fmt.Errorf("authoring: 章节文件名 %s 无法解析章号: %w", entry.Name(), parseErr)
		}
		path := filepath.Join(chaptersDir, entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("authoring: 章节 %s 读取失败: %w", entry.Name(), readErr)
		}
		view.Chapters = append(view.Chapters, LongChapterView{
			Chapter:       chapter,
			Path:          path,
			Content:       string(raw),
			ContentDigest: domain.ComputeArcArtifactSHA256(raw),
			Canonical:     true,
		})
	}
	sort.Slice(view.Chapters, func(i, j int) bool { return view.Chapters[i].Chapter < view.Chapters[j].Chapter })
	return view, nil
}

// chapterNumberFromName 从 "01.md" / "chapter-12.md" 等名字提取章号。
func chapterNumberFromName(name string) (int, error) {
	base := strings.TrimSuffix(name, ".md")
	digits := base
	if idx := strings.IndexFunc(base, func(r rune) bool { return r < '0' || r > '9' }); idx >= 0 {
		digits = base[:idx]
	}
	if digits == "" {
		return 0, fmt.Errorf("无前导数字")
	}
	value, err := strconv.Atoi(digits)
	if err != nil {
		return 0, err
	}
	return value, nil
}

// StaleSourceEntry 是一条已确认但已被正文推进甩下的来源。
type StaleSourceEntry struct {
	SourceID     string `json:"source_id"`
	Kind         string `json:"kind"`
	Text         string `json:"text"`
	BoundVersion string `json:"bound_version"`
	Reason       string `json:"reason"`
}

// StaleSourceReport 列出目标下已确认但相对当前 head 陈旧的来源
// （P3-04：来源陈旧标记；作者可据此决定刷新、重审或放弃）。
func (s *Store) StaleSourceReport(targetID string) ([]StaleSourceEntry, error) {
	confirmed, err := s.ConfirmedSources(targetID)
	if err != nil {
		return nil, err
	}
	head, err := s.Head(targetID)
	if err != nil {
		return nil, err
	}
	var stale []StaleSourceEntry
	for _, ref := range confirmed {
		if ref.VersionID == head.VersionID {
			continue
		}
		stale = append(stale, StaleSourceEntry{
			SourceID:     ref.SourceID,
			Kind:         ref.Kind,
			Text:         ref.Text,
			BoundVersion: ref.VersionID,
			Reason:       "version_superseded",
		})
	}
	return stale, nil
}

// RequiresReview 报告目标当前版本是否缺少审阅（P3-04：必要重审）——
// head 推进后旧审阅不再覆盖新版本，接受修改后该目标必须重审。
func (s *Store) RequiresReview(targetID string) (bool, error) {
	head, err := s.Head(targetID)
	if err != nil {
		return false, err
	}
	confirmed, err := s.ConfirmedSources(targetID)
	if err != nil {
		return false, err
	}
	for _, ref := range confirmed {
		if ref.Kind != SourceReviewIssue || ref.VersionID != head.VersionID {
			continue
		}
		return false, nil
	}
	return true, nil
}
