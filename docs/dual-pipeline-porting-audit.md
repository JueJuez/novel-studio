# 双管线移植审计（P0-02）

更新日期：2026-10-31。状态：已完成首轮源码审计；实际引入任一项时须更新本文件并在 `THIRD_PARTY_NOTICES.md` 落声明。

审计对象与方法：本仓库内克隆两个外部仓库的完整源码（位于 `refer/`，已 gitignore，不随本仓库分发），按计划第 3 节要求的字段逐项记录。本轮为静态源码审计 + 公开许可文本核对，未运行外部应用；下表"验收案例"为引入时必须通过的用例，不是已通过记录。

| 固定项 | 值 |
|---|---|
| Show Me The Story（SMTS） | https://github.com/Nigh/show-me-the-story @ `f790187373f23dbaaffa0838197e972a394960d3`（2026-10-04，main HEAD），克隆于 `refer/show-me-the-story` |
| Pagekin（AI-Novel-Writer） | https://github.com/EthanYoQ/AI-Novel-Writer @ `59153ceb1174b20339e205b801f4e694e5ad5507`（2026-10-29，master HEAD），克隆于 `refer/pagekin-ai-novel-writer` |
| SMTS 许可证 | MIT（`LICENSE`，Copyright (c) 2026 xianii）：可复制与改写，须保留版权与许可文本 |
| Pagekin 许可证 | GPL v3（`LICENSE`）：按计划第 3 节决定**不复制任何代码与资产**，仅做行为级交互参考；将来若改为复制，须先单独调整许可与分发方案 |
| SMTS 语言/结构 | Go 1.25.1 + `frontend/`（Vite）；`internal/{agent,config,httpapi,i18n,llm,prose,sse,story}` |
| Pagekin 技术栈 | Electron + React 19 + TypeScript（pnpm）；数据层 better-sqlite3、@lancedb/lancedb——均不引入 |

## SMTS 移植候选记录

### R1 块级局部编辑与按块校订合同

- **源路径**：`internal/httpapi/web.go:71-99`（`PUT /api/chapters/{num}/blocks/{id}`、`DELETE .../blocks/{id}`、`POST .../blocks`、`POST .../blocks/{id}/revise`；proofread 组 `GET/POST/PUT/DELETE /api/proofread*`）；状态与逻辑在 `internal/story/writing*.go`、`internal/story/proofread.go`。
- **审计结论**：修改以"章 → 块 ID"寻址，按块 revise 单独成端点，与整章 `revise/polish/edit` 并存；proofread 侧 `ProofreadIssue.Status ∈ {pending, resolved, ignored}`（proofread.go:31,133,148），重新分析时保留 resolved/ignored 状态，并配 backup/apply/undo 全套端点（web.go:91-95）。
- **移植位置（规划）**：块寻址与按块 revise 的 API 合同 → `internal/workbench/`；场景/块 revise 逻辑 → `internal/shortstory/`。
- **保留**：块 ID 寻址形状；"局部 revise 与整章 revise 分离"；issue 状态机及重分析保状态。
- **改写**：不引入其项目状态、存储与模型客户端；块 ID 必须绑定本仓库 P1-03 的版本/哈希/作者确认合同；本仓库已有限制终稿直改的 `internal/tools/edit_chapter.go` 规则优先。
- **验收案例**：①同一文本多次出现时按块 ID 精确定位、不命中错误段落；②修改请求携带过期版本哈希时返回冲突而非静默整章改写；③拒绝候选后正文字节不变；④issue 置 ignored 后重分析不被翻回 pending。

### R2 轻量本地检索（短篇默认检索路径）

- **源路径**：`internal/story/knowledge_retrieval.go`（308 行，纯标准库）及其测试 `knowledge_retrieval_test.go`。
- **审计结论**：BM25 排序（`rankKnowledge`，k1≈1.2/b≈0.75 变体，稳定并列保序）；分词 = CJK bigram + 拉丁词/数字（`knowledgeTerms`，无需词典与 embedding）；长字段按整句切分节选并加 `[EXCERPT / 节选；省略部分未提供]` 标记（`settingExcerpt`）；预算打包整条保留不裁剪（`packKnowledge`，注释明确"裁剪设定/事实可能丢否定与条件"）；记忆条目按"当前章引用=1000 / 近 3 章=1"加权，证据片段 ≤300 rune 且只用已发生章（`retrieveMemories`）。实体名直接命中加 +1000 并展开一层关系邻居（不递归）。
- **移植位置（规划）**：`internal/shortstory/` 检索入口；请求接口对齐本仓库现有检索合同（来源与预算统计字段）。
- **保留**：算法逻辑；"节选不得作为自动覆盖权威数据的依据"约束；整条保留的预算策略。
- **改写**：数据结构适配本仓库资料模型与来源引用格式；不替换长篇既有 RAG 生命周期。
- **验收案例**：①纯中文查询经 bigram 命中目标设定；②预算耗尽时丢弃整条而非截半句；③被节选字段带省略标记，且下游不会把它当完整实体写回；④同名实体直接命中优先于普通 BM25 得分。

### R3 终稿校订（proofread）流程

- **源路径**：`internal/httpapi/web.go:89-99`、`internal/story/proofread.go`（345 行）、`postprocess.go`。
- **审计结论**：分析→issue 列表→按块修改/应用→备份→撤销 的完整闭环；`ProofreadRevision`/`ProofreadBlockChange` 支持按块落盘与导出前终校。
- **移植位置（规划）**：审核载体与报告展示 → `internal/workbench/`；校订动作语义并入 P2-04 短篇终审。
- **保留**：issue 生命周期与"应用生成新版本、可撤销"的语义。
- **改写**：issue 结构复用本仓库既有审核 issue 与正文哈希绑定；不引入其存储。
- **验收案例**：apply 后生成新版本并使旧审核失效；undo 恢复到 apply 前字节；部分接受（只应用部分 issue）时未应用项保持 pending。

### R4 前端组件与依赖

- **源路径**：`frontend/package.json`——运行依赖仅 `@xianii/design-system`、`marked`、`dompurify`。
- **审计结论**：交付形态为静态前端随二进制分发（本仓库计划同形，技术选 Svelte）；`@xianii/design-system` 为作者自有 npm 包，**许可证未核查**。
- **处理**：本仓库自建 Svelte 前端，不复制 SMTS 前端组件；若 P4 阶段要复用其任何组件/样式，先核查 `@xianii/design-system` 与对应文件的许可并记入 `THIRD_PARTY_NOTICES.md`。
- **验收案例**：不适用（本轮不引入）。

## Pagekin 行为参考记录（GPL v3，不复制代码）

### P1 修稿交互：选区修改、差异、逐段接受、版本

- **源路径（行为证据）**：`src/components/stage/chapter/VersionSwitcher.tsx`、`src/components/stage/history/version-diff.ts`、`src/components/stage/history/use-version-history.ts`、`src/components/stage/chapter/__tests__/selection-toolbar.browser.tsx`（选区工具栏）、`src/services/workflows/commands/review-revision-command.ts` 与 `review-revision-materials.ts`（修稿评审命令）、`src/shared/review-revision-generation.ts`；diff 依赖 `diff-match-patch@^1.0.5`（Apache-2.0）。
- **处理**：仅记录交互规格，在 `internal/authoring/` + `services/workbench/` 独立实现；差异计算可另选实现或直接引入 Apache-2.0 的 diff-match-patch 库（引入时记入第三方声明）。
- **验收案例**：①选区跨段落或命中重复文本时给出明确冲突、不静默改写；②逐项接受多个候选只产生一个新版本；③未选区字节不变；④版本回退以新草稿形式呈现，不覆盖历史。

### P2 文风分析

- **源路径（行为证据）**：`src/services/workflows/commands/analyze-style.command.ts`、`src/services/style-presets.ts`、`src/services/__tests__/prompt-style-imitation-contract.test.ts`。
- **处理**：计划已将文风分析列为 MVP 后项；本轮仅登记路径供后续参考，不移植、不实现。

## 许可与依赖结论

1. SMTS 为 MIT：R1—R3 的逻辑可复制/改写；实际引入当天在 `THIRD_PARTY_NOTICES.md` 保留其版权与许可文本，并逐文件记录来源 commit。
2. Pagekin 为 GPL v3：全部按行为参考独立实现；不得复制其源码、提示词文本与资产；若未来引入 diff-match-patch（Apache-2.0）等第三方库，单独记声明，不构成 GPL 传染。
3. 提示词与样例：SMTS 的提示词模板随 MIT 代码可复用但须保留声明；本轮审计未发现必须随代码引入的图片资产。
4. 本轮未运行外部应用；两仓库克隆保留在 `refer/` 供后续 P3 精读，不入库、不分发。

## 与计划第 3 节预期的差异

- 计划预期"Show Me The Story API 源码有块编辑及校订端点"——已核实（web.go:71-99），且块编辑同时存在于正文与 proofread 两组端点。
- 计划预期"knowledge_retrieval.go 含本地 BM25、CJK bigram 和上下文预算处理"——已核实，另发现记忆条目加权与证据片段约束（ retrieveMemories），一并纳入 R2 保留项。
- SMTS 前端依赖极简且含未核查许可的自有设计系统包——维持计划"自建 Svelte"决定，不复制前端。
