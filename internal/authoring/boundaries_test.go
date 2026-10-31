package authoring

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSharedPackageBoundaryImports 是 P3-03 的依赖契约：共享包
// （authoring/project/shortstory）不得引用任一管线实现包。用 go/parser
// 静态检查全部 Go 源文件的 import，无需启动工具链。
func TestSharedPackageBoundaryImports(t *testing.T) {
	forbidden := []string{
		"github.com/chenhongyang/novel-studio/internal/host",
		"github.com/chenhongyang/novel-studio/internal/agents",
		"github.com/chenhongyang/novel-studio/internal/tools",
		"github.com/chenhongyang/novel-studio/internal/store",
		"github.com/chenhongyang/novel-studio/cmd",
		"github.com/chenhongyang/novel-studio/services",
	}
	roots := []string{"."}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				value := strings.Trim(imported.Path.Value, `"`)
				for _, bad := range forbidden {
					if value == bad || strings.HasPrefix(value, bad+"/") {
						t.Errorf("%s 引用了管线包 %s（共享包依赖边界被破坏）", path, value)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectLongPipelineReadOnly(t *testing.T) {
	base := t.TempDir()
	engineDir := filepath.Join(base, "output", "novel", "chapters")
	if err := os.MkdirAll(engineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chapter1 := "第一章正文。\n\n第二段。"
	chapter2 := "第二章正文。"
	if err := os.WriteFile(filepath.Join(engineDir, "01.md"), []byte(chapter1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engineDir, "02.md"), []byte(chapter2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engineDir, "notes.txt"), []byte("非章节文件"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 记录投影前的目录状态，验证只读。
	before := map[string]string{}
	if err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(path)
			before[path] = string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	view, err := ProjectLongPipeline(filepath.Join(base, "output", "novel"))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Chapters) != 2 {
		t.Fatalf("章节数错误: %d", len(view.Chapters))
	}
	if view.Chapters[0].Chapter != 1 || view.Chapters[1].Chapter != 2 {
		t.Fatalf("章号排序错误: %+v", view.Chapters)
	}
	if view.Chapters[0].Content != chapter1 || view.Chapters[1].ContentDigest == "" {
		t.Fatalf("章节内容/摘要缺失: %+v", view.Chapters[0])
	}
	if !view.Chapters[0].Canonical {
		t.Fatal("正史标记缺失")
	}
	// 投影后目录零变化（只读）。
	after := map[string]string{}
	if err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(path)
			after[path] = string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for path, content := range before {
		if after[path] != content {
			t.Fatalf("只读投影修改了文件: %s", path)
		}
	}
	if len(after) != len(before) {
		t.Fatalf("只读投影创建了新文件: before=%d after=%d", len(before), len(after))
	}
	// 损坏章节名 → 显式报错。
	if err := os.WriteFile(filepath.Join(engineDir, "abc.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectLongPipeline(filepath.Join(base, "output", "novel")); err == nil {
		t.Fatal("无法解析章号的文件未被拒绝")
	}
	// 无章节目录 → 空视图而非错误。
	empty, err := ProjectLongPipeline(filepath.Join(base, "不存在"))
	if err != nil || len(empty.Chapters) != 0 {
		t.Fatalf("缺失目录应返回空视图: %+v err=%v", empty, err)
	}
}
