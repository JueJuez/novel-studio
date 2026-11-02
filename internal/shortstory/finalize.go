package shortstory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/project"
)

// 本文件实现 P2-04：作者接受正文 → 全文终审 → 作者终稿确认 → 导出与
// 备份。新短篇拥有独立终审前置，不复用旧 sealed-generation 哈希审核：
// 交付绑定作者确认的确切全文版本，任何场景返工都会改变全文版本从而使
// 旧确认失效（阻断旧哈希审核和未确认交付，计划 §8）。

// ErrDeliveryBlocked 表示交付前置未满足。
var ErrDeliveryBlocked = errors.New("shortstory: 交付前置未满足")

// AssembleFullText 从全部场景草稿的当前已确认版本组装全文：按场景
// index 排序；任一场景草稿未被作者确认即报错（作者接受正文是终审前置）。
// 有章号（chapter>0）的场景输出章标题，chapter=0 的单篇无章号。
func AssembleFullText(auth *authoring.Store, scenes []Scene) (string, []string, error) {
	var confirmedScenes []Scene
	for _, scene := range scenes {
		state, err := auth.AuthorApprovalForHead(SceneTargetID(scene.Index))
		if err != nil {
			return "", nil, fmt.Errorf("%w: 场景 %d 草稿缺失: %v", ErrDeliveryBlocked, scene.Index, err)
		}
		if !state.Approved {
			return "", nil, fmt.Errorf("%w: 场景 %d 草稿未经作者确认", ErrDeliveryBlocked, scene.Index)
		}
		confirmedScenes = append(confirmedScenes, scene)
	}
	if len(confirmedScenes) == 0 {
		return "", nil, fmt.Errorf("%w: 没有可组装的场景", ErrDeliveryBlocked)
	}
	hasChapters := false
	for _, scene := range confirmedScenes {
		if scene.Chapter > 0 {
			hasChapters = true
			break
		}
	}
	var builder strings.Builder
	var versionIDs []string
	for _, scene := range confirmedScenes {
		head, err := auth.Head(SceneTargetID(scene.Index))
		if err != nil {
			return "", nil, err
		}
		versionIDs = append(versionIDs, head.VersionID)
		if hasChapters && scene.Chapter > 0 {
			builder.WriteString(fmt.Sprintf("## 第%d章\n\n", scene.Chapter))
		}
		builder.WriteString(strings.TrimSpace(head.Content))
		builder.WriteString("\n\n")
	}
	return strings.TrimSpace(builder.String()), versionIDs, nil
}

// CommitFullText 把组装结果提交为 fulltext 目标的新版本（内容寻址：
// 场景返工必然改变全文版本，旧终审/旧确认自动失效）。同内容幂等。
func CommitFullText(auth *authoring.Store, scenes []Scene) (*authoring.TextVersion, error) {
	if err := EnsureTarget(auth, TargetFullText); err != nil {
		return nil, err
	}
	head, err := auth.Head(TargetFullText)
	if err != nil {
		return nil, err
	}
	content, sceneVersions, err := AssembleFullText(auth, scenes)
	if err != nil {
		return nil, err
	}
	// 幂等收口：场景版本未变时重复 Commit 必须返回同一 head。候选寻址
	// 键含 base 版本——若每次都重新 Propose，base 随上次提交推进，版本
	// 链会无谓生长并把审阅/确认绑定甩成陈旧（P5-02 实测两连跳）。
	if head != nil && head.Content == content {
		return head, nil
	}
	// 候选寻址键必须确定性：场景版本不变时重复 Commit 必须命中同一候选
	// （幂等），否则审阅/确认绑定的版本会被时间戳漂移甩下（P5-02 实测：
	// finalize 二次提交产生新 head，旧终审失效）。组装时间由交付清单的
	// exported_at 承载，不进寻址键。
	provenance, err := MarshalCanonical(map[string]any{
		"scene_versions": sceneVersions,
	})
	if err != nil {
		return nil, err
	}
	candidate, err := auth.Propose(TargetFullText, head.VersionID, "manual", "fulltext-assembly\n"+provenance, content)
	if err != nil {
		return nil, err
	}
	version, err := auth.Accept(TargetFullText, candidate.CandidateID)
	if err != nil {
		return nil, err
	}
	return version, nil
}

// FinalizeGateCheck 校验全文终审前置（新短篇的独立终审合同，不触碰旧
// 哈希审核）：
//  1. fulltext 当前版本与组装结果逐字节一致（防止确认后正文漂移）；
//  2. 该版本上有终审报告且无未豁免的 blocker；
//  3. 作者终稿确认绑定当前版本。
func FinalizeGateCheck(auth *authoring.Store, scenes []Scene) error {
	expected, _, err := AssembleFullText(auth, scenes)
	if err != nil {
		return err
	}
	head, err := auth.Head(TargetFullText)
	if err != nil {
		return fmt.Errorf("%w: 全文尚未组装: %v", ErrDeliveryBlocked, err)
	}
	if head.Content != expected {
		return fmt.Errorf("%w: 全文版本与当前场景组装结果不一致（正文已返工，需重新终审）", ErrDeliveryBlocked)
	}
	report, err := LoadLatestReview(auth, TargetFullText)
	if err != nil {
		return fmt.Errorf("%w: 当前全文版本缺少终审报告: %v", ErrDeliveryBlocked, err)
	}
	if report.ContentVersionID != head.VersionID || report.ContentDigest != head.ContentDigest {
		return fmt.Errorf("%w: 终审报告绑定的是旧版本（阻断旧哈希审核复用）", ErrDeliveryBlocked)
	}
	for _, issue := range report.Issues {
		if issue.Severity != "blocker" {
			continue
		}
		// 作者终裁豁免：blocker 的 issue 来源被作者显式拒绝（裁定不成立/
		// 不构成交付障碍）才放行；未决、接受、读取失败一律阻断。
		waived, err := ReviewIssueWaived(auth, report, issue)
		if err != nil || !waived {
			return fmt.Errorf("%w: 终审存在未豁免 blocker（%s: %s）", ErrDeliveryBlocked, issue.Type, issue.Description)
		}
	}
	state, err := auth.AuthorApprovalForHead(TargetFullText)
	if err != nil {
		return fmt.Errorf("%w: 终稿确认读取失败: %v", ErrDeliveryBlocked, err)
	}
	if !state.Approved {
		return fmt.Errorf("%w: 作者终稿确认缺失（当前全文版本 %s）", ErrDeliveryBlocked, head.VersionID)
	}
	return nil
}

// ExportResult 是一次交付导出的结果。
type ExportResult struct {
	MarkdownPath   string `json:"markdown_path"`
	TextPath       string `json:"text_path"`
	ManifestPath   string `json:"manifest_path"`
	BackupDir      string `json:"backup_dir"`
	FullTextDigest string `json:"full_text_digest"`
}

// DeliveryManifest 是交付清单：正文哈希与各文件哈希一致可验。
type DeliveryManifest struct {
	SchemaVersion    string            `json:"schema_version"`
	ProjectID        string            `json:"project_id"`
	Title            string            `json:"title"`
	FullTextVersion  string            `json:"fulltext_version"`
	FullTextDigest   string            `json:"fulltext_digest"`
	MarkdownDigest   string            `json:"markdown_digest"`
	TextDigest       string            `json:"text_digest"`
	AuthorApprovalID string            `json:"author_approval_id"`
	SceneVersions    map[string]string `json:"scene_versions,omitempty"`
	ExportedAt       string            `json:"exported_at"`
}

// Export 导出 Markdown/TXT 与备份。交付前置：FinalizeGateCheck 全部通过
// （作者终稿确认绑定确切全文版本）；导出的三个文件与清单哈希互相一致。
func Export(runRoot, title string, scenes []Scene) (*ExportResult, *DeliveryManifest, error) {
	auth := authoring.Open(runRoot)
	if err := FinalizeGateCheck(auth, scenes); err != nil {
		return nil, nil, err
	}
	head, err := auth.Head(TargetFullText)
	if err != nil {
		return nil, nil, err
	}
	state, err := auth.AuthorApprovalForHead(TargetFullText)
	if err != nil || !state.Approved {
		return nil, nil, fmt.Errorf("%w: 作者终稿确认缺失", ErrDeliveryBlocked)
	}
	exportDir := filepath.Join(runRoot, "export")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return nil, nil, err
	}
	safeName := sanitizeExportName(title)
	markdown := head.Content + "\n"
	plain := plainText(head.Content)
	mdPath := filepath.Join(exportDir, safeName+".md")
	txtPath := filepath.Join(exportDir, safeName+".txt")
	if err := os.WriteFile(mdPath, []byte(markdown), 0o644); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(txtPath, []byte(plain), 0o644); err != nil {
		return nil, nil, err
	}
	manifest := &DeliveryManifest{
		SchemaVersion:    "short-story-delivery.v1",
		Title:            title,
		FullTextVersion:  head.VersionID,
		FullTextDigest:   head.ContentDigest,
		MarkdownDigest:   domain.ComputeArcArtifactSHA256([]byte(markdown)),
		TextDigest:       domain.ComputeArcArtifactSHA256([]byte(plain)),
		AuthorApprovalID: state.Record.ApprovalID,
		ExportedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if manifestObj, err := project.Load(runRoot); err == nil {
		manifest.ProjectID = manifestObj.ProjectID
	}
	manifestPath := filepath.Join(exportDir, "manifest.json")
	manifestRaw, err := MarshalCanonical(manifest)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(manifestPath, []byte(manifestRaw), 0o644); err != nil {
		return nil, nil, err
	}
	// 本地备份：整份交付复制到 export/backup/<时间戳>/。
	backupDir := filepath.Join(exportDir, "backup", time.Now().UTC().Format("20060102T150405"))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return nil, nil, err
	}
	for _, src := range []string{mdPath, txtPath, manifestPath} {
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(backupDir, filepath.Base(src)), data, 0o644); err != nil {
			return nil, nil, err
		}
	}
	return &ExportResult{
		MarkdownPath:   mdPath,
		TextPath:       txtPath,
		ManifestPath:   manifestPath,
		BackupDir:      backupDir,
		FullTextDigest: head.ContentDigest,
	}, manifest, nil
}

// plainText 把 Markdown 正文压成纯文本：去章标题标记，段落保持。
func plainText(content string) string {
	var builder strings.Builder
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "## ")
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n") + "\n"
}

func sanitizeExportName(title string) string {
	name := strings.TrimSpace(title)
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", "\n", "_")
	name = replacer.Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "short-story"
	}
	return name
}
