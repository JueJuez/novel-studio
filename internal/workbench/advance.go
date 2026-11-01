package workbench

// 长篇编排入口（P4-04）：把既有 pipeline 编排器接到工作台意图。
//
// 边界（对齐计划 §5/§6.3）：
//   - 前端只提交意图：本文件不实现任何编排逻辑，阶段执行委托给命令层
//     注入的 PipelineRunner（cmd 侧以同一应用命令入口运行既有 pipeline
//     阶段）；Runner 为 nil 时意图端点显式报告编排器未接入。
//   - 仅协作长篇可提交推进意图；legacy/短篇/非协作长篇一律 403。
//   - 阶段白名单只含 sealed 渲染协议的推进/收口阶段；改写/返工仍走
//     原引擎路径，不在本入口开放。
//   - 单作品同时只允许一个推进意图（409）。运行状态是辅助信息（内存
//     持有，重启即失）；权威状态始终是引擎持久工件——事件/详情接口从
//     工件派生，刷新/重启不丢等待与候选。
//   - 等待作者确认的识别：PipelineRunner 返回的输出按固定消息片段解析
//     出门禁目标与版本（消息格式由 cmd/novel-studio/pipeline_author_gate.go
//     产出，两侧注释互相锚定；cmd 侧测试与本项目测试各持一份相同
//     pattern 双向钉住契约）。

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chenhongyang/novel-studio/internal/project"
)

// longGateWaitPattern 从编排器输出中识别「等待作者确认」并提取门禁
// 目标/版本。必须与 cmd 层 requireLongAuthorGate 的错误消息格式一致。
var longGateWaitPattern = regexp.MustCompile(`目标 (\S+) 版本 (\S+) 尚无作者确认`)

// advanceStagesAllowed 是推进意图的阶段白名单：promote,render 即单章
// 推进（promote 机械提升下一章计划，render 在冻结计划上完成单章渲染
// 事务）；complete-book/deliver 是收口与交付。
var advanceStagesAllowed = map[string]bool{
	"seal":          true,
	"promote":       true,
	"render":        true,
	"complete-book": true,
	"deliver":       true,
}

// AdvanceStatus 是一次推进意图的辅助运行状态。
type AdvanceStatus struct {
	Stages        string `json:"stages"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
	Running       bool   `json:"running"`
	WaitingAuthor bool   `json:"waiting_author"`
	GateTarget    string `json:"gate_target,omitempty"`
	GateVersion   string `json:"gate_version,omitempty"`
	OutputTail    string `json:"output_tail,omitempty"`
	Error         string `json:"error,omitempty"`
}

// advanceOutputTailLimit 限制回传给前端的编排器输出尾部大小。
const advanceOutputTailLimit = 4096

func (s *Server) advanceStatusFor(name string) *AdvanceStatus {
	s.advanceMu.Lock()
	defer s.advanceMu.Unlock()
	status, ok := s.advanceStatus[name]
	if !ok {
		return nil
	}
	snapshot := *status
	return &snapshot
}

func (s *Server) setAdvanceStatus(name string, status *AdvanceStatus) {
	s.advanceMu.Lock()
	defer s.advanceMu.Unlock()
	s.advanceStatus[name] = status
}

// handleAdvance 处理 POST（提交单章推进/收口意图）与 GET（查询最近一
// 次意图的运行状态）。
func (s *Server) handleAdvance(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusForbidden, fmt.Errorf("推进意图仅对协作长篇开放（kind=%q legacy=%v）", p.Kind(), p.Legacy))
		return
	}
	if r.Method == http.MethodGet {
		status := s.advanceStatusFor(p.Root)
		if status == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("尚无推进意图"))
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	var body struct {
		Stages string `json:"stages"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	stages := body.Stages
	if stages == "" {
		stages = "promote,render"
	}
	for _, stage := range splitAdvanceStages(stages) {
		if !advanceStagesAllowed[stage] {
			writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("阶段 %q 不在推进白名单内（允许：%v）", stage, allowedAdvanceStageNames()))
			return
		}
	}
	if s.PipelineRunner == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("编排器未接入：命令层未注入 PipelineRunner"))
		return
	}
	if existing := s.advanceStatusFor(p.Root); existing != nil && existing.Running {
		writeError(w, http.StatusConflict, fmt.Errorf("作品已有推进意图在运行（stages=%s，started_at=%s）", existing.Stages, existing.StartedAt))
		return
	}
	status := &AdvanceStatus{
		Stages:    stages,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Running:   true,
	}
	s.setAdvanceStatus(p.Root, status)
	go s.runAdvanceIntent(p.Root, stages, status)
	writeJSON(w, http.StatusAccepted, status)
}

// runAdvanceIntent 在后台执行推进意图并落最终状态。引擎自身的执行锁/
// 租约保证跨进程竞争下的正确性；本状态只服务前端展示。
func (s *Server) runAdvanceIntent(projectRoot, stages string, status *AdvanceStatus) {
	output, err := s.PipelineRunner(projectRoot, []string{"--stages", stages})
	s.advanceMu.Lock()
	status.Running = false
	status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if len(output) > advanceOutputTailLimit {
		output = output[len(output)-advanceOutputTailLimit:]
	}
	status.OutputTail = output
	if err != nil {
		status.Error = err.Error()
		if match := longGateWaitPattern.FindStringSubmatch(output + "\n" + err.Error()); match != nil {
			status.WaitingAuthor = true
			status.GateTarget = match[1]
			status.GateVersion = match[2]
		}
	}
	s.advanceMu.Unlock()
}

func splitAdvanceStages(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func allowedAdvanceStageNames() []string {
	names := make([]string, 0, len(advanceStagesAllowed))
	for name := range advanceStagesAllowed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
