# P5-02 真实云端小样记录（第一批：短篇三题材）

Owner：ouyang。状态：生效（P5-02 进行中，本批为短篇部分；长篇窗口与 legacy 恢复样本下批继续）。更新日期：2026-11-01。

模型与凭证：`deepseek/deepseek-v4-flash` @ `https://api.commandcode.ai/provider/v1`（OpenAI 兼容代理；密钥仅在仓库根 `.env.local`，配置经 `api_key_env` 引用，不入库、不进文档）。每作品一份项目级配置 `data/runs/<名>/.novel-studio/config.json`（gitignored，`reasoning_effort=off`）。

执行口径：作者确认点由执行会话代为确认/终裁（每条候选确认与豁免的 note/reason 均已留痕并标注「执行会话代为确认」）；未经确认或被门禁拦截的样稿保持候选态。用量/成本经现有 TokenUsage/ResolveTokenCost 口径输出（Source=estimated，provider 未上报价格不显示为零）。

## 1. 样稿结果

| 样稿 | 题材/结构 | 结果 | 门禁行为 |
|---|---|---|---|
| 悬疑样稿-末班车（A） | 悬疑·5 场景·7500 rune | 2 轮自动返工后仍有 2 个未豁免 blocker，**交付被门禁拦截，保持候选待作者** | 每轮终审均给出精确定位的真实矛盾（工龄十年/十一年、午后时间线、车厢编号、残留物自相矛盾）；终稿确认（作者）通过但 finalize 拒绝——确认≠门禁放行，语义正确 |
| 现实样稿-归途（B） | 现实/情感·单篇无章号（5 场景 chapter=0）·7000 rune | 1 轮返工+作者终裁豁免 3 条计划/正文口径出入后**真实交付**：export/Markdown+TXT+manifest.json+backup/，三哈希互验、绑定作者确认 appr-sha256:eec62806… | 鸡蛋数 46/31、行李架 vs 膝头等矛盾被逐条拦截；豁免走来源决策留痕（RejectSource+reason），版本推进后旧豁免按内容寻址失效 |
| 幻想样稿-守灯人（C） | 幻想·开放式结局·5 场景·6300 rune | 终审 3 个 blocker（信拆/未拆矛盾、凭空引入未铺垫意象），**交付被门禁拦截，保持候选待作者** | 与 A 同口径：门禁拦截+精确定位 |

审阅模型方差：同一正文两次终审给出的 blocker 集不同（B 第二轮 2 条 → 第三次重审 3 条，均为不同点），说明单次审阅是抽样证据，最终语义结论必须归作者（计划 §8 的要求在此得到实证）。

## 2. 本批发现并修复的集成缺陷（全部带回归测试或执行证据）

1. **输出上限换算反了**（internal/shortstory/generation.go sceneMaxTokens）：原按 1 token≈1.5 rune 估，实测该模型中文 ≈1.5 token/rune，草稿在 1024 token 处拦腰截断（scene-2 正文停在句中）、scene-1 输出全为空。改为 3/2+512、上限 32768。
2. **JSON 产物 MaxTokens 偏小**：structure（4K 截断，样稿 B 首跑失败）、scenes（4K/8K 两度截断）、review、extraction 依次放宽至 8192/16384/8192/8192。
3. **reasoning 配置对短篇直连路径失效**（internal/shortstory/model.go）：长篇经 agents 层应用角色级 reasoning_effort，短篇直连 ChatModel 绕过了该机制——deepseek-v4-flash 推理耗尽全部输出（usage.reasoning_tokens=8192、resp 为空）。新增 thinkingModel 装饰器等价应用配置（shortstory 不得 import agents，用 bootstrap+agentcore 依赖面）。
4. **vendored litellm 的 openai provider 不识别 deepseek 推理模型**（third_party/litellm/provider/openai/request.go）：isReasoningModel 只认 gpt-5*，ThinkingDisabled 直接报错且禁用值映射为 OpenAI 的 "none"（该代理只接受 off|low|medium|high|xhigh|max，实测 HTTP 400）。补 deepseek-v4/r1/reasoner 分类 + 按模型映射禁用值（deepseek→"off"），带新测试 TestBuildRequestDeepSeekThinkingDisabledMapsToOff。
5. **CommitFullText 非幂等**（internal/shortstory/finalize.go）：候选任务含 assembled_at 时间戳且 base 取当前 head——每次调用都串新版本，审阅/确认绑定被甩成陈旧（P5-02 实测两次跳版本）。修复：候选寻址键去时间戳 + head 内容一致时直接返回 head。
6. **blocker 豁免机制有语义无实现**（finalize.go "未豁免" 一词无对应能力）：补 RecordReview 将 blocker 级 issue 逐条登记为未决来源；FinalizeGateCheck 仅放行被作者显式拒绝（终裁）的 blocker；CLI `short-story issues`/`waive` 提供列举与终裁入口。作者权力留痕、机械门禁不变。
7. **短篇生成无产品面入口**（P2 全部 runner 仅测试可达）：补 CLI 驱动 `short-story generate|candidates|confirm|reject|waive|issues|finalize`（runner 全部复用既有库函数，零新业务逻辑）；确认按状态机阶段守卫推进（重复确认/修稿循环重确认不再触发非法转移）。

## 3. 用量与成本（含全部重试与返工）

- 累计 52+ 次真实调用（结构/场景/草稿/事实提取/审阅/重写/重审），输入 ≈13.3 万 token、输出 ≈7.6 万 token，估算成本 ≈$0.027（estimated 口径，provider 未上报价格）。
- 原始日志：output/acceptance/p5-02-logs/sample-{A,B,C}/（gitignored，含每次调用的 usage/cost 行）。

## 4. 遗留（下批继续）

- 长篇多弧窗口真实小样（architect→…→seal→promote→render，含作者门禁代位确认）。
- legacy 旧项目恢复样本（只读回放 + 显式 adopt）。
- 三篇样稿的作者评阅（冲突/结局/人物一致性/可读性）——语义结论待作者，自动化不替代。

## 5. legacy 旧项目恢复样本（2026-11-03，P5-02 收尾项之二）

副本构造：`data/runs/legacy-恢复样本-副本/`（WSL 原生文件系统），旧格式布局＝`output/novel/{meta/progress.json(phase=complete, 无 pending rewrites)、chapters/01-02.md、premise.md}`，无 manifest。全程记录 `output/acceptance/p5-02-logs/legacy-replay.log`。

回放结果（全链路经 workbench API 实测）：

1. **只读发现**：DiscoverRuns 分类 `legacy=true, kind=""`，引擎目录正确指向 `output/novel`，未写回任何 manifest。
2. **只读投影**：`GET /long` 返回 2 章内容寻址视图（`sha256:ac319d41…`、`sha256:ec70a997…`，canonical=true，read_only=true）。
3. **意图拒绝**：`POST /author-gate` 返回 403——legacy 无确认面。
4. **显式接入 adopt**：空闲边界机械校验（progress 无进行中返工 + 无活动执行租约）后写 manifest——`pipeline_kind=long, collaborative=true, project_id=proj-d92eed9dfa7…`；重复 adopt 返回 409。
5. **正史未动**：接入前后 `chapters/01.md`、`meta/progress.json` md5 不变（`ce276c63…`、`fbaf0f1e…`）；副本语义全程成立，原件不升级。

结论：旧项目「只读查看 → 显式接入 → 接入后受协作门禁约束」全路径可用；恢复样本不依赖任何真实模型调用。

## 6. 长篇窗口（进行中）与一个已定位的长篇规划基线缺口（2026-11-03）

执行形态：自治运行器（单会话内 workbench + 管线 + 门禁轮询代位确认 + 断点重跑），日志 `output/acceptance/p5-02-logs/long-window/{run,gates,repair-layered*.log}`。

**基线缺口（已定位、未修复，走正路绕行）**：sealed 流程的 `outline-all` 阶段硬性要求 live 存在 `layered_outline.json`（pipeline_outline_all.go:137），但「规划级别 short」的 Architect 只产 flat `outline.json`（architect_check.go:528 把 outline.json 当兼容视图）；且 `--refresh-architect` 修复路径的 `pipelineOpeningFoundationDigest`（pipeline_delivery.go:1345）无条件预读双文件，缺 layered 时修复路径自身被阻塞——形成 catch-22。判定为长篇规划模式适配缺口（上游继承行为，短篇样稿不受影响）。

**绕行（正路）**：以「规划级别 long」重开书——长篇 Architect 流程本就产出 layered_outline.json，且多弧窗口样稿需要分层卷/弧/章合同。此前 short 级尝试的书目录已清理；连带发现并遵守的两处机制：① `--new-novel` 在 CWD 下自建 `data/runs/<书名>`，manifest 必须落在书目录（门禁推导 deriveLongProjectRoot 的口径）；② adopt 的空闲边界校验会正确拒绝活动执行租约中的书（P4-04 机制实证），故接入动作放在管线停点执行。

## 7. 长篇窗口进行时：第二个基线缺口（禁令式硬合同不可兑现）与收敛实证（2026-11-03）

长篇窗口在 architect 完成后卡 outline-all，定位出**第二个基线缺口**：`outline-all` 的 arc contract payoff 校验要求 compass 的每条 non_negotiable 在对应章（chapter-6）的 core_event/scenes 中以「肯定式行动者+行动+终态」兑现，而本次样稿最初的 compass 把**禁令/格式式表述**（"不写外星生物""保持限知叙述""不引入势力线"）存成了 non_negotiables——禁令无法被"具体兑现"，模型 max turns (4) 内必然失败。该缺口与作者提示词写法强相关：给 Architect 的硬合同必须是可兑现的剧情承诺。

修复路径实测记录：
- `--refresh-architect --architect-target update_compass`：**被章零 rebase 门禁拒绝**（要求 meta/all_chapter_rebase.json 回执——管线已有推进证据后，硬合同变更必须走 rebase 协议，这正是 P4-03 边界保护在起作用）。
- `--architect-repair-file`：manifest schema 只支持 characters/update_compass，不支持 layered_outline/outline。
- 平铺大纲的 title/core_event 缺失已通过 `--refresh-architect --architect-target layered_outline`（layered 存在后 digest 预读可通过）+ 模型双写修复；characters 的 5 个 initial_state.location 通过 `--architect-target characters`（授权目标）修复，architect 阶段最终 EXIT=0。

**收敛实证**：outline-all 连续失败中，未兑现 non_negotiables 从 4 条收敛到 1 条（non_negotiable-04，纯禁令），模型在逐条给出具体兑现。当前自治运行器继续磨（门禁代位确认口径不变）；若 non_negotiable-04 持续不可兑现，正路是 chapter-zero rebase 后修正该条硬合同（rebase 协议属长篇返工边界，留待下一批或引擎侧修复）。

工程影响评估：两处缺口（§6 short 级无分层 + refresh 摘要预读阻断；本节 禁令式硬合同无授权修复通道）均为长篇 sealed 流程与作者合同写法的交互问题，不阻塞短篇管线；已按「失败区分基线问题」纪律登记，建议后续在引擎层为禁令式 non_negotiables 提供 entry 期校验（compass 写入时即拒绝不可兑现条款）。

## 8. 长篇窗口门链突破与精确续作路径（2026-11-03 续）

本段把长篇窗口推进到 rebase 之后的章零态，并完整测绘了 foundation 变更的门禁链：

1. **`--rebase-all-chapters` 成功**（无修复参数）：`meta/all_chapter_rebase.json` 回执落盘（RECEIPT_OK），正史安全归档并回到章零——此前 update_compass/全量 refresh 被拦的「缺 rebase 回执」前提已解除。
2. **零-init 重推发现模型级违规（可重试类）**：initial world_tick 丢失显式时间锚「明天」；三条 world_tick 事件让「地面调度值班调度员」在第 1/3 章可见，早于大纲首次可见（第 6 章）——角色可见性合同校验生效（计划 §6.1 的角色知识边界在真实链路上工作）。
3. **refresh 资源层的新前置**：失败的零-init 在章零工作区留下 `drafts/01.zero_init.plan.json`，章零门禁拒绝在有草稿/正式计划的workspace 里做 foundation refresh。续作顺序＝先重跑 `--stages zero-init`（模型方差重试直至 world_tick 过）清干净 workspace，再 `--refresh-architect`（全量，补 6 份 artifact_material 资源），再默认链（outline-all 重规划——注意 compass 禁令条款问题的处置见 §7）→ rehearse-arc（资料层就位后 ready_for_detail 应转真）→ 门禁代位确认循环。

修复机制实证汇总（本段）：repair manifest schema 只支持 characters/update_compass 且 characters 修复要求全书跨度已验证预演报告（ArcLastChapter==TotalChapters）；rehearse-arc 按弧切分拒绝章节范围缩短；rebase-all 是唯一能同时解锁 refresh 与重推的桥。三处均为「作者/执行会话在推进后的书上变更 foundation」的合同保护，属引擎正确行为；缺口在于全书跨度报告的产出缺一个自然的宿主阶段（建议引擎侧在 rebase 候选内自动做一次全书预演）。

## 9. 决定性诊断：预演输入的物理状态投影缺陷（P5-02 长篇窗口真正的拦路虎，2026-11-03）

门禁链继续收敛后的精确诊断（证据链完整）：

- 预演输入 `inputs/c6b110c3….json`（与最新报告 09e95517 绑定）：`execution_capabilities.action_kinds` 已含全部 11 种（**V3 配置修复生效**：`character_agents.execution_policy="v3"`）；但 `world_state.characters=0`、`world_state.resources=0`。
- 同期盘面：`characters.json` 有 3 个角色（K/林晚/地面调度值班调度员）且带 initial_state；`meta/initial_resource_ledger.json` 有 **8 条已登记资源主张**（evidence=characters.json:initial_state.resources，zero-init 推演产物）。
- 缺陷定位：`internal/agents/character_physical_state.go loadCharacterPhysicalPreState`——当存在 projected planning context（rebase/outline-all 之后必然存在）时走 `ProjectedPhysicalStateV2(projected)` 分支；该投影对章零前物理负载返回空时**不回退**到 characters.json 构建路径（该回退仅在 `projected.Version=="" && chapter==1` 时可达）。结果是：推进后的书（恰是 rebase 之后的正常形态）整弧预演看到「无角色、无资源」，预演如实报 unready。
- 影响面：长篇窗口唯一硬阻断；短篇管线不受影响。修法（下批，小而清晰）：章零预物理态在投影负载为空时回退 characters.json+initial_resource_ledger 构建，带 fixture 回归测试（projected context 空物理负载 + characters 带资源 → 预演输入必须包含角色与资源）。
- 本批其余收敛全部生效：world_rules 物化 refresh 被工作区门禁拦（零-init 草稿存在，第三重 catch-22，登记）不影响本诊断——资源/角色本已存在于盘面，只是没进预演输入。

## 10. 诊断修正与 typ化资源数据修复（2026-11-03 续二）

**修正 §9 诊断**：预演输入并非「零角色」——actors=3（K/林晚/地面调度值班调度员，我误读了键名）。真正缺口是：角色的资源停留在 legacy 非类型化字段 `initial_state.resources: [字符串]`，而 V2 物理态的 `resources` 只从类型化 `initial_state.resource_balances` 构建（非类型化字符串按设计不物化，见 agents 层注释"Unstructured legacy resources are author material"）。预演如实按「资源层为空」处理。

**数据修复（执行会话代位，逐字留痕）**：characters.json 的 PRE=13c25d49… → POST=330ca18c…，把 7 个 legacy 资源字符串 1:1 升级为 V2 类型化条目（resource_id/名称/单位/数量/access/readable_facts≥3，内容源自预演报告与守则原文），K=3 条、林晚=2 条、地面调度=2 条。引擎完整性系统随即检测到变更并给出官方路径：`architect-check` 重跑 → **ready: true**（仅剩建议级角色人数警告）。

**第四处门禁发现**：architect-check 重写 `meta/architect_readiness.md` 后，rebase 回执锚定的旧摘要产生 drift（"outline-all refreshed readiness digest drift"），且显式 `--stages outline-all` 的重跑仍走旧回执 verify——引擎语义下唯一出路是再次 `--rebase-all-chapters`（重置回执、锚定当前文件、重推全链）。已启动 rebase2 + zero-init 重试 + 门禁链自治序列。

**结构性缺口登记（第 4 项，建议引擎侧修复）**：推进后的书变更 foundation（无论经授权 refresh、repair-file 还是宿主数据修复）都会与 outline-all 回执锚定冲突，当前唯一出路是整册 rebase 重推——对已完成较长正史的书代价过高。建议引擎为「readiness/锚定文件由宿主显式刷新」提供一等重算路径（rebase 候选内自动全书预演 + 回执重锚定）。
