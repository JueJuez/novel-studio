// Package workbench 实现统一写作工作台的 Web API（计划 P4-01/P4-02）。
//
// 边界（计划 §5/§6.3）：
//   - 本地默认绑定回环地址；写路径全部经过 authoring/shortstory 的
//     授权入口——前端只提交意图，不直接改项目文件；
//   - 事件以服务端持久工件为准（状态机历史、候选、确认记录），SSE
//     只推当前摘要，断线后客户端用全量接口校准，刷新/重启不丢等待
//     和候选；
//   - legacy/long 作品只读查看，意图端点显式拒绝；
//   - 不显示原始思维链——事件与详情不含模型内部推理。
package workbench

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
	"github.com/chenhongyang/novel-studio/internal/shortstory"
)

// Server 是工作台 API 服务。StaticDir 非空时托管工作台前端构建产物
// （services/workbench/dist，可选；非 /api 路径回退到静态文件）。
type Server struct {
	RunsRoot  string
	StaticDir string
}

// New 构建工作台服务。
func New(runsRoot string) *Server {
	return &Server{RunsRoot: runsRoot}
}

// Handler 返回全部路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/projects", s.handleProjects)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("POST /api/projects/{name}/author-gate", s.handleAuthorGate)
	mux.HandleFunc("GET /api/projects/{name}", s.handleProjectDetail)
	mux.HandleFunc("GET /api/projects/{name}/events", s.handleEvents)
	mux.HandleFunc("GET /api/projects/{name}/stream", s.handleStream)
	mux.HandleFunc("GET /api/projects/{name}/diff", s.handleDiff)
	mux.HandleFunc("GET /api/projects/{name}/long", s.handleLongView)
	mux.HandleFunc("POST /api/projects/{name}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/projects/{name}/targets/{target}/candidates", s.handleProposeManual)
	mux.HandleFunc("POST /api/projects/{name}/targets/{target}/confirm", s.handleConfirm)
	mux.HandleFunc("POST /api/projects/{name}/targets/{target}/reject", s.handleReject)
	if s.StaticDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(s.StaticDir)))
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC().Format(time.RFC3339)})
}

// projectRef 按 name 在发现结果中定位作品：名称永远来自发现列表，
// 不拼接原始输入（拒绝目录逃逸）。
func (s *Server) projectRef(name string) (*project.Project, bool, error) {
	projects, err := project.DiscoverRuns(s.RunsRoot)
	if err != nil {
		return nil, false, err
	}
	for _, p := range projects {
		if filepath.Base(p.Root) == name {
			return p, true, nil
		}
	}
	return nil, false, nil
}

func (s *Server) shortStores(p *project.Project) (*shortstory.Store, *authoring.Store, error) {
	if p.Legacy || p.Kind() != project.PipelineShort {
		return nil, nil, fmt.Errorf("workbench: 作品 %s 不是短篇管线（kind=%q legacy=%v），意图端点拒绝", filepath.Base(p.Root), p.Kind(), p.Legacy)
	}
	store, err := shortstory.Open(p.Root)
	if err != nil {
		return nil, nil, err
	}
	return store, authoring.Open(p.Root), nil
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := project.DiscoverRuns(s.RunsRoot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type entry struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Legacy    bool   `json:"legacy"`
		EngineDir string `json:"engine_dir"`
		Stage     string `json:"stage,omitempty"`
		RunState  string `json:"run_state,omitempty"`
	}
	out := []entry{}
	for _, p := range projects {
		item := entry{Name: filepath.Base(p.Root), Kind: string(p.Kind()), Legacy: p.Legacy, EngineDir: p.EngineDir}
		if !p.Legacy && p.Kind() == project.PipelineShort {
			if store, err := shortstory.Open(p.Root); err == nil {
				if state, err := store.Load(); err == nil {
					item.Stage = string(state.Stage)
					item.RunState = string(state.RunState)
				}
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// targetSummary 是一个内容目标的当前视图。
type targetSummary struct {
	TargetID          string `json:"target_id"`
	VersionID         string `json:"version_id,omitempty"`
	RootVersionID     string `json:"root_version_id"`
	ContentDigest     string `json:"content_digest,omitempty"`
	AuthorApproved    bool   `json:"author_approved"`
	RequiresReview    bool   `json:"requires_review"`
	PendingCandidates int    `json:"pending_candidates"`
	StaleSources      int    `json:"stale_sources"`
	Content           string `json:"content,omitempty"`
}

func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	detail := map[string]any{
		"name":       filepath.Base(p.Root),
		"kind":       string(p.Kind()),
		"legacy":     p.Legacy,
		"engine_dir": p.EngineDir,
	}
	if p.Manifest != nil {
		detail["manifest"] = p.Manifest
	}
	if p.Legacy || p.Kind() != project.PipelineShort {
		writeJSON(w, http.StatusOK, detail)
		return
	}
	store, auth, err := s.shortStores(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state, err := store.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	detail["state"] = state
	targets := []targetSummary{}
	pendingByTarget := []map[string]any{}
	for _, targetID := range workbenchTargets(auth) {
		summary, err := summarizeTarget(auth, targetID)
		if err != nil {
			continue
		}
		targets = append(targets, *summary)
		if pending, err := listPendingCandidates(auth, targetID); err == nil {
			for _, candidate := range pending {
				pendingByTarget = append(pendingByTarget, map[string]any{
					"target_id":       targetID,
					"candidate_id":    candidate.CandidateID,
					"kind":            candidate.Kind,
					"task":            candidate.Task,
					"base_version_id": candidate.BaseVersionID,
					"created_at":      candidate.CreatedAt,
				})
			}
		}
	}
	detail["targets"] = targets
	detail["pending_by_target"] = pendingByTarget
	if state.RunState == shortstory.RunWaitingAuthor {
		detail["waiting_author"] = true
	}
	writeJSON(w, http.StatusOK, detail)
}

// workbenchTargets 列出作品在 authoring 层的全部内容目标。
func workbenchTargets(auth *authoring.Store) []string {
	root := filepath.Join(auth.Dir(), "targets")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var targets []string
	for _, entry := range entries {
		if entry.IsDir() {
			targets = append(targets, entry.Name())
		}
	}
	sort.Strings(targets)
	return targets
}

func summarizeTarget(auth *authoring.Store, targetID string) (*targetSummary, error) {
	summary := &targetSummary{TargetID: targetID, RootVersionID: authoring.ComputeVersionID(targetID, "", "")}
	head, err := auth.Head(targetID)
	if err == nil {
		summary.VersionID = head.VersionID
		summary.ContentDigest = head.ContentDigest
		if state, err := auth.AuthorApprovalForHead(targetID); err == nil {
			summary.AuthorApproved = state.Approved
		}
		if requires, err := auth.RequiresReview(targetID); err == nil {
			summary.RequiresReview = requires
		}
		if stale, err := auth.StaleSourceReport(targetID); err == nil {
			summary.StaleSources = len(stale)
		}
	}
	if pending, err := listPendingCandidates(auth, targetID); err == nil {
		summary.PendingCandidates = len(pending)
	}
	return summary, nil
}

// listPendingCandidates 列出目标上未决的候选。
func listPendingCandidates(auth *authoring.Store, targetID string) ([]authoring.Candidate, error) {
	dir := filepath.Join(auth.Dir(), "targets", targetID, "candidates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pending []authoring.Candidate
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
		pending = append(pending, *candidate)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].CreatedAt < pending[j].CreatedAt })
	return pending, nil
}
