# P5-03 浏览器完整流程与首次使用走查报告

Owner：ouyang。状态：生效。更新日期：2026-11-04（P5-02 同批产出）。

走查环境：WSL 侧隔离实例 `output/acceptance/p5-03-walkthrough/`（gitignored）——专用二进制（构建自 Windows HEAD 同源工作树）、scratch 数据目录 `data/runs/`（走查短篇=API 创建的 short、legacy-走查副本=旧格式副本，manifest 移除还原 legacy 态）、**中性化模型配置**（base_url=127.0.0.1:9 死端，任何意外管线启动立即连接失败，保证走查零真实云端调用）。workbench 端口 8796，Windows 浏览器经 WSL2 localhost 转发访问。截图证据：`output/acceptance/p5-03-walkthrough/*.png`。

## 1. 走查覆盖与结果（浏览器实测，非截图断言）

| 流程 | 结果 | 说明 |
|---|---|---|
| 首次使用·空列表 | 通过（有缺口） | 空列表渲染正常，但**零引导文案**（无「如何创建第一件作品」提示），见缺口 G1 |
| 创建作品 | API 通过 | `POST /api/projects`（显式 kind=short）创建成功并入列；**UI 无创建入口**（G2） |
| 列表→详情 | 通过 | short 显示阶段/运行态/事件；legacy 显示「legacy 只读」标识 |
| 手改→候选 | 通过 | 手改内容提交为 manual 候选（基线=当前 head，事件 candidate_pending 留痕） |
| 候选接受 | 通过 | 接受后 确认=✓、待决归零、author_approved 事件留痕；重复接受幂等 |
| 候选拒绝 | 通过 | 待决归零；但**拒绝决定无事件留痕**（待决派生事件随决定消失，G6） |
| 差异视图 | 修复后通过 | 见缺陷 D1/D2；修复后 ops 逐段渲染（insert 段落可见） |
| 双标签页旧页冲突 | 通过 | 两标签同载作品→A 接受→B（陈旧视图）再接受同一候选→服务端幂等：每个候选仅一条确认记录、版本链线性（54a0→3791→6e0f）、无重复推进、无损坏；B 无错误提示（幂等语义正确但 UI 未提示「已被另一页处理」，G7） |
| legacy 只读 | 通过（有缺口） | 只读语义正确；但详情**未内联渲染长篇章节投影**，仅提示文案（G3） |
| SSE 服务端契约 | 通过 | `GET /api/projects/{name}/stream`：连接即推 `event: state` + 15s `event: heartbeat`（实测抓包）；**UI 未消费 SSE**（G4） |
| 取消/限流/超时 | 未覆盖 | **UI 无任何取消入口**（advance/生成均无按钮）；限流/超时 UI 面不存在（G5） |
| 桌面 1280×800 | 修复后通过 | 见缺陷 D3 |
| 窄屏 390×844 | 通过 | 无横向溢出，单列堆叠可读（截图 narrow-detail.png） |

## 2. 走查发现并修复的缺陷（全部带回归证据）

1. **D1 发版 dist 陈旧（P4-01 遗留）**：入库的 `services/workbench/dist` 构建于源码修正之前——编译产物中「与初始版本比较」按钮的 from 参数被编译为字面量 `""`（bundle 内完全不存在 `root_version_id` 字段名），差异请求必然 500（`versions/.json: no such file or directory`）。修复＝从当前源码重建 dist（`npm run build`，产物 index-BORIP5b8.js），重建后差异视图正常。**流程教训：dist 属提交产物，源码变更后必须重构建再提交。**
2. **D2 DiffOp JSON 键契约不符**：`internal/authoring/diff.go` DiffOp{Kind,Text} 无 json tag，API 序列化为 Go 默认大写键名，前端读 `op.kind/op.text` 得 undefined → 差异段落渲染为空。修复＝加 `json:"kind"`/`json:"text"`（DiffOp 仅瞬态视图、无哈希/持久化消费者），authoring 包测试通过，浏览器复验 insert 段落渲染。
3. **D3 无颜色方案（视觉门禁）**：应用未声明任何 color/background——暗色偏好浏览器（prefers-color-scheme: dark）下正文黑底黑字不可读（desktop-detail.png 实证：仅按钮与边框可见）。修复＝App.svelte 显式亮色方案（`color-scheme: light` + body 背景/前景），重建后桌面与窄屏截图均清晰可读。

## 3. UI 缺口登记（本批不扩权实现，留待后续批次）

- G1 空列表无新手引导文案。
- G2 创建作品无 UI 入口（仅 API/CLI）。
- G3 legacy/长篇详情未内联渲染章节投影（仅指引文案；/long 投影数据 API 可用）。
- G4 UI 未消费 SSE（P4-02 服务端契约已验证可用；事件仅在打开详情时拉取一次，无断线重连校准——「断线后以服务端工件校准」目前靠手动刷新达成）。
- G5 UI 无取消入口（advance 取消意图端点无按钮）；限流/超时无 UI 面。
- G6 拒绝决定无事件留痕（作者审计线索弱）。
- G7 双标签幂等接受成功但未提示「已由另一页面处理」。

工程口径：以上缺口均为「有服务端契约、无前端面」，不阻塞 MVP 数据安全（写路径全部走授权入口且幂等）；P5-03 验证列要求的「取消/限流/超时/重连覆盖」在 UI 层的结论如实记录为未覆盖。

## 4. token/成本/耗时/返工汇总口径

- 汇总数据源：各作品 `meta/usage.json` + `meta/runtime/usage_audit.jsonl`（现有 TokenUsage/ResolveTokenCost 口径，Source=estimated）。
- 短篇三题材（P5-02 第一批）：52+ 次调用 ≈13.3 万输入/7.6 万输出 token ≈$0.027，含全部返工与重审（见样稿笔记 §3）。
- 长篇窗口累计：见样稿笔记 §17（本批结束时点）与 C9 运行日志；返工原因五层合同收敛记录于笔记 §6—§14。
- 浏览器走查本身零模型调用（中性化配置保证），不产生成本。
