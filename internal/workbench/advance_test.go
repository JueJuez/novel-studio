package workbench

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhongyang/novel-studio/internal/project"
)

// applyTestServer 用 httptest 托管给定的 Server 实例（允许测试注入
// PipelineRunner 后再托管）。
func applyTestServer(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// setupCollabLong 在 runs 根下创建协作长篇作品目录。
func setupCollabLong(t *testing.T, runsRoot, name string, collaborative bool) string {
	t.Helper()
	root := filepath.Join(runsRoot, name)
	if _, err := project.Create(root, project.CreateOptions{
		Title:         name,
		PipelineKind:  project.PipelineLong,
		Collaborative: collaborative,
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

// setupLegacyLong 创建标准 output/novel 布局的 legacy 长篇；progressRaw
// 非空时写入 meta/progress.json（如 {"pending_rewrites":[...]}）。
func setupLegacyLong(t *testing.T, runsRoot, name, progressRaw string) string {
	t.Helper()
	root := filepath.Join(runsRoot, name)
	meta := filepath.Join(root, "output", "novel", "meta")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	if progressRaw != "" {
		if err := os.WriteFile(filepath.Join(meta, "progress.json"), []byte(progressRaw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestWorkbenchAdvanceIntentGuards(t *testing.T) {
	runsRoot, _ := fixtureRunsRoot(t)
	setupCollabLong(t, runsRoot, "协作长篇", true)
	setupCollabLong(t, runsRoot, "独立长篇", false)
	setupLegacyLong(t, runsRoot, "标准旧长篇", `{}`)
	server := New(runsRoot)
	ts := applyTestServer(t, server)
	base := ts.URL

	// 未运行过任何意图 → 404。
	getJSON(t, base+"/api/projects/协作长篇/advance", http.StatusNotFound)

	// legacy / 短篇 / 非协作长篇 → 403。
	postJSON(t, base+"/api/projects/标准旧长篇/advance", map[string]string{}, http.StatusForbidden)
	postJSON(t, base+"/api/projects/雾城夜行/advance", map[string]string{}, http.StatusForbidden)
	postJSON(t, base+"/api/projects/独立长篇/advance", map[string]string{}, http.StatusForbidden)

	// 白名单外阶段 → 422。
	postJSON(t, base+"/api/projects/协作长篇/advance", map[string]string{"stages": "render,rewrite"}, http.StatusUnprocessableEntity)

	// Runner 未注入 → 501。
	postJSON(t, base+"/api/projects/协作长篇/advance", map[string]string{}, http.StatusNotImplemented)
}

func TestWorkbenchAdvanceIntentRunsAndReports(t *testing.T) {
	runsRoot, _ := fixtureRunsRoot(t)
	collabRoot := setupCollabLong(t, runsRoot, "协作长篇", true)
	server := New(runsRoot)

	// 受控假编排器：记录调用并阻塞，用于验证单飞与状态迁移。
	release := make(chan struct{})
	var gotRoot string
	var gotArgs []string
	calls := 0
	server.PipelineRunner = func(projectRoot string, args []string) (string, error) {
		calls++
		gotRoot = projectRoot
		gotArgs = args
		<-release
		return "stage ok", nil
	}
	ts := applyTestServer(t, server)
	base := ts.URL

	accepted := postJSON(t, base+"/api/projects/协作长篇/advance", map[string]string{}, http.StatusAccepted)
	if accepted["running"] != true || accepted["stages"] != "promote,render" {
		t.Fatalf("推进意图未按默认单章推进受理: %v", accepted)
	}
	// 运行中重复提交 → 409。
	postJSON(t, base+"/api/projects/协作长篇/advance", map[string]string{}, http.StatusConflict)
	// 运行中状态可查询。
	running := getJSON(t, base+"/api/projects/协作长篇/advance", http.StatusOK)
	if running["running"] != true {
		t.Fatalf("运行中状态丢失: %v", running)
	}

	close(release)
	waitForAdvanceFinished(t, base)
	status := getJSON(t, base+"/api/projects/协作长篇/advance", http.StatusOK)
	if status["running"] != false || status["error"] != nil {
		t.Fatalf("结束后状态不符: %v", status)
	}
	if status["output_tail"] != "stage ok" {
		t.Fatalf("输出尾部未回传: %v", status)
	}
	// 编排器收到的是作品根 + 默认阶段参数。
	if gotRoot != collabRoot {
		t.Fatalf("runner 项目根错误: %q", gotRoot)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "--stages" || gotArgs[1] != "promote,render" {
		t.Fatalf("runner 参数错误: %v", gotArgs)
	}
	if calls != 1 {
		t.Fatalf("单飞失败，runner 被调用 %d 次", calls)
	}

	// 等待作者确认：编排器输出含固定消息片段 → 状态标记并提取门禁键。
	server.PipelineRunner = func(string, []string) (string, error) {
		msg := "pipeline: 长篇协作边界：等待作者确认: 目标 long-seal-gen-1 版本 sha256-plan 尚无作者确认；确认落盘后重跑即可继续，等待期间不占执行锁"
		return msg, errors.New(msg)
	}
	postJSON(t, base+"/api/projects/协作长篇/advance", map[string]string{"stages": "seal"}, http.StatusAccepted)
	waitForAdvanceFinished(t, base)
	status = getJSON(t, base+"/api/projects/协作长篇/advance", http.StatusOK)
	if status["waiting_author"] != true {
		t.Fatalf("未识别等待作者状态: %v", status)
	}
	if status["gate_target"] != "long-seal-gen-1" || status["gate_version"] != "sha256-plan" {
		t.Fatalf("门禁键提取错误: %v", status)
	}
}

// waitForAdvanceFinished 轮询直到最近一次推进意图结束（测试辅助）。
func waitForAdvanceFinished(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := getJSON(t, base+"/api/projects/协作长篇/advance", http.StatusOK)
		if status["running"] == false {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("推进意图未在期限内结束: %v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkbenchAdoptLegacyProject(t *testing.T) {
	runsRoot, _ := fixtureRunsRoot(t)
	legacyRoot := setupLegacyLong(t, runsRoot, "标准旧长篇", `{}`)
	setupLegacyLong(t, runsRoot, "返工中旧长篇", `{"pending_rewrites":[3]}`)
	// 非标准布局：引擎在 <root>/novel。
	nonStandard := filepath.Join(runsRoot, "非标准旧长篇")
	if err := os.MkdirAll(filepath.Join(nonStandard, "novel", "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonStandard, "novel", "meta", "progress.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	server := New(runsRoot)
	ts := applyTestServer(t, server)
	base := ts.URL

	// 空标题 → 422。
	postJSON(t, base+"/api/projects/标准旧长篇/adopt", map[string]string{"title": ""}, http.StatusUnprocessableEntity)

	// 返工中 → 空闲边界拒绝。
	postJSON(t, base+"/api/projects/返工中旧长篇/adopt", map[string]string{"title": "x"}, http.StatusConflict)

	// 非标准布局 → 拒绝接入（保持只读）。
	postJSON(t, base+"/api/projects/非标准旧长篇/adopt", map[string]string{"title": "x"}, http.StatusUnprocessableEntity)

	// 标准布局 legacy → 接入成功，固定 long+collaborative。
	manifest := postJSON(t, base+"/api/projects/标准旧长篇/adopt",
		map[string]string{"title": "接入的旧长篇"}, http.StatusOK)
	if manifest["pipeline_kind"] != "long" || manifest["collaborative"] != true {
		t.Fatalf("接入结果不符: %v", manifest)
	}
	// 接入后发现视图不再是 legacy，重复 adopt → 409。
	postJSON(t, base+"/api/projects/标准旧长篇/adopt", map[string]string{"title": "again"}, http.StatusConflict)
	p, found, err := project.DiscoverRun(legacyRoot)
	if err != nil || !found || p.Legacy || p.Manifest == nil {
		t.Fatalf("接入后发现状态错误: found=%v legacy=%v err=%v", found, p.Legacy, err)
	}
}

func TestWorkbenchAdoptRejectsActiveLease(t *testing.T) {
	runsRoot, _ := fixtureRunsRoot(t)
	legacyRoot := setupLegacyLong(t, runsRoot, "租约中旧长篇", `{}`)
	// 伪造当前进程持有的活动执行租约（InspectPipelineExecution 判活）。
	lockDir := filepath.Join(legacyRoot, "output", "novel", "meta", "runtime")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := fmt.Sprintf(`{"version":1,"mode":"promote","target_chapter":1,"owner":"adopt-test","process_id":%d,"acquired_at":%q,"expires_at":%q}`,
		os.Getpid(), time.Now().UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(lockDir, "pipeline_execution.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	server := New(runsRoot)
	ts := applyTestServer(t, server)
	postJSON(t, ts.URL+"/api/projects/租约中旧长篇/adopt", map[string]string{"title": "x"}, http.StatusConflict)
	// 接入被拒后目录仍无 manifest。
	if _, err := os.Stat(filepath.Join(legacyRoot, project.ManifestFileName)); !os.IsNotExist(err) {
		t.Fatalf("拒绝接入后不应写 manifest: %v", err)
	}
}
