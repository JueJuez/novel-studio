// Package shortstory 实现短篇管线的领域状态机与持久工件（dual-pipeline
// plan §6.1/§6.3）。它与长篇推演链零依赖：不导入 host/agents/tools/store，
// 只依赖 internal/domain 的哈希工具与 internal/project 的 manifest 合同，
// 因此短篇启动不需要世界推演、RAG 或 Qdrant。
//
// 阶段流（§6.1 拟定）：
//
//	brief → structure → scenes → draft → review → author_approval → finalize → delivered
//	                            ↑        ↓
//	                            └─ revise ┘
//
// waiting_author / failed / cancelled 是运行状态（RunState），不是正文阶段，
// 不能冒充阶段完成。每阶段产物、状态与历史记录全部持久化，恢复 = Load。
package shortstory

import (
	"fmt"
	"strings"
	"time"
)

const (
	// StateSchemaVersion 是短篇状态文件格式版本。
	StateSchemaVersion = "short-story-state.v1"
	// StateFileName 位于 <runRoot>/short/state.json。
	StateFileName = "state.json"
	// ArtifactsDirName 保存各阶段工件（相对 short/ 目录）。
	ArtifactsDirName = "artifacts"
)

// Stage 是正文阶段；顺序与转移由 allowedTransitions 固定。
type Stage string

const (
	StageBrief          Stage = "brief"
	StageStructure      Stage = "structure"
	StageScenes         Stage = "scenes"
	StageDraft          Stage = "draft"
	StageReview         Stage = "review"
	StageAuthorApproval Stage = "author_approval"
	StageFinalize       Stage = "finalize"
	StageDelivered      Stage = "delivered"
)

// Valid 报告阶段是否受支持。
func (s Stage) Valid() bool {
	_, ok := allowedTransitions[s]
	return ok
}

// allowedTransitions 固定阶段转移表。review→draft 是唯一的回退边
// （修稿循环）；delivered 为终态；其余一律前进。
var allowedTransitions = map[Stage][]Stage{
	StageBrief:          {StageStructure},
	StageStructure:      {StageScenes},
	StageScenes:         {StageDraft},
	StageDraft:          {StageReview},
	StageReview:         {StageDraft, StageAuthorApproval},
	StageAuthorApproval: {StageFinalize},
	StageFinalize:       {StageDelivered},
	StageDelivered:      {},
}

// ValidateStageTransition 校验一次阶段推进是否合法。
func ValidateStageTransition(current, next Stage) error {
	if !current.Valid() {
		return fmt.Errorf("shortstory: 当前阶段 %q 无效", string(current))
	}
	if !next.Valid() {
		return fmt.Errorf("shortstory: 目标阶段 %q 无效", string(next))
	}
	for _, candidate := range allowedTransitions[current] {
		if candidate == next {
			return nil
		}
	}
	return fmt.Errorf("shortstory: 阶段不能从 %s 推进到 %s", current, next)
}

// RunState 是运行状态，与正文阶段分开记录。
type RunState string

const (
	RunActive        RunState = "active"
	RunWaitingAuthor RunState = "waiting_author"
	RunFailed        RunState = "failed"
	RunCancelled     RunState = "cancelled"
)

// Valid 报告运行状态是否受支持。
func (r RunState) Valid() bool {
	switch r {
	case RunActive, RunWaitingAuthor, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// Brief 是创意澄清阶段的产物。
type Brief struct {
	Idea              string   `json:"idea"`
	AuthorConstraints []string `json:"author_constraints,omitempty"`
}

// StructureCharacter 记录人物欲望与选择（§6.1：结构中记录人物欲望与选择）。
type StructureCharacter struct {
	Name   string `json:"name"`
	Desire string `json:"desire"`
	Choice string `json:"choice"`
}

// Structure 是故事结构产物。结构模板可选，不强制三幕式；结局允许开放式，
// 检查的是作者选定的兑现义务（Promises）。
type Structure struct {
	Conflict          string               `json:"conflict"`
	Characters        []StructureCharacter `json:"characters"`
	EndingDirection   string               `json:"ending_direction"`
	Promises          []string             `json:"promises,omitempty"`
	AuthorConstraints []string             `json:"author_constraints,omitempty"`
	TemplateName      string               `json:"template_name,omitempty"`
}

// Scene 是单个场景计划。Chapter=0 表示单篇无章号；字数预算沿用项目 rune 口径。
type Scene struct {
	Index          int      `json:"index"`
	Chapter        int      `json:"chapter,omitempty"`
	POV            string   `json:"pov"`
	Goal           string   `json:"goal"`
	Obstacle       string   `json:"obstacle"`
	Turn           string   `json:"turn,omitempty"`
	CausalDeps     []int    `json:"causal_deps,omitempty"`
	Facts          []string `json:"facts,omitempty"`
	WordBudget     int      `json:"word_budget,omitempty"`
	ContinuityPrev string   `json:"continuity_prev,omitempty"`
	ContinuityNext string   `json:"continuity_next,omitempty"`
}

// SceneDraft 是一个场景的正文候选。候选不自动成为正式故事；作者接受
// 与模型审核分别记录（确认合同在 P1-03 的 authoring 层落地）。
type SceneDraft struct {
	SceneIndex int    `json:"scene_index"`
	Body       string `json:"body"`
}

// ReviewReport 记录全文/局部审阅结果；Issue 集合与正文哈希绑定的细化
// 在 P2-03 落地，此处先保证审阅工件可持久、可恢复。
type ReviewReport struct {
	Target  string   `json:"target"`
	Summary string   `json:"summary"`
	Issues  []string `json:"issues,omitempty"`
}

// AuthorApproval 是作者对终稿候选的确认。完整确认合同（父版本、规则
// 摘要、失效条件）在 P1-03 的 authoring 层落地；此处先绑定被批准内容的
// 摘要，保证确认对象可追溯。
type AuthorApproval struct {
	ApprovedDigest string `json:"approved_digest"`
	Note           string `json:"note,omitempty"`
}

// FinalizeRecord 是全文终审准备阶段的记录（导出前的一致性核对）。
type FinalizeRecord struct {
	FullTextDigest string `json:"full_text_digest"`
	Notes          string `json:"notes,omitempty"`
}

// DeliveryRecord 记录交付结果（Markdown/TXT 导出与备份位置）。
type DeliveryRecord struct {
	FullTextDigest string `json:"full_text_digest"`
	ExportPath     string `json:"export_path,omitempty"`
	BackupPath     string `json:"backup_path,omitempty"`
}

// StageRecord 是一条阶段历史：工件路径 + 内容摘要 + 时间，追加不可变。
type StageRecord struct {
	Stage          Stage  `json:"stage"`
	Artifact       string `json:"artifact,omitempty"`
	ArtifactDigest string `json:"artifact_digest,omitempty"`
	At             string `json:"at"`
}

// RunEvent 记录一次运行状态变化（waiting_author/failed/cancelled 等），
// 与正文阶段历史分开记账。
type RunEvent struct {
	From   RunState `json:"from"`
	To     RunState `json:"to"`
	Reason string   `json:"reason,omitempty"`
	At     string   `json:"at"`
}

// State 是 <runRoot>/short/state.json 的内存表示。
type State struct {
	SchemaVersion string        `json:"schema_version"`
	ProjectID     string        `json:"project_id"`
	Stage         Stage         `json:"stage"`
	RunState      RunState      `json:"run_state"`
	History       []StageRecord `json:"history"`
	RunEvents     []RunEvent    `json:"run_events,omitempty"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

// InitialState 是 Create 时的状态：停在 brief，等待创意澄清。
func InitialState(projectID string, now time.Time) *State {
	stamp := now.UTC().Format(time.RFC3339)
	return &State{
		SchemaVersion: StateSchemaVersion,
		ProjectID:     projectID,
		Stage:         StageBrief,
		RunState:      RunActive,
		CreatedAt:     stamp,
		UpdatedAt:     stamp,
	}
}

// Validate 校验状态文件自洽性；历史为空时阶段必须是 brief。
func (s *State) Validate() error {
	if s.SchemaVersion != StateSchemaVersion {
		return fmt.Errorf("shortstory state: 不支持的 schema_version %q（当前支持 %s），明确拒绝", s.SchemaVersion, StateSchemaVersion)
	}
	if !s.Stage.Valid() {
		return fmt.Errorf("shortstory state: 阶段 %q 无效", string(s.Stage))
	}
	if !s.RunState.Valid() {
		return fmt.Errorf("shortstory state: 运行状态 %q 无效", string(s.RunState))
	}
	if len(s.History) == 0 && s.Stage != StageBrief {
		return fmt.Errorf("shortstory state: 无历史却停在阶段 %s", s.Stage)
	}
	if len(s.History) > 0 && s.History[len(s.History)-1].Stage != s.Stage {
		return fmt.Errorf("shortstory state: 最新历史 %s 与当前阶段 %s 不一致", s.History[len(s.History)-1].Stage, s.Stage)
	}
	for i, record := range s.History {
		if !record.Stage.Valid() {
			return fmt.Errorf("shortstory state: 历史[%d] 阶段 %q 无效", i, string(record.Stage))
		}
		if _, err := time.Parse(time.RFC3339, record.At); err != nil {
			return fmt.Errorf("shortstory state: 历史[%d] 时间 %q 不是 RFC3339", i, record.At)
		}
		if i > 0 {
			if err := ValidateStageTransition(s.History[i-1].Stage, record.Stage); err != nil {
				return fmt.Errorf("shortstory state: 历史[%d] %s→%s 非法: %w", i, s.History[i-1].Stage, record.Stage, err)
			}
		} else if record.Stage != StageBrief {
			return fmt.Errorf("shortstory state: 首条历史必须是 brief，得到 %s", record.Stage)
		}
	}
	if s.ProjectID == "" || !strings.HasPrefix(s.ProjectID, "proj-") {
		return fmt.Errorf("shortstory state: project_id %q 无效", s.ProjectID)
	}
	return nil
}
