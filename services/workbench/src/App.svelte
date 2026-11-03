<script>
  // 统一写作工作台：作品列表 → 短篇详情（状态/目标/候选/确认）→ 差异。
  // 前端只提交意图（candidates/confirm/reject/cancel），不直接改项目文件。
  import { onMount } from 'svelte'

  let projects = []
  let selected = null
  let detail = null
  let events = []
  let diff = null
  let error = ''
  let manualContent = ''

  async function api(path, options) {
    const resp = await fetch(path, options)
    const body = await resp.json()
    if (!resp.ok) throw new Error(body.error || resp.statusText)
    return body
  }

  async function loadProjects() {
    const body = await api('/api/projects')
    projects = body.projects
  }

  async function openProject(name) {
    error = ''
    diff = null
    selected = name
    await refresh()
    events = (await api(`/api/projects/${encodeURIComponent(name)}/events`)).events
  }

  async function refresh() {
    detail = await api(`/api/projects/${encodeURIComponent(selected)}`)
  }

  async function confirmCandidate(target, candidateId) {
    error = ''
    try {
      await api(`/api/projects/${encodeURIComponent(selected)}/targets/${encodeURIComponent(target)}/confirm`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ candidate_id: candidateId, rules_summary: '工作台确认' }),
      })
      await openProject(selected)
    } catch (e) {
      error = e.message
    }
  }

  async function rejectCandidate(target, candidateId) {
    error = ''
    try {
      await api(`/api/projects/${encodeURIComponent(selected)}/targets/${encodeURIComponent(target)}/reject`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ candidate_id: candidateId, reason: '工作台拒绝' }),
      })
      await openProject(selected)
    } catch (e) {
      error = e.message
    }
  }

  async function submitManual(target) {
    error = ''
    try {
      await api(`/api/projects/${encodeURIComponent(selected)}/targets/${encodeURIComponent(target)}/candidates`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content: manualContent }),
      })
      manualContent = ''
      await openProject(selected)
    } catch (e) {
      error = e.message
    }
  }

  async function showDiff(target, from, to) {
    error = ''
    try {
      diff = await api(`/api/projects/${encodeURIComponent(selected)}/diff?target=${encodeURIComponent(target)}&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`)
    } catch (e) {
      error = e.message
    }
  }

  function pendingFor(targetId) {
    return (detail?.pending_by_target || []).filter((c) => c.target_id === targetId)
  }

  onMount(loadProjects)
</script>

<main>
  <h1>Novel Studio 工作台</h1>
  {#if error}<p class="error">{error}</p>{/if}

  <section>
    <h2>作品列表</h2>
    <ul>
      {#each projects as p}
        <li>
          <button onclick={() => openProject(p.name)}>
            {p.name} [{p.kind || (p.legacy ? 'legacy 只读' : '?')}]{p.stage ? ` 阶段=${p.stage}` : ''}
          </button>
        </li>
      {/each}
    </ul>
  </section>

  {#if detail}
    <section>
      <h2>{detail.name} <small>{detail.state ? `阶段=${detail.state.stage} 运行=${detail.state.run_state}` : (detail.legacy ? '只读' : '')}</small></h2>
      {#if detail.targets}
        {#each detail.targets as t}
          <div class="target">
            <h3>{t.target_id}
              <small>确认={t.author_approved ? '✓' : '未'} 待决={t.pending_candidates} 需重审={t.requires_review ? '是' : '否'} 陈旧来源={t.stale_sources}</small>
            </h3>
            {#each pendingFor(t.target_id) as c}
              <p>
                候选（{c.kind}，基线 {c.base_version_id?.slice(0, 18)}…）
                <button onclick={() => confirmCandidate(t.target_id, c.candidate_id)}>接受</button>
                <button onclick={() => rejectCandidate(t.target_id, c.candidate_id)}>拒绝</button>
              </p>
            {/each}
            <details>
              <summary>手改（提交为候选）</summary>
              <textarea rows="4" bind:value={manualContent}></textarea>
              <button onclick={() => submitManual(t.target_id)}>提交候选</button>
            </details>
            {#if t.version_id && t.root_version_id !== t.version_id}
              <button onclick={() => showDiff(t.target_id, t.root_version_id, t.version_id)}>与初始版本比较</button>
            {/if}
          </div>
        {/each}
      {:else}
        <p>该作品为 legacy/长篇：只读查看，可经 /long 接口获取章节投影。</p>
      {/if}
    </section>
  {/if}

  {#if diff}
    <section>
      <h3>差异（{diff.from?.slice(0, 18)}… → {diff.to?.slice(0, 18)}…）</h3>
      {#each diff.ops as op}
        <p class={op.kind}><code>{op.kind}</code> {op.text}</p>
      {/each}
    </section>
  {/if}

  {#if events.length}
    <section>
      <h2>事件（来自持久工件）</h2>
      <ul>
        {#each events.slice(-12) as e}
          <li><small>{e.at}</small> {e.type} — {e.summary}</li>
        {/each}
      </ul>
    </section>
  {/if}
</main>

<style>
  /* 显式声明亮色方案：不随 UA 的 prefers-color-scheme 翻转，避免暗色
     环境下出现黑底黑字（P5-03 走查实证：未声明时正文在暗色浏览器不可读）。 */
  :global(html) { color-scheme: light; }
  :global(body) { background: #fafafa; color: #1a1a1a; margin: 0; }
  main { max-width: 860px; margin: 0 auto; font-family: system-ui, sans-serif; }
  .error { color: #b00020; }
  .keep { opacity: 0.6; }
  .delete { color: #b00020; }
  .insert { color: #0a7d32; }
  .target { border: 1px solid #ddd; border-radius: 6px; padding: 8px; margin: 8px 0; }
</style>
