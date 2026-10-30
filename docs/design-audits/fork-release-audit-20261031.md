# Fork 发行链核对（P0-04）

更新日期：2026-10-31。状态：核对完成；本任务只登记与定方案，不改发行配置（按计划，正式发布前逐项处理）。

背景：`origin` 已指向 `git@github.com:JueJuez/novel-studio.git` 且 push/fetch 正常（P0-01 验证），但安装器、Release、镜像与自更新仍指向上游 `Xiaoyangy/novel-studio`。核对范围为 CI、发布配置、安装器、镜像与帮助链接；`README-20260714.md` 为历史快照（0 处上游引用），保留原样不入清单。

| # | 位置 | 现状 | 影响 | 处理方案 | 时机 |
|---|---|---|---|---|---|
| 1 | [.goreleaser.yml:68-70](../../.goreleaser.yml) | `release.github.owner: Xiaoyangy` | 打 tag 会向上游仓库发 Release（失败或误发）——**首次发布阻断项** | 改为 `owner: JueJuez, name: novel-studio` | 首次 fork release 前（P5-04） |
| 2 | [.github/workflows/release.yml:47-48](../../.github/workflows/release.yml) | 镜像 tag `ghcr.io/xiaoyangy/novel-studio` | 推送到上游命名空间（失败）——**首次发布阻断项** | 改为 `ghcr.io/juejuez/novel-studio`（workflow 已有 `packages:write`，GITHUB_TOKEN 可推自己命名空间） | 同上 |
| 3 | release.yml 触发条件 | 仅 `push: tags: v*` | fork 日常 push 不会意外发布 | 先完成 #1/#2 再打 tag；无需改动 | — |
| 4 | [cmd/novel-studio/main.go:377](../../cmd/novel-studio/main.go) | `runSelfUpdate` 的 `Repo: "Xiaoyangy/novel-studio"` | **行为级风险**：fork 二进制执行 `novel-studio update` 会下载并安装上游 Release 覆盖自身 | 改为 JueJuez；或在 fork 无 Release 期间禁用并提示按源码运行（README 已有提示横幅） | P5-04 必改 |
| 5 | [scripts/install.sh:4,5,11](../../scripts/install.sh) | `REPO="Xiaoyangy/novel-studio"` 及注释 URL | 一键安装装到上游版本；fork 无 Release 前切换会导致 404 | fork 首个 Release 发布后切换 REPO 与文档内 curl URL | 首个 Release 后 |
| 6 | [docker-compose.yml:11](../../docker-compose.yml) | `image: ghcr.io/xiaoyangy/novel-studio:latest` | 拉起的是上游镜像，与 fork 代码不一致 | 首个镜像发布后改；此前本地 `docker compose build` | 首个 Release 后 |
| 7 | README.md :11-16 徽章、:417/:466/:474 Issue/Star/PR 链接；README_EN.md 对应行 | 指向上游（荣誉、反馈、贡献归上游） | 低 | fork 横幅已加（d247387）；发行时随 P5-04 统一切换 | P5-04 |
| 8 | README-TECHNICAL.md | 5 处上游引用（文档/仓库链接） | 低 | P5-04 逐条处理 | P5-04 |
| 9 | [.github/workflows/ci.yml](../../.github/workflows/ci.yml) | 无上游引用；fork 每次 push 自动生效（P0-01 以来已在跑） | ✓ | 无需处理 | — |
| 10 | [Dockerfile](../../Dockerfile) | `golang:1.27.1-alpine3.24` 与 CI 同版；`GOPROXY` build-arg 默认 proxy.golang.org（GH runner 可用；本地/WSL 构建需 goproxy.cn，见 P0-01 记录） | ✓（附注） | 无需处理 | — |
| 11 | [go.mod](../../go.mod) | module 路径 `github.com/chenhongyang/novel-studio`（与 fork remote 和上游均不同） | 编译/导入无影响；仅发行元数据口径 | 按计划 §9 保留，不阻塞；如需重命名须全仓 import 同步 + tag 大版本，单列决策 | P5-04 决策项 |
| 12 | docs/*.md 其余帮助链接 | 未发现其他上游仓库引用（唯一例外是计划文档中对上游的有意引用） | — | 无 | — |

## 结论

- **首次 fork 发布的最小必改集 = #1、#2、#4**（.goreleaser owner、ghcr 命名空间、自更新 Repo），全部已登记到 P5-04 的处理范围。
- 装完首个 Release 后切换 #5、#6；文档徽章与链接随 P5-04 统一处理。
- CI 与 Dockerfile 与 CI/发布口径一致，无需改动；P0-01 已验证 fork CI 实际生效。
