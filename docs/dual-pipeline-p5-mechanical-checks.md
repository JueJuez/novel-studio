# P5-01 机械/兼容/恢复检查报告

Owner：ouyang。状态：生效。更新日期：2026-11-01。

执行环境：WSL2 Ubuntu 24.04 + Go 1.27.1（与 CI 同版）；检查时基线提交 `55b9ab3`。
执行方式：`output/acceptance/p5-01-run.sh`（仓库内，gitignored）逐项复刻
`.github/workflows/ci.yml` 全部可本地执行检查；原始日志在
`output/acceptance/p5-01-logs/`（gitignored），本文件只保留结论与映射。
race 分片在提交 `55b9ab3` 后的工作树上执行，但分片目标包
（internal/store、internal/agents）未受本批后续改动影响。

## 1. CI 等价检查结果

| 检查项 | 结果 | 备注 |
|---|---|---|
| `go test ./...`（35 包） | 通过 | 0 FAIL |
| `third_party/litellm` `go test -race` | 通过 | |
| Dashboard Python（53 项） | 52/53 | 唯一失败 `test_body_cache_invalidates_same_size_rewrite_and_atomic_replacement` 经 ext4 对照判为环境差异：同一套件在 WSL 原生 ext4（/tmp 拷贝）53/53 全过，仅 /mnt/d（drvfs）失败——同尺寸改写的缓存失效依赖 mtime 粒度，drvfs 粒度粗导致误判命中。与 P0-01 记录的 ctime 粒度 flake 同类，CI（Linux ext4）通过 |
| Dashboard node（test_client/test_broadcast_client） | 通过 | |
| Release 构建 + 冒烟 | 通过 | `--version` 正常；`doctor --config <不存在>` 退出码 1（预期） |
| Race 分片合同（agents/store） | 通过 | Windows 检出 CRLF 需去 CR 后执行（脚本见 run.sh 注记），CI 无此问题 |
| Race 敏感包（internal/tools、services/dashboard，-race） | 通过 | |
| Store race 分片 0/1 | 通过 | |
| Agents race 分片 0/1/2/3 | 通过 | |

容器构建（docker build-push）未在本地复刻：本批不涉及镜像发布，留待 P5-04 发行准备。

## 2. 计划第 8 节机械/兼容项 → 强制测试映射

| §8 要求 | 强制测试（全部在上述全量检查中通过） |
|---|---|
| 两管线状态与依赖隔离；短篇零长篇依赖，缺 embedding/Qdrant 不阻断 | `TestShortStoryPipelineIsolation`（无 output/novel、旧长篇工程 Open 拒绝）、`TestSharedPackageBoundaryImports`（go/parser 静态断言 authoring/project/shortstory 不引用 host/agents/tools/store/cmd/services） |
| 正文/规则/计划变化后旧作者确认、旧审核、旧交付清单不能复用 | `TestFinalizeGatesAndExportConsistency`（四级阻断链）、`TestDeliveryBlockedAfterSceneRework`（返工后旧终审失效）、`TestAuthorApprovalInvalidatesOnAdvanceAndModelAcceptIndependent`、长篇侧 `TestRequireLongAuthorGateScenarios`（版本变化重确认）+ `TestLongSealAndPromoteGateKeys` |
| 模型 accept 与作者确认分别记账、互不替代 | `TestChapterRenderModelAcceptAloneDoesNotPublishCanon`（模型通过跳步发布被拒）、`TestAuthorApprovalInvalidatesOnAdvanceAndModelAcceptIndependent` |
| 原文未变拒绝候选不改文件；重复接受不重复推进；旧请求不覆盖新版本 | `TestCandidateAcceptRejectVersionConflictAndIdempotency`、`TestModifyContractConflicts`（ErrVersionConflict/ErrBlockConflict）、`TestReviseBlockCandidateDeterministicReassembly`（未选区逐字节不变）、`TestInsertAfterAcceptSequenceAndRevert` |
| 生成结束/候选保存/审核返回/作者接受/正式发布之间中断，重启保持上一完整状态 | `TestAuthoringCrashRecoveryConvergence`（两个崩溃点）、`TestShortStoryCreateGuardsAndCrashCompletion`、`TestShortStoryTamperedArtifactFailsRecovery`、chapter-render 事务 9 项既有 store 测试（幂等/冲突/崩溃孤儿/坏摘要链/并发收敛） |
| 工作台与原 CLI 同写同一作品由同一权威锁拒绝 | `TestPipelineExecutionCrossProcessFlockBlocksAndCrashReleases`（跨进程 flock）、`TestWorkbenchAdvanceIntentRunsAndReports`（工作台单飞）、长篇推进经同一 CLI 入口（自执行）共用执行锁 |
| 修改旧长篇不绕过原 sealed/rebase 边界 | `TestProjectLongPipelineReadOnly`（投影前后文件零变化）、foundation 阶段 sealed 保护（`runPipelineFoundationStageAtOutput` 拒绝改写 active sealed generation）、adopt 空闲边界 `TestWorkbenchAdoptRejectsActiveLease` |
| 缺 manifest 旧短篇/长篇、未完成 generation、旧全文终审、complete-book 样本副本回放，不默认升级原件 | `TestDiscoverClassifiesManifestAndLegacy`、`TestCreateRefusesLegacyWorkDir`、`TestLoadRejectsUnknownSchemaAndUnknownFields`、`TestWorkbenchAdoptLegacyProject`（显式接入、非标准布局拒绝）；engine 恢复路径（recoverChapterRenderTransactionCandidate 等）由 chapter-render store 测试覆盖 |
| 导出绑定作者确认版本；MD/TXT/正文哈希一致；备份恢复新副本 | `TestFinalizeGatesAndExportConsistency`（三哈希互验+确认绑定+备份）、`TestDeliveryBlockedAfterSceneRework` |
| 阶段状态机固定、取消不冒充正文阶段 | `TestShortStoryRejectsIllegalTransitions`、`TestShortStoryRunStateSeparateFromStage`、`TestWorkbenchEventsStreamAndCancel` |
| 预算/DAG/承诺、审阅原文绑定 | `TestScenesGenerationValidatesBudgetDAGAndPromises`、`TestReviewQuoteBindingAndRoundtrip`（quote 逐字校验，幻觉定位拒绝）、`TestImpactPreviewAndStaleness` |

## 3. 结论

- 全部 §8 机械/兼容/恢复项均有已执行的强制测试，无缺口；阻断项可定位到具体测试名。
- 唯一环境相关失败（Dashboard mtime 粒度）已用 ext4 对照实验归因，非产品回归；已记入 P5-03 浏览器走查的注意事项（在 drvfs 上本地运行时同尺寸改写缓存可能误判）。
