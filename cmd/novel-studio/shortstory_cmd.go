package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/project"
	"github.com/chenhongyang/novel-studio/internal/shortstory"
)

// runShortStoryCommand 实现 `novel-studio short-story init|status|resume|generate|confirm|candidates|finalize`：
// 双管线的短篇命令入口。init 显式创建 short 管线作品（版本化 manifest
// + 短篇状态），status 列出 runs 根下全部作品与管线分类，resume 是恢复
// 入口（加载持久化状态并打印续作摘要），generate/confirm/candidates/finalize
// 是生成驱动（P5-02 使能项：runner 全部复用 internal/shortstory 库函数，
// 生成产物一律登记为候选，作者确认显式收口）。长篇入口不受影响，仍走原调度。
func runShortStoryCommand(argv []string) int {
	if len(argv) == 0 || hasHelpToken(argv) {
		printShortStoryUsage(os.Stdout)
		return 0
	}
	sub, rest := argv[0], argv[1:]
	switch sub {
	case "init":
		return runShortStoryInit(rest)
	case "status":
		return runShortStoryStatus(rest)
	case "resume":
		return runShortStoryResume(rest)
	case "generate":
		return runShortStoryGenerate(rest)
	case "confirm":
		return runShortStoryConfirm(rest)
	case "candidates":
		return runShortStoryCandidates(rest)
	case "reject":
		return runShortStoryReject(rest)
	case "waive":
		return runShortStoryWaive(rest)
	case "issues":
		return runShortStoryIssues(rest)
	case "finalize":
		return runShortStoryFinalize(rest)
	default:
		fmt.Fprintf(os.Stderr, "short-story: 未知子命令 %q\n\n", sub)
		printShortStoryUsage(os.Stderr)
		return 1
	}
}

func printShortStoryUsage(w *os.File) {
	fmt.Fprintln(w, "用法:")
	fmt.Fprintln(w, "  novel-studio short-story init --title <书名> --prompt \"<创意>\" [--language zh] [--collaborative] [--dir data/runs]")
	fmt.Fprintln(w, "  novel-studio short-story status [--dir data/runs]   # 列出作品与管线分类（short/long/legacy 只读）")
	fmt.Fprintln(w, "  novel-studio short-story resume --dir <作品根>       # 恢复短篇状态并打印续作摘要")
	fmt.Fprintln(w, "  novel-studio short-story generate --dir <作品根> --stage structure|scenes|draft|review [--scene N] [--budget-range \"[5000, 30000]\"] [--config p] [--timeout-seconds 900]")
	fmt.Fprintln(w, "  novel-studio short-story candidates --dir <作品根> [--target <t>]    # 列待决候选")
	fmt.Fprintln(w, "  novel-studio short-story reject --dir <作品根> --target <t> --candidate <id> --reason <理由>  # 拒绝候选（正文不变）")
	fmt.Fprintln(w, "  novel-studio short-story confirm --dir <作品根> --target <t> --candidate <id> [--note ...]  # 作者确认（Accept+确认绑定，推进状态机）")
	fmt.Fprintln(w, "  novel-studio short-story finalize --dir <作品根>     # 终审门禁 → 导出 Markdown/TXT+清单+备份")
}

func runShortStoryInit(argv []string) int {
	fs := flag.NewFlagSet("short-story init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	title := fs.String("title", "", "作品标题（同时作为 runs 下的目录名）")
	prompt := fs.String("prompt", "", "创意（brief）")
	language := fs.String("language", "zh", "作品语言")
	collaborative := fs.Bool("collaborative", false, "启用协作创作策略（增加作者确认边界）")
	runsRoot := fs.String("dir", filepath.Join("data", "runs"), "runs 根目录")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*title) == "" || strings.TrimSpace(*prompt) == "" {
		fmt.Fprintln(os.Stderr, "short-story init: --title 与 --prompt 必填")
		return 1
	}
	runName := sanitizeRunName(*title)
	if runName == "" {
		fmt.Fprintln(os.Stderr, "short-story init: 标题无法转换为目录名")
		return 1
	}
	runRoot := filepath.Join(*runsRoot, runName)
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "short-story init: %v\n", err)
		return 1
	}
	manifest, err := project.Create(runRoot, project.CreateOptions{
		Title:         strings.TrimSpace(*title),
		Language:      *language,
		PipelineKind:  project.PipelineShort,
		Collaborative: *collaborative,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story init: %v\n", err)
		return 1
	}
	store, state, err := shortstory.Create(runRoot, manifest, shortstory.Brief{
		Idea: strings.TrimSpace(*prompt),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story init: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "已创建短篇作品：%s\n", runRoot)
	fmt.Fprintf(os.Stdout, "  project_id=%s  pipeline=short  状态目录=%s\n", manifest.ProjectID, store.Dir())
	fmt.Fprintf(os.Stdout, "  阶段=%s  运行状态=%s\n", state.Stage, state.RunState)
	fmt.Fprintf(os.Stdout, "  下一步：确认故事结构后 novel-studio short-story resume --dir %s\n", runRoot)
	return 0
}

func runShortStoryStatus(argv []string) int {
	fs := flag.NewFlagSet("short-story status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runsRoot := fs.String("dir", filepath.Join("data", "runs"), "runs 根目录")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	projects, err := project.DiscoverRuns(*runsRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story status: %v\n", err)
		return 1
	}
	if len(projects) == 0 {
		fmt.Fprintf(os.Stdout, "在 %s 下未发现作品。\n", *runsRoot)
		return 0
	}
	fmt.Fprintf(os.Stdout, "作品列表（%s）：\n", *runsRoot)
	for _, p := range projects {
		name := filepath.Base(p.Root)
		switch {
		case p.Legacy:
			fmt.Fprintf(os.Stdout, "  %-16s  [legacy 只读]  引擎目录=%s（无 manifest，不写回、不重分管线）\n", name, p.EngineDir)
		case p.Kind() == project.PipelineShort:
			describeShortProject(p)
		case p.Kind() == project.PipelineLong:
			fmt.Fprintf(os.Stdout, "  %-16s  [long]  引擎目录=%s（原调度）\n", name, p.EngineDir)
		default:
			fmt.Fprintf(os.Stdout, "  %-16s  [未知管线]\n", name)
		}
	}
	return 0
}

func describeShortProject(p *project.Project) {
	name := filepath.Base(p.Root)
	store, err := shortstory.Open(p.Root)
	if err != nil {
		fmt.Fprintf(os.Stdout, "  %-16s  [short]  状态目录不可恢复: %v\n", name, err)
		return
	}
	state, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stdout, "  %-16s  [short]  恢复校验失败: %v\n", name, err)
		return
	}
	fmt.Fprintf(os.Stdout, "  %-16s  [short]  阶段=%s  运行=%s  历史=%d 条  project_id=%s\n",
		name, state.Stage, state.RunState, len(state.History), state.ProjectID)
}

func runShortStoryResume(argv []string) int {
	fs := flag.NewFlagSet("short-story resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runRoot := fs.String("dir", "", "作品根目录")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	if strings.TrimSpace(*runRoot) == "" {
		fmt.Fprintln(os.Stderr, "short-story resume: --dir 必填（作品根目录）")
		return 1
	}
	found, ok, err := project.DiscoverRun(*runRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story resume: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "short-story resume: %s 不是可识别的作品目录\n", *runRoot)
		return 1
	}
	if found.Legacy {
		fmt.Fprintf(os.Stderr, "short-story resume: %s 是旧作品（只读），不接入短篇管线\n", *runRoot)
		return 1
	}
	if found.Kind() != project.PipelineShort {
		fmt.Fprintf(os.Stderr, "short-story resume: %s 的管线是 %s，请走对应管线入口\n", *runRoot, found.Kind())
		return 1
	}
	store, err := shortstory.Open(*runRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story resume: %v\n", err)
		return 1
	}
	state, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "short-story resume: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "已恢复短篇状态：%s\n", store.Dir())
	fmt.Fprintf(os.Stdout, "  project_id=%s  阶段=%s  运行状态=%s\n", state.ProjectID, state.Stage, state.RunState)
	tail := len(state.History) - 4
	if tail < 0 {
		tail = 0
	}
	for _, record := range state.History[tail:] {
		fmt.Fprintf(os.Stdout, "  - %s  %s  %s\n", record.At, record.Stage, record.Artifact)
	}
	for _, event := range state.RunEvents {
		fmt.Fprintf(os.Stdout, "  · 运行事件 %s: %s→%s %s\n", event.At, event.From, event.To, event.Reason)
	}
	return 0
}

// sanitizeRunName 把标题转成安全的目录名：剥掉路径分隔符与保留字符。
func sanitizeRunName(title string) string {
	name := strings.TrimSpace(title)
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	name = replacer.Replace(name)
	name = strings.Trim(name, ". ")
	return name
}
