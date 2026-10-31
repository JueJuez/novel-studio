package workbench

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
	"github.com/chenhongyang/novel-studio/internal/shortstory"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体解析失败: %w", err))
		return false
	}
	return true
}

// handleCreateProject 新建作品（P4-04 统一入口）：显式选择管线，
// 不接受在带旧作品特征的目录上创建（project.Create 拒绝并透传）。
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		Title         string `json:"title"`
		Kind          string `json:"kind"`
		Language      string `json:"language"`
		Collaborative bool   `json:"collaborative"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = body.Title
	}
	if body.Name == "" || filepath.Base(body.Name) != body.Name || body.Name == "." || body.Name == ".." {
		writeError(w, http.StatusBadRequest, fmt.Errorf("作品名不能为空且不能含路径分隔符"))
		return
	}
	kind := project.PipelineKind(body.Kind)
	if kind != project.PipelineLong && kind != project.PipelineShort {
		writeError(w, http.StatusBadRequest, fmt.Errorf("kind 必须显式为 short 或 long"))
		return
	}
	runRoot := filepath.Join(s.RunsRoot, body.Name)
	manifest, err := project.Create(runRoot, project.CreateOptions{
		Title:         body.Title,
		Language:      body.Language,
		PipelineKind:  kind,
		Collaborative: body.Collaborative,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if kind == project.PipelineShort {
		if _, _, err := shortstory.Create(runRoot, manifest, shortstory.Brief{Idea: "(待补充创意)"}); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, manifest)
}

// handleAuthorGate 记录协作长篇的作者确认（P4-03/P4-04）：确认对象是
// 确切的目标与版本/事务标识；只有协作长篇 manifest 接受该意图。
func (s *Server) handleAuthorGate(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	if p.Legacy || p.Manifest == nil || p.Manifest.PipelineKind != project.PipelineLong || !p.Manifest.Collaborative {
		writeError(w, http.StatusForbidden, fmt.Errorf("作者确认门仅对协作长篇开放（kind=%q legacy=%v）", p.Kind(), p.Legacy))
		return
	}
	var body struct {
		TargetID  string `json:"target_id"`
		VersionID string `json:"version_id"`
		Note      string `json:"note"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	auth := authoring.Open(p.Root)
	approval, err := auth.RecordExternalAuthorApproval(body.TargetID, body.VersionID, body.Note)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

// handleProposeManual 手改意图：内容经 ProposeManual 登记为候选。
func (s *Server) handleProposeManual(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	_, auth, err := s.shortStores(p)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("content 不能为空"))
		return
	}
	targetID := r.PathValue("target")
	if err := shortstory.EnsureTarget(auth, targetID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	candidate, err := shortstory.ProposeManual(auth, targetID, body.Content)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, candidate)
}

// handleConfirm 确认意图：接受候选并落作者确认。
func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	_, auth, err := s.shortStores(p)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var body struct {
		CandidateID  string `json:"candidate_id"`
		RulesSummary string `json:"rules_summary"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	version, err := shortstory.ConfirmTarget(auth, r.PathValue("target"), body.CandidateID, body.RulesSummary)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, version)
}

// handleReject 拒绝意图：不改变当前正文。
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	_, auth, err := s.shortStores(p)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var body struct {
		CandidateID string `json:"candidate_id"`
		Reason      string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := auth.Reject(r.PathValue("target"), body.CandidateID, body.Reason); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// handleCancel 取消意图：运行状态置为 cancelled（不冒充正文阶段）。
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	store, _, err := s.shortStores(p)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	state, err := store.SetRunState(shortstory.RunCancelled, body.Reason)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleDiff 两版本间的段落级差异（P3-02 合同复用）。
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	_, auth, err := s.shortStores(p)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	target := r.URL.Query().Get("target")
	fromVersion := r.URL.Query().Get("from")
	toVersion := r.URL.Query().Get("to")
	from, err := auth.LoadVersion(target, fromVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	to, err := auth.LoadVersion(target, toVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := authoring.DiffParagraphs(from.Content, to.Content)
	writeJSON(w, http.StatusOK, map[string]any{
		"target":  target,
		"from":    from.VersionID,
		"to":      to.VersionID,
		"ops":     ops,
		"summary": authoring.SummarizeDiff(ops),
	})
}

// handleLongView 长篇/legacy 作品的只读投影。
func (s *Server) handleLongView(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	view, err := authoring.ProjectLongPipeline(p.EngineDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":      filepath.Base(p.Root),
		"kind":      string(p.Kind()),
		"legacy":    p.Legacy,
		"read_only": true,
		"view":      view,
	})
}

// WorkEvent 是从持久工件派生的事件（服务端工件校准的口径）。
type WorkEvent struct {
	At      string `json:"at"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// handleEvents 从状态机历史、运行事件与候选/确认记录派生事件列表。
// 不含模型内部推理——summary 只描述工件事实。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	events := []WorkEvent{}
	if !p.Legacy && p.Kind() == "short" {
		if store, err := shortstory.Open(p.Root); err == nil {
			if state, err := store.Load(); err == nil {
				for _, record := range state.History {
					events = append(events, WorkEvent{At: record.At, Type: "stage_" + string(record.Stage), Summary: "阶段推进到 " + string(record.Stage)})
				}
				for _, runEvent := range state.RunEvents {
					events = append(events, WorkEvent{At: runEvent.At, Type: "run_" + string(runEvent.To), Summary: strings.TrimSpace("运行状态 " + string(runEvent.From) + "→" + string(runEvent.To) + " " + runEvent.Reason)})
				}
			}
		}
		if auth := authoring.Open(p.Root); auth != nil {
			for _, targetID := range workbenchTargets(auth) {
				if pending, err := listPendingCandidates(auth, targetID); err == nil {
					for _, candidate := range pending {
						events = append(events, WorkEvent{At: candidate.CreatedAt, Type: "candidate_pending", Summary: fmt.Sprintf("目标 %s 有待决候选（%s）", targetID, candidate.Kind)})
					}
				}
				if state, err := auth.AuthorApprovalForHead(targetID); err == nil && state.Approved {
					events = append(events, WorkEvent{At: state.Record.CreatedAt, Type: "author_approved", Summary: fmt.Sprintf("目标 %s 已获作者确认", targetID)})
				}
			}
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].At < events[j].At })
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleStream SSE：连接即推一次当前状态摘要；此后每 15s 心跳。断线
// 的客户端用 /api/projects/{name} 全量校准（服务端工件是唯一真源）。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	p, found, err := s.projectRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("作品不存在"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("流式响应不支持"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	emit := func(eventType, payload string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
		flusher.Flush()
	}
	emit("state", fmt.Sprintf(`{"project":%q,"at":%q}`, filepath.Base(p.Root), time.Now().UTC().Format(time.RFC3339)))
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			emit("heartbeat", time.Now().UTC().Format(time.RFC3339))
		}
	}
}
