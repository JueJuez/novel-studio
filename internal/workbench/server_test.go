package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/authoring"
	"github.com/chenhongyang/novel-studio/internal/project"
	"github.com/chenhongyang/novel-studio/internal/shortstory"
)

func fixtureRunsRoot(t *testing.T) (string, string) {
	t.Helper()
	runsRoot := t.TempDir()
	shortRoot := filepath.Join(runsRoot, "雾城夜行")
	manifest, err := project.Create(shortRoot, project.CreateOptions{Title: "雾城夜行", PipelineKind: project.PipelineShort})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := shortstory.Create(shortRoot, manifest, shortstory.Brief{Idea: "夜航测试"}); err != nil {
		t.Fatal(err)
	}
	// 一个 legacy 长篇：意图端点必须拒绝。
	legacyRoot := filepath.Join(runsRoot, "旧长篇")
	legacyMeta := filepath.Join(legacyRoot, "output", "novel", "meta")
	if err := os.MkdirAll(legacyMeta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyMeta, "progress.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return runsRoot, shortRoot
}

func testServer(t *testing.T, runsRoot string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(New(runsRoot).Handler())
	t.Cleanup(server.Close)
	return server
}

func getJSON(t *testing.T, url string, status int) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("GET %s status=%d want %d", url, resp.StatusCode, status)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func postJSON(t *testing.T, url string, body any, status int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("POST %s status=%d want %d", url, resp.StatusCode, status)
	}
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestWorkbenchAPIFlow(t *testing.T) {
	runsRoot, shortRoot := fixtureRunsRoot(t)
	server := testServer(t, runsRoot)
	base := server.URL

	// 作品列表：短篇 + legacy 分类正确。
	list := getJSON(t, base+"/api/projects", http.StatusOK)
	projects := list["projects"].([]any)
	if len(projects) != 2 {
		t.Fatalf("作品数错误: %d", len(projects))
	}

	// 详情：短篇带状态。
	detail := getJSON(t, base+"/api/projects/雾城夜行", http.StatusOK)
	if detail["kind"] != "short" {
		t.Fatalf("kind 错误: %v", detail["kind"])
	}

	// legacy 详情无状态；意图端点 403。
	legacyDetail := getJSON(t, base+"/api/projects/旧长篇", http.StatusOK)
	if _, ok := legacyDetail["state"]; ok {
		t.Fatal("legacy 作品不应有短篇状态")
	}
	if resp, err := http.Post(base+"/api/projects/旧长篇/targets/structure/candidates", "application/json", strings.NewReader(`{"content":"x"}`)); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy 意图端点未被拒绝: %v %d", err, func() int {
			if resp == nil {
				return 0
			}
			return resp.StatusCode
		}())
	}

	// 意图流：手改候选 → 确认 → head 推进且作者确认生效。
	proposed := postJSON(t, base+"/api/projects/雾城夜行/targets/structure/candidates",
		map[string]string{"content": "{\"conflict\":\"测试\"}\n"}, http.StatusOK)
	candidateID := proposed["candidate_id"].(string)
	confirmed := postJSON(t, base+"/api/projects/雾城夜行/targets/structure/confirm",
		map[string]string{"candidate_id": candidateID, "rules_summary": "API 确认"}, http.StatusOK)
	if confirmed["version_id"] == "" {
		t.Fatalf("确认未产生版本: %v", confirmed)
	}
	detail = getJSON(t, base+"/api/projects/雾城夜行", http.StatusOK)
	targets := detail["targets"].([]any)
	found := false
	for _, item := range targets {
		summary := item.(map[string]any)
		if summary["target_id"] == "structure" {
			found = true
			if summary["author_approved"] != true {
				t.Fatalf("确认状态未生效: %v", summary)
			}
		}
	}
	if !found {
		t.Fatal("targets 未包含 structure")
	}

	// 拒绝意图：另一候选拒绝后正文不变。
	rejected := postJSON(t, base+"/api/projects/雾城夜行/targets/structure/candidates",
		map[string]string{"content": "{\"conflict\":\"备选\"}\n"}, http.StatusOK)
	postJSON(t, base+"/api/projects/雾城夜行/targets/structure/reject",
		map[string]string{"candidate_id": rejected["candidate_id"].(string), "reason": "不合适"}, http.StatusOK)
	detail = getJSON(t, base+"/api/projects/雾城夜行", http.StatusOK)
	for _, item := range detail["targets"].([]any) {
		summary := item.(map[string]any)
		if summary["target_id"] == "structure" && summary["pending_candidates"] != float64(0) {
			t.Fatalf("拒绝后仍有待决候选: %v", summary)
		}
	}

	// diff：根版本（空内容）到当前版本，应有差异。
	auth := authoring.Open(shortRoot)
	head, err := auth.Head("structure")
	if err != nil {
		t.Fatal(err)
	}
	rootVersionID := authoring.ComputeVersionID("structure", "", "")
	diff := getJSON(t, base+"/api/projects/雾城夜行/diff?target=structure&from="+rootVersionID+"&to="+head.VersionID, http.StatusOK)
	summary := diff["summary"].(map[string]any)
	if summary["unchanged"] != false {
		t.Fatalf("根到当前应有差异: %v", diff)
	}
}

func TestWorkbenchDiffFromRoot(t *testing.T) {
	runsRoot, shortRoot := fixtureRunsRoot(t)
	server := testServer(t, runsRoot)
	auth := authoring.Open(shortRoot)
	if err := shortstory.EnsureTarget(auth, "structure"); err != nil {
		t.Fatal(err)
	}
	candidate, err := shortstory.ProposeManual(auth, "structure", "内容A\n\n内容B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shortstory.ConfirmTarget(auth, "structure", candidate.CandidateID, "确认"); err != nil {
		t.Fatal(err)
	}
	root, _ := auth.Head("structure")
	// 根版本内容为空——diff from 根版本。
	body := getJSON(t, server.URL+"/api/projects/雾城夜行/diff?target=structure&from="+root.VersionID+"&to="+root.VersionID, http.StatusOK)
	summary := body["summary"].(map[string]any)
	if summary["unchanged"] != true {
		t.Fatalf("根版本自比应 unchanged: %v", body)
	}
}

func TestWorkbenchEventsStreamAndCancel(t *testing.T) {
	runsRoot, shortRoot := fixtureRunsRoot(t)
	server := testServer(t, runsRoot)

	// 事件：初始状态机 brief 一条 + 手改候选 pending 一条。
	auth := authoring.Open(shortRoot)
	if err := shortstory.EnsureTarget(auth, "structure"); err != nil {
		t.Fatal(err)
	}
	if _, err := shortstory.ProposeManual(auth, "structure", "待决内容"); err != nil {
		t.Fatal(err)
	}
	events := getJSON(t, server.URL+"/api/projects/雾城夜行/events", http.StatusOK)
	list := events["events"].([]any)
	if len(list) < 2 {
		t.Fatalf("事件数错误: %d", len(list))
	}

	// 取消意图：运行状态变 cancelled，不冒充正文阶段。
	cancelled := postJSON(t, server.URL+"/api/projects/雾城夜行/cancel", map[string]string{"reason": "作者要求"}, http.StatusOK)
	if cancelled["run_state"] != "cancelled" {
		t.Fatalf("取消未生效: %v", cancelled["run_state"])
	}
	state := cancelled["stage"].(string)
	if state == "cancelled" {
		t.Fatal("取消冒充了正文阶段")
	}

	// SSE：连接即收到 state 事件。
	resp, err := http.Get(server.URL + "/api/projects/雾城夜行/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE 响应异常: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	buf := make([]byte, 256)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf), "event: state") {
		t.Fatalf("首个事件不是 state: %s", string(buf))
	}
}

func TestWorkbenchRejectsPathTraversal(t *testing.T) {
	runsRoot, _ := fixtureRunsRoot(t)
	server := testServer(t, runsRoot)
	// 名称永远是发现列表内的键：路径穿越名找不到即 404，不拼接原始输入。
	resp, err := http.Get(server.URL + "/api/projects/..%2F..%2Fetc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("路径穿越未被拒绝: %d", resp.StatusCode)
	}
}

func TestWorkbenchStaticServing(t *testing.T) {
	runsRoot := t.TempDir()
	staticDir := filepath.Join(runsRoot, "dist")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<html>workbench</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := New(runsRoot)
	server.StaticDir = staticDir
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "workbench") {
		t.Fatalf("静态托管失败: %s", string(body))
	}
	// API 不受静态托管影响。
	if health := getJSON(t, ts.URL+"/api/health", http.StatusOK); health["ok"] != true {
		t.Fatal("health 检查失败")
	}
}
