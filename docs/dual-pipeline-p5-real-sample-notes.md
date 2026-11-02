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
