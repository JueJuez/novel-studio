# 作者确认边界定位审计（P0-03）

更新日期：2026-10-31。状态：已完成定位与 fixture 验证；Hook 实施在 P4-03，确认记录合同在 P1-03。

方法：静态源码定位真实发布链（file:line 均为本仓库当前 main）+ 隔离 fixture 测试；未发起任何模型调用。

## 1. 真实发布链（当前实现）

章节正文从生成到正史是一个内容寻址的多阶段事务状态机，定义在 [chapter_render_transaction.go](../../internal/domain/chapter_render_transaction.go)：

```text
body_ready → committed → formal_accepted → actual_matched → published
           → outcome_accepted → chapter_accepted → completed
旁路：structurally_blocked（终态）、formal_rejected（不可变负回执，唯一出路是重新 formal_accepted）
```

- **转移表固定且单调**（`ValidateChapterRenderPhaseTransition`，domain:277-314）：不可跳步、不可回滚；`published` 的唯一入口是 `actual_matched`，`formal_accepted` 的唯一入口是 `committed`。
- **回执哈希链**：每阶段一张不可变回执（`PreviousReceiptDigest` 串联，domain:95-103），单文件原子写入，无 HEAD 指针。
- **正文不可变**：`Advance` 前强制重验 body 内容寻址（store:118-120）；事务根是输出目录的隐藏兄弟目录 `.render-transactions/<namespace>`（store:23,58-64），与 canonical 输出树物理分离。

各阶段在真实发布链上的推进调用点：

| 阶段 | 证据合同（domain validateChapterRenderPhaseEvidence） | 推进调用点 |
|---|---|---|
| 模型正式审（formal_accepted） | verdict=="accept" + disposition + 审查工件绑定（domain:423-426） | [pipeline_render_candidate.go:2931,2961](../../cmd/novel-studio/pipeline_render_candidate.go)、[pipeline_planning_stages.go:1239,1257](../../cmd/novel-studio/pipeline_planning_stages.go)；裁决来源 `ReviewVerdict: inspection.Verdict`（[pipeline_chapter_render_transaction.go:258](../../cmd/novel-studio/pipeline_chapter_render_transaction.go)） |
| exact-body 复核（actual_matched） | actual_match_digest | pipeline_planning_stages.go:1051,1293 |
| 目录晋升（published） | directory_publish_id + digest；正文字节由 `DirectoryPublishStore` 原子晋升 candidate→live，快照/manifest/发布回执全部重验后才落回执（[pipeline_chapter_render_transaction.go:405-489](../../cmd/novel-studio/pipeline_chapter_render_transaction.go)） | pipeline_planning_stages.go:1077,1302 |
| 结果/验收/收尾 | outcome / chapter_acceptance / render_receipt digest | pipeline_chapter_render_transaction.go:491,535,594 |

## 2. 作者确认 Hook 定位（对应计划 §6.2 的四个边界）

| 边界 | 挂载点 | 机制 |
|---|---|---|
| 初始化产物确认 | architect 初始状态提升回执（architect_character_initial_state.go；promotion receipt digest 已进入 ChapterRenderPlanIdentity，domain:28） | collaborative 策略下提升前校验作者确认记录，确认绑定 promotion digest |
| 封存前计划确认 | 弧窗口封存回执链（pipeline_arc_cycle*.go、pipeline_arc_window_completion.go；internal/store/arc_cycle.go，flock 保护） | seal 前拦截；等待时不持执行租约 |
| **正文发布前（主 Hook）** | (i) domain 转移表在 actual_matched 与 published 之间插入作者确认阶段/证据；(ii) cmd 层 `pipelineAdvanceChapterRenderPublished` 在 DirectoryPublishStore 晋升动作前校验作者确认 | 两道闸：阶段机拒绝 + 晋升函数拒绝；确认记录绑定 candidate root / body sha（P1-03 合同） |
| 最终交付前 | pipeline_finalize.go（GlobalReviewVerdict，:487）与 pipeline_complete_book.go 交付合同 | 交付前置校验；未确认停在 waiting_author |

**当前差距**：全库不存在作者确认概念（grep `author_confirm|authorApproval|author_approved` 仅命中无关文案 zero_init_assets.go:623）——现状是"模型通过即推进"，正史化不依赖作者。计划 §2"不能把模型 accept 当成作者认可"的边界正是 P4-03 要补的；本审计证明该边界可以纯机械方式插入，不需要新的执行机制。

## 3. 取消边界

- 每个阶段回执都是一个持久取消点：中断即停在最后有效回执，不存在半态。
- 崩溃恢复路径已存在：`recoverChapterRenderTransactionCandidate`（cmd:989）、`pipelineRecoverChapterRenderCompletionIfTracked`（cmd:744）、`loadPipelineRecoveredChapterTransactionSnapshot`（cmd:1265）；无回执的孤儿 body 由 `ListPlanBodies` 按内容寻址安全处理（store:197-233）。
- **作者等待不持锁**：waiting_author = 事务停在不推进；重启走既有恢复路径重验版本，执行租约已释放。计划 §6.2 的要求在机制上天然成立，无需新锁。
- 既有测试覆盖幂等/冲突、崩溃孤儿、坏摘要链、并发收敛（internal/store/chapter_render_transaction_test.go）。

## 4. long adapter 与既有存储复用

- **适配点**：长篇编排入口 cmd/novel-studio/pipeline_cmd.go + 上表推进调用点。collaborative 策略在这些 advance 调用前检查确认状态（P4-03 实施）；旧默认策略路径不新增行为。
- **可无损复用**：ChapterRenderTransactionStore（新增作者确认 phase 值不改回执 schema；旧项目 completed 是终态不再推进，天然兼容）、DirectoryPublishStore、执行锁/租约、checkpoint、用量与成本来源。新增仅 internal/authoring 的确认记录（P1-03）。
- **风险与注意**：`ChapterRenderPhaseEvidence` 是 typed union，新增作者确认字段不影响旧回执 digest（旧回执不含新字段）；转移表扩展需同步 domain 转移测试；作者确认记录必须绑定正文/计划哈希，正文变化即失效（计划 §6.3 关键约束 2）。

## 5. fixture 验证（隔离，无模型调用）

新增 [chapter_render_transaction_author_boundary_test.go](../../internal/store/chapter_render_transaction_author_boundary_test.go) `TestChapterRenderModelAcceptAloneDoesNotPublishCanon`：

1. BeginBody → committed → formal_accepted（evidence：verdict="accept" + 工件绑定），模拟"模型通过"。
2. 断言 latest == formal_accepted。
3. 断言 formal_accepted → published 直接跳步被拒（"cannot advance"）。
4. 断言回执链中不存在 published 回执。
5. 断言 canonical 输出目录零字节（事务产物全部在隐藏事务根内）。

结果：连同既有 9 个 chapter-render store 测试全部通过（WSL2 Ubuntu 24.04 + Go 1.27.1，2026-10-31，套件 0.36s）。

**结论**：把作者确认边界插在 actual_matched 与 published 之间后，"模型通过但未获作者确认不会成为正史"由阶段机强制保证；插入点是 domain 转移表一处 + cmd 晋升函数一处，均为既有持久化机制的扩展，不引入新执行原语。
