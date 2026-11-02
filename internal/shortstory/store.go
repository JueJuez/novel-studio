package shortstory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/project"
)

// Store 管理 <runRoot>/short/ 下的状态与工件。写入策略：工件 no-clobber
// （崩溃重试幂等）、状态文件临时文件+rename 原子替换。跨进程项目级互斥
// 在 P2 接入存储执行锁时统一接线，本包不重复造锁。
type Store struct {
	dir string // <runRoot>/short
}

// DirFor 返回作品根对应的短篇状态目录（未创建也返回）。
func DirFor(runRoot string) string {
	return filepath.Join(runRoot, project.ShortStateDirName)
}

// Create 在作品根建立短篇状态目录。要求 manifest 已声明 short 管线且
// 引擎协议匹配；创意（brief）是首个持久化工件，随初始化一起落盘。
// 已有状态时拒绝（不覆盖）。
func Create(runRoot string, manifest *project.Manifest, brief Brief) (*Store, *State, error) {
	if manifest == nil {
		return nil, nil, fmt.Errorf("shortstory create: 缺少 project manifest")
	}
	if manifest.PipelineKind != project.PipelineShort {
		return nil, nil, fmt.Errorf("shortstory create: manifest 管线是 %s，拒绝以短篇启动", manifest.PipelineKind)
	}
	if manifest.EngineProtocol != project.EngineProtocolShort {
		return nil, nil, fmt.Errorf("shortstory create: manifest 引擎协议 %q 与短篇状态机 %s 不匹配", manifest.EngineProtocol, project.EngineProtocolShort)
	}
	if strings.TrimSpace(brief.Idea) == "" {
		return nil, nil, fmt.Errorf("shortstory create: 创意（brief.idea）不能为空")
	}
	dir := DirFor(runRoot)
	statePath := filepath.Join(dir, StateFileName)
	if _, err := os.Stat(statePath); err == nil {
		return nil, nil, fmt.Errorf("shortstory create: %s 已存在，拒绝覆盖", statePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, ArtifactsDirName), 0o755); err != nil {
		return nil, nil, err
	}
	store := &Store{dir: dir}
	now := time.Now().UTC().Format(time.RFC3339)
	state := InitialState(manifest.ProjectID, time.Now())
	rel, digest, err := store.writeArtifact(StageBrief, nil, brief)
	if err != nil {
		return nil, nil, err
	}
	state.History = []StageRecord{{Stage: StageBrief, Artifact: rel, ArtifactDigest: digest, At: now}}
	state.UpdatedAt = now
	if err := writeStateFile(statePath, state); err != nil {
		return nil, nil, err
	}
	return store, state, nil
}

// Open 是恢复入口：加载已有短篇状态目录；不存在时返回包裹
// os.ErrNotExist 的错误，调用方据此区分“未初始化的短篇”。
func Open(runRoot string) (*Store, error) {
	dir := DirFor(runRoot)
	if _, err := os.Stat(filepath.Join(dir, StateFileName)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("shortstory open: %w", err)
		}
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir 返回状态目录路径。
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Load 读取状态并做恢复校验：schema、阶段转移历史、以及每条历史引用的
// 工件必须存在且摘要一致。任何不一致都显式报错，不静默降级。
func (s *Store) Load() (*State, error) {
	if s == nil {
		return nil, fmt.Errorf("shortstory: store 为空")
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, StateFileName))
	if err != nil {
		return nil, fmt.Errorf("shortstory load: %w", err)
	}
	var state State
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return nil, fmt.Errorf("shortstory load: state.json 解析失败: %w", err)
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	for i, record := range state.History {
		if record.Artifact == "" {
			continue
		}
		path := filepath.Join(s.dir, filepath.FromSlash(record.Artifact))
		artifactRaw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("shortstory load: 历史[%d] 工件缺失 %s: %w", i, record.Artifact, err)
		}
		if digest := domain.ComputeArcArtifactSHA256(artifactRaw); digest != record.ArtifactDigest {
			return nil, fmt.Errorf("shortstory load: 历史[%d] 工件 %s 摘要不一致（恢复校验失败）", i, record.Artifact)
		}
	}
	return &state, nil
}

// Advance 推进到下一阶段并持久化该阶段工件。重试安全：工件按 no-clobber
// 写入，若崩溃发生在工件落盘之后、状态落盘之前，重试以相同字节完成收尾；
// 字节不同则明确报冲突。
func (s *Store) Advance(next Stage, artifact any) (*State, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	if err := ValidateStageTransition(state.Stage, next); err != nil {
		return nil, err
	}
	rel, digest, err := s.writeArtifact(next, state.History, artifact)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	state.History = append(state.History, StageRecord{
		Stage:          next,
		Artifact:       rel,
		ArtifactDigest: digest,
		At:             now,
	})
	state.Stage = next
	state.UpdatedAt = now
	if err := writeStateFile(filepath.Join(s.dir, StateFileName), state); err != nil {
		return nil, err
	}
	return state, nil
}

// SetRunState 变更运行状态并追加事件。运行状态不改变正文阶段，
// waiting_author/failed/cancelled 不冒充阶段完成。
func (s *Store) SetRunState(next RunState, reason string) (*State, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	if !next.Valid() {
		return nil, fmt.Errorf("shortstory: 运行状态 %q 无效", string(next))
	}
	if next == state.RunState {
		return state, nil
	}
	state.RunEvents = append(state.RunEvents, RunEvent{
		From:   state.RunState,
		To:     next,
		Reason: reason,
		At:     time.Now().UTC().Format(time.RFC3339),
	})
	state.RunState = next
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeStateFile(filepath.Join(s.dir, StateFileName), state); err != nil {
		return nil, err
	}
	return state, nil
}

// writeArtifact 序列化工件并按 no-clobber 语义落盘，返回相对路径与摘要。
// 崩溃重试时同字节写入幂等收尾，异字节明确冲突。
func (s *Store) writeArtifact(stage Stage, history []StageRecord, artifact any) (string, string, error) {
	raw, err := json.Marshal(artifact)
	if err != nil {
		return "", "", fmt.Errorf("shortstory: 工件序列化失败: %w", err)
	}
	rel := artifactRelPath(stage, history)
	abs := filepath.Join(s.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", "", err
	}
	digest := domain.ComputeArcArtifactSHA256(raw)
	if existing, statErr := os.ReadFile(abs); statErr == nil {
		if domain.ComputeArcArtifactSHA256(existing) != digest {
			return "", "", fmt.Errorf("shortstory: 工件 %s 已存在且内容不同，拒绝覆盖", rel)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", statErr
	} else if err := os.WriteFile(abs, raw, 0o644); err != nil {
		return "", "", err
	}
	return rel, digest, nil
}

// artifactRelPath 生成阶段工件的相对路径：artifacts/<stage>/<stage>.<rev>.json。
// rev = 该阶段已有历史条数 + 1，修稿循环（review→draft）自然产生新修订号。
func artifactRelPath(stage Stage, history []StageRecord) string {
	rev := 1
	for _, record := range history {
		if record.Stage == stage {
			rev++
		}
	}
	return fmt.Sprintf("%s/%s/%s.%d.json", ArtifactsDirName, string(stage), string(stage), rev)
}

// writeStateFile 原子写入状态文件：临时文件 + rename，避免半份状态。
func writeStateFile(path string, state *State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadBrief 读取初始化时落盘的创意（brief）工件。brief 只在 init 写入
// 一次（rev=1，修稿循环不回写 brief），路径确定。
func (s *Store) LoadBrief() (*Brief, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, ArtifactsDirName, "brief", "brief.1.json"))
	if err != nil {
		return nil, fmt.Errorf("shortstory brief: %w", err)
	}
	var brief Brief
	if err := json.Unmarshal(raw, &brief); err != nil {
		return nil, fmt.Errorf("shortstory brief: 解析失败: %w", err)
	}
	return &brief, nil
}
