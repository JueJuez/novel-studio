// Package project 实现双管线的作品层合同：版本化 manifest、显式管线选择、
// 旧作品只读发现与路径映射（dual-pipeline plan §6.3）。
//
// 关键约束：
//   - 新作品在创建时显式选择 short/long；字数与旧 planning_tier 不作为推断依据。
//   - 无 manifest 的旧工程只读发现，不写回、不重分管线。
//   - 未知 manifest 格式明确拒绝，绝不静默兼容。
package project

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

const (
	// ManifestFileName 位于作品根目录（data/runs/<名>/project.json）。
	ManifestFileName = "project.json"
	// SchemaVersionV1 是当前唯一支持的 manifest 格式。
	SchemaVersionV1 = "novel-studio-project.v1"
	// EngineProtocolLong 沿用长篇 sealed 渲染协议标识。
	EngineProtocolLong = "sealed-chapter-render.v1"
	// EngineProtocolShort 是短篇状态机协议标识（internal/shortstory）。
	EngineProtocolShort = "short-story-state.v1"

	// ShortStateDirName 保存短篇状态与结构/场景工件（计划 §6.3）。
	ShortStateDirName = "short"
	// AuthoringDirName 保存跨管线的候选、版本与作者确认记录（计划 §6.3）。
	AuthoringDirName = "authoring"
)

// PipelineKind 是作品根目录里显式声明的管线类型，创建后不可变更。
type PipelineKind string

const (
	PipelineShort PipelineKind = "short"
	PipelineLong  PipelineKind = "long"
)

// Valid 报告该管线类型是否受支持；空值与未知值都视为无效。
func (k PipelineKind) Valid() bool {
	return k == PipelineShort || k == PipelineLong
}

func (k PipelineKind) engineProtocol() string {
	switch k {
	case PipelineLong:
		return EngineProtocolLong
	case PipelineShort:
		return EngineProtocolShort
	default:
		return ""
	}
}

// Manifest 是作品根目录 project.json 的内存表示（计划 §6.3 最小增量）。
type Manifest struct {
	SchemaVersion  string       `json:"schema_version"`
	ProjectID      string       `json:"project_id"`
	Title          string       `json:"title"`
	Language       string       `json:"language"`
	PipelineKind   PipelineKind `json:"pipeline_kind"`
	Collaborative  bool         `json:"collaborative"`
	EngineProtocol string       `json:"engine_protocol"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
}

// CreateOptions 是新建作品时的显式选择；没有默认管线。
type CreateOptions struct {
	Title         string
	Language      string // 空值取 "zh"
	PipelineKind  PipelineKind
	Collaborative bool
}

// Create 在作品根目录写入 project.json。目录里已存在 manifest、或该目录
// 带有旧作品特征（legacy 标记）时拒绝创建：旧工程不写回、不重分管线。
func Create(runRoot string, opts CreateOptions) (*Manifest, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return nil, fmt.Errorf("project manifest: title 不能为空")
	}
	if !opts.PipelineKind.Valid() {
		return nil, fmt.Errorf("project manifest: pipeline_kind 必须显式选择 short 或 long，得到 %q", string(opts.PipelineKind))
	}
	if info, err := os.Stat(filepath.Join(runRoot, ManifestFileName)); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("project manifest: %s 已存在，拒绝覆盖", filepath.Join(runRoot, ManifestFileName))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if legacy, err := legacyMarkers(runRoot); err != nil {
		return nil, err
	} else if legacy {
		return nil, fmt.Errorf("project manifest: %s 是无 manifest 的旧作品，不写回 manifest、不重分管线", runRoot)
	}
	language := strings.TrimSpace(opts.Language)
	if language == "" {
		language = "zh"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := &Manifest{
		SchemaVersion:  SchemaVersionV1,
		ProjectID:      newProjectID(),
		Title:          strings.TrimSpace(opts.Title),
		Language:       language,
		PipelineKind:   opts.PipelineKind,
		Collaborative:  opts.Collaborative,
		EngineProtocol: opts.PipelineKind.engineProtocol(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return nil, err
	}
	if err := writeFileNoClobber(filepath.Join(runRoot, ManifestFileName), raw); err != nil {
		return nil, fmt.Errorf("project manifest: 写入失败: %w", err)
	}
	return manifest, nil
}

// Load 读取并校验作品 manifest。文件不存在时返回包裹 os.ErrNotExist 的错误，
// 调用方据此走旧作品只读发现路径。
func Load(runRoot string) (*Manifest, error) {
	path := filepath.Join(runRoot, ManifestFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("project manifest: %w", err)
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("project manifest: %s 解析失败: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// Validate 校验 manifest 的自洽性。未知 schema_version 明确拒绝，
// pipeline_kind 必须显式且与引擎协议一致。
func (m *Manifest) Validate() error {
	const prefix = "project manifest"
	if m == nil {
		return fmt.Errorf("%s: 为空", prefix)
	}
	if m.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("%s: 不支持的 schema_version %q（当前支持 %s），明确拒绝写入", prefix, m.SchemaVersion, SchemaVersionV1)
	}
	if !m.PipelineKind.Valid() {
		return fmt.Errorf("%s: pipeline_kind %q 不是显式的 short/long 选择", prefix, string(m.PipelineKind))
	}
	if want := m.PipelineKind.engineProtocol(); m.EngineProtocol != want {
		return fmt.Errorf("%s: engine_protocol %q 与管线 %s 不匹配（应为 %s）", prefix, m.EngineProtocol, m.PipelineKind, want)
	}
	if !strings.HasPrefix(m.ProjectID, "proj-") || len(m.ProjectID) != len("proj-")+32 {
		return fmt.Errorf("%s: project_id %q 不是稳定的 proj-<32hex> 标识", prefix, m.ProjectID)
	}
	for _, r := range strings.TrimPrefix(m.ProjectID, "proj-") {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return fmt.Errorf("%s: project_id %q 含非 hex 字符", prefix, m.ProjectID)
		}
	}
	if strings.TrimSpace(m.Title) == "" {
		return fmt.Errorf("%s: title 为空", prefix)
	}
	if strings.TrimSpace(m.Language) == "" {
		return fmt.Errorf("%s: language 为空", prefix)
	}
	for name, value := range map[string]string{"created_at": m.CreatedAt, "updated_at": m.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("%s: %s 不是 RFC3339 时间: %q", prefix, name, value)
		}
	}
	return nil
}

// newProjectID 生成 proj-<32hex> 的稳定标识：一旦写入 manifest 便不再变化。
func newProjectID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("project manifest: 系统熵源不可用: " + err.Error())
	}
	return "proj-" + hex.EncodeToString(buf)
}

// writeFileNoClobber 原子写入且目标存在时失败：硬链接到最终名，
// 保证并发创建不会互相覆盖（对齐存储层 no-replace 语义）。
func writeFileNoClobber(path string, raw []byte) error {
	digest := domain.ComputeArcArtifactSHA256(raw)
	suffix := strings.NewReplacer(":", "", "-", "").Replace(digest[len(digest)-16:])
	tmp := path + ".tmp-" + suffix
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Link(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(tmp)
}
