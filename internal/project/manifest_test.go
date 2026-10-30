package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createTempRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestCreateManifestExplicitKindAndRoundtrip(t *testing.T) {
	root := createTempRoot(t)
	manifest, err := Create(root, CreateOptions{Title: "雾城夜行", PipelineKind: PipelineShort, Collaborative: true})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProjectID == "" || !strings.HasPrefix(manifest.ProjectID, "proj-") {
		t.Fatalf("project_id 未生成: %q", manifest.ProjectID)
	}
	if manifest.EngineProtocol != EngineProtocolShort {
		t.Fatalf("short 管线引擎协议=%q", manifest.EngineProtocol)
	}
	if manifest.Language != "zh" {
		t.Fatalf("默认语言=%q", manifest.Language)
	}

	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if *loaded != *manifest {
		t.Fatalf("roundtrip 不一致: %+v != %+v", *loaded, *manifest)
	}
	if loaded.CreatedAt == "" || loaded.UpdatedAt == "" {
		t.Fatal("时间戳缺失")
	}

	// 重复创建拒绝：manifest 不覆盖。
	if _, err := Create(root, CreateOptions{Title: "另一本", PipelineKind: PipelineLong}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复创建未被拒绝: %v", err)
	}
}

func TestCreateRequiresExplicitKind(t *testing.T) {
	root := createTempRoot(t)
	if _, err := Create(root, CreateOptions{Title: "无管线"}); err == nil || !strings.Contains(err.Error(), "显式") {
		t.Fatalf("空管线类型未被拒绝: %v", err)
	}
	if _, err := Create(root, CreateOptions{Title: "坏管线", PipelineKind: PipelineKind("medium")}); err == nil {
		t.Fatal("未知管线类型未被拒绝")
	}
	if _, err := Create(root, CreateOptions{Title: "", PipelineKind: PipelineShort}); err == nil {
		t.Fatal("空标题未被拒绝")
	}
}

func TestLoadRejectsUnknownSchemaAndUnknownFields(t *testing.T) {
	root := createTempRoot(t)
	if err := os.WriteFile(filepath.Join(root, ManifestFileName), []byte(`{"schema_version":"novel-studio-project.v2","pipeline_kind":"short"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "明确拒绝") {
		t.Fatalf("未知 schema_version 未被明确拒绝: %v", err)
	}

	root = createTempRoot(t)
	if err := os.WriteFile(filepath.Join(root, ManifestFileName), []byte(`{"schema_version":"novel-studio-project.v1","future_field":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("未知字段未被拒绝")
	}
}

func TestCreateRefusesLegacyWorkDir(t *testing.T) {
	root := createTempRoot(t)
	novelDir := filepath.Join(root, "output", "novel", "meta")
	if err := os.MkdirAll(novelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(novelDir, "progress.json"), []byte(`{"phase":"writing"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Create(root, CreateOptions{Title: "旧作", PipelineKind: PipelineLong})
	if err == nil || !strings.Contains(err.Error(), "不写回") {
		t.Fatalf("旧工程未被拒绝创建 manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ManifestFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧工程被写回了 manifest: %v", err)
	}
}

func TestDiscoverClassifiesManifestAndLegacy(t *testing.T) {
	root := createTempRoot(t)

	shortRoot := filepath.Join(root, "短篇作品")
	if _, err := Create(shortRoot, CreateOptions{Title: "短篇作品", PipelineKind: PipelineShort}); err != nil {
		t.Fatal(err)
	}
	longRoot := filepath.Join(root, "长篇旧作")
	novelMeta := filepath.Join(longRoot, "output", "novel", "meta")
	if err := os.MkdirAll(novelMeta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(novelMeta, "progress.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "普通目录"), 0o755); err != nil {
		t.Fatal(err)
	}

	projects, err := DiscoverRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("发现数量=%d， want 2（普通目录不算作品）: %+v", len(projects), projects)
	}
	byRoot := map[string]*Project{}
	for _, p := range projects {
		byRoot[p.Root] = p
	}
	short := byRoot[shortRoot]
	if short == nil || short.Legacy || short.Kind() != PipelineShort {
		t.Fatalf("short 作品分类错误: %+v", short)
	}
	if want := filepath.Join(shortRoot, ShortStateDirName); short.EngineDir != want {
		t.Fatalf("short EngineDir=%q want %q", short.EngineDir, want)
	}
	long := byRoot[longRoot]
	if long == nil || !long.Legacy || long.Manifest != nil || long.Kind() != "" {
		t.Fatalf("legacy 作品分类错误: %+v", long)
	}
	if want := filepath.Join(longRoot, "output", "novel"); long.EngineDir != want {
		t.Fatalf("legacy EngineDir=%q want %q", long.EngineDir, want)
	}

	// 单目录发现与批量口径一致。
	one, found, err := DiscoverRun(longRoot)
	if err != nil || !found || !one.Legacy {
		t.Fatalf("单目录 legacy 发现: found=%v err=%v", found, err)
	}
}

func TestDiscoverRejectsUnknownManifestFormatLoudly(t *testing.T) {
	root := createTempRoot(t)
	bad := filepath.Join(root, "坏格式")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"schema_version": "someone-else.v9"})
	if err := os.WriteFile(filepath.Join(bad, ManifestFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverRuns(root); err == nil || !strings.Contains(err.Error(), "明确拒绝") {
		t.Fatalf("未知格式未被批量发现暴露: %v", err)
	}
}
