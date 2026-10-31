package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/chenhongyang/novel-studio/internal/workbench"
)

// runWorkbenchCommand 实现 `novel-studio workbench`：统一写作工作台的
// Web API。默认只绑定回环地址；写路径全部经过 authoring/shortstory
// 授权入口（前端只提交意图）；原 Dashboard 保留为生产监控，本命令是
// 两条管线的统一操作入口。
func runWorkbenchCommand(argv []string) int {
	if len(argv) > 0 && (argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help") {
		printWorkbenchUsage(os.Stdout)
		return 0
	}
	fs := flag.NewFlagSet("workbench", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runsRoot := fs.String("dir", filepath.Join("data", "runs"), "runs 根目录")
	host := fs.String("host", "127.0.0.1", "监听地址（默认仅回环）")
	port := fs.Int("port", 8790, "监听端口")
	if err := fs.Parse(argv); err != nil {
		return 1
	}
	addr := fmt.Sprintf("%s:%d", *host, *port)
	server := workbench.New(*runsRoot)
	// 前端构建产物存在则一并托管（services/workbench/dist）。
	if _, err := os.Stat(filepath.Join("services", "workbench", "dist", "index.html")); err == nil {
		server.StaticDir = filepath.Join("services", "workbench", "dist")
		fmt.Fprintln(os.Stdout, "已托管工作台前端：http://"+addr+"/")
	}
	fmt.Fprintf(os.Stdout, "工作台 API：http://%s/api/projects（runs=%s）\n", addr, *runsRoot)
	fmt.Fprintln(os.Stdout, "Ctrl-C 退出。前端只提交意图，写路径走授权入口；legacy/long 作品只读。")
	if err := http.ListenAndServe(addr, server.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "workbench: %v\n", err)
		return 1
	}
	return 0
}

func printWorkbenchUsage(w *os.File) {
	fmt.Fprintln(w, "用法:")
	fmt.Fprintln(w, "  novel-studio workbench [--dir data/runs] [--host 127.0.0.1] [--port 8790]")
	fmt.Fprintln(w, "接口:")
	fmt.Fprintln(w, "  GET  /api/health                                   # 健康检查")
	fmt.Fprintln(w, "  GET  /api/projects                                # 作品列表（short/long/legacy 分类）")
	fmt.Fprintln(w, "  GET  /api/projects/{name}                          # 详情（状态/目标/确认/陈旧）")
	fmt.Fprintln(w, "  GET  /api/projects/{name}/events                   # 持久工件派生事件")
	fmt.Fprintln(w, "  GET  /api/projects/{name}/stream                   # SSE（断线后用全量接口校准）")
	fmt.Fprintln(w, "  GET  /api/projects/{name}/diff?target&from&to      # 段落级差异")
	fmt.Fprintln(w, "  GET  /api/projects/{name}/long                     # 长篇只读投影")
	fmt.Fprintln(w, "  POST /api/projects/{name}/targets/{t}/candidates   # 手改意图 {content}")
	fmt.Fprintln(w, "  POST /api/projects/{name}/targets/{t}/confirm      # 确认意图 {candidate_id,rules_summary}")
	fmt.Fprintln(w, "  POST /api/projects/{name}/targets/{t}/reject       # 拒绝意图 {candidate_id,reason}")
	fmt.Fprintln(w, "  POST /api/projects/{name}/cancel                   # 取消意图 {reason}")
}
