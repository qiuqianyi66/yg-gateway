/* app_views_logs.js — 日志视图（从 app.js 拆出的第十个组件，2026-11-02）
   ────────────────────────────────────────────────────────────────────────────────
   内容：日志页（频道：全部/任务/对话/系统）、请求指标聚合、请求记录表与筛选。

   纪律：**只许声明**——本文件排在 app.js 之前加载（app.js 顶层路由会调用 renderLogs*）。
   顶层事件绑定与状态（logCh / 各 $('x').onclick）留在 app.js（引用 `$`）。

   依赖（运行时解析）：app_core.js 的 esc/ago/dur/fmtTok；app.js 的 $/api/状态。 */
/* ── 日志（频道：全部/任务/对话/系统） ─────────────────────────────── */
async function loadLogs() {
  const box = $('logBox');
  const atEnd = box.scrollTop + box.clientHeight >= box.scrollHeight - 24;
  const limit = ($('reqLimit') && $('reqLimit').value) || 100;
  // 时间范围由归档侧过滤（不是前端筛已拉取的条目）：区间落在更早的时间段时，
  // 「最近 N 条」里根本不会有那些记录，必须让服务端按时间取。
  const rq = trangeQuery('reqRange', false);
  rq.set('limit', limit);
  try {
    const [d, metrics, requestRows] = await Promise.all([
      api('logs'),
      api('request_metrics').catch(() => ({})),
      api('request_logs?' + rq.toString()).catch(() => ({ entries: [] })),
    ]);
    // 归档开启时以归档为准——「区间内没有记录」是一个真实结果，不能回落成内存里
    // 的最近 100 条（那会把筛选条件之外、时间范围之外的请求显示出来）。
    // 只有归档关闭时才回落到内存指标，保证没有归档的部署仍能看到最近请求。
    const archiveOn = !!(metrics && metrics.archive && metrics.archive.enabled);
    const recent = archiveOn ? (requestRows.entries || []) : (metrics.recent || []);
    renderRequestMetrics(metrics, recent);
    const entries = (d.entries || []).filter(e => logCh === 'all' || e.ch === logCh);
    box.innerHTML = entries.length
      ? entries.map(e => {
        const lvl = /error|失败|错误/.test(e.text) ? ' e' : /warn|冷却|熔断/.test(e.text) ? ' w' : '';
        const t = e.ts ? new Date(e.ts).toLocaleTimeString('zh-CN', { hour12: false }) : '';
        const ch = logCh === 'all' ? '<i class="lch c-' + esc(e.ch) + '">' + ({ task: '任务', chat: '对话', sys: '系统' }[e.ch] || e.ch) + '</i>' : '';
        return '<span class="ln' + lvl + '">' + ch + esc(t + ' ' + e.text) + '</span>';
      }).join('')
      : '<span style="color:var(--ink-3)">暂无日志</span>';
    if (logPin && atEnd) box.scrollTop = box.scrollHeight;
    const counts = {};
    for (const e of (d.entries || [])) counts[e.ch] = (counts[e.ch] || 0) + 1;
    $('logNote').textContent = logCh === 'all'
      ? '任务 ' + (counts.task || 0) + ' · 对话 ' + (counts.chat || 0) + ' · 系统 ' + (counts.sys || 0)
      : (logCh === 'task' ? '任务' : logCh === 'chat' ? '对话' : '系统') + ' ' + entries.length + ' 行';
  } catch (e) {
    // 此前是 `catch (e) { /* 概览已提示 */ }` —— 但概览的 toast 只在非 quiet 时弹，
    // 且可能早已消失，日志页失败就变成"旧内容一直挂着"。改为在日志区就地提示 + 重试。
    const boxEl = $('logBox');
    if (boxEl) boxEl.innerHTML = loadErrorHTML('读取日志失败', e.message, '重试');
    if (boxEl) bindRetry(boxEl, loadLogs);
  }
}

function renderRequestMetrics(m, entries) {
  m = m || {};
  const a = m.archive || {};
  $('reqSummary').textContent =
    '已完成 ' + fmtTok(m.completed) +
    ' · 成功 ' + (m.success_rate == null ? '—' : Number(m.success_rate).toFixed(1) + '%') +
    ' · HTTP ' + (m.http_success_rate == null ? '—' : Number(m.http_success_rate).toFixed(1) + '%') +
    ' · 平均 ' + fmtMs(m.avg_duration_ms) +
    ' · 进行中 ' + String(m.in_flight || 0);
  $('reqNote').textContent = a.enabled
    ? 'JSONL 归档 ' + fmtBytes(a.bytes) + (a.dropped_writes ? ' · 丢弃 ' + a.dropped_writes + ' 条' : '') +
      (a.last_error ? ' · 错误：' + a.last_error : '')
    : '仅内存指标，JSONL 归档已关闭';

  reqEntries = entries || [];
  renderRequestTable();
}

/* reqMatch 请求记录筛选：q 对 IP/UA/模型/账号/请求 ID 做空格分词的 AND 包含匹配，
   outcome 精确匹配。两者都在已拉取的条目上做（最多 1000 条），不发新请求。 */
function reqMatch(e, f) {
  f = f || reqFilter;
  if (f.outcome && String(e && e.outcome || '') !== f.outcome) return false;
  if (f.q) {
    const text = [e && e.client_ip, e && e.user_agent, e && e.model, e && e.account, e && e.request_id]
      .filter(Boolean).join(' ').toLowerCase();
    for (const kw of f.q.toLowerCase().split(/\s+/).filter(Boolean)) {
      if (!text.includes(kw)) return false;
    }
  }
  return true;
}

function reqOutcomeTag(e) {
  const outcome = String(e && e.outcome || '');
  const label = { success: '成功', http_error: 'HTTP 错误', stream_error: '流错误', interrupted: '中断' }[outcome] || outcome || '—';
  const cls = outcome === 'success' ? 'ok'
    : outcome === 'interrupted' ? 'warn'
    : outcome ? 'bad' : 'mute';
  return '<span class="tag ' + cls + '">' + esc(String(e && e.status || '—') + ' ' + label) + '</span>';
}

function reqTokenCell(e) {
  const total = Number(e && e.total_tokens || 0) ||
    (Number(e && e.prompt_tokens || 0) + Number(e && e.completion_tokens || 0));
  return total ? fmtTok(total) : '—';
}

function reqCreditCell(e) {
  if (!e || !e.credit_known) return '<span class="muted">—</span>';
  const v = Number(e.credit);
  return Number.isFinite(v) ? trimFixed(v.toFixed(2)) : '<span class="muted">—</span>';
}

/* renderRequestTable 渲染请求记录表。来源列是这一版的重点：IP 用等宽字体方便扫，
   UA 单行截断（完整值在 title 里，行本身用 requestLogText 作 tooltip）。 */
/* 请求记录分页参数：每页渲染条数。1000 行 × 10 列全量渲染会卡面板（DOM 构建 +
   浏览器布局），而用户实际一次只看几十条。客户端分页：数据全量在内存（筛选/
   导出不受影响），渲染只画当前页。 */
/* 排序状态：默认按时间倒序（最新在上）。点列头同 key 切换升降序，换 key 默认降序。 */
/* 批量选中的 request_id 集合。 */

function updateSelButtons() {
  const n = selectedRids.size;
  const be = $('btnReqSelExport'), bc = $('btnReqSelClear');
  if (be) be.hidden = !n;
  if (bc) bc.hidden = !n;
  if (be) be.textContent = n ? '导出选中（' + n + '）' : '导出选中';
}
function syncSelectAll() {
  const sa = $('reqSelectAll');
  if (!sa) return;
  // 当前页所有可见 rid 是否都在 selectedRids 里。
  const rows = document.querySelectorAll('#reqBody .req-chk');
  if (!rows.length) { sa.checked = false; sa.indeterminate = false; return; }
  let sel = 0;
  rows.forEach(cb => { if (selectedRids.has(cb.dataset.rid)) sel++; });
  sa.checked = sel === rows.length;
  sa.indeterminate = sel > 0 && sel < rows.length;
}

/* reqSortValue 取排序键对应的可比值。数字列返回 Number，文字列返回字符串。 */
function reqSortValue(e, key) {
  if (!e) return 0;
  switch (key) {
    case 'time': return new Date(e.time || 0).getTime() || 0;
    case 'duration_ms': return Number(e.duration_ms || 0);
    case 'tokens': return Number(e.total_tokens || 0) ||
      (Number(e.prompt_tokens || 0) + Number(e.completion_tokens || 0));
    case 'credit': return (e.credit_known && Number.isFinite(Number(e.credit))) ? Number(e.credit) : -Infinity;
    case 'outcome': return e.outcome || '';
    default: return String(e[key] || '');
  }
}

function renderRequestTable() {
  let list = reqEntries.filter(e => reqMatch(e));
  // 排序：数字列数值比，文字列 localeCompare。
  list = list.slice().sort((a, b) => {
    const va = reqSortValue(a, reqSortKey), vb = reqSortValue(b, reqSortKey);
    if (typeof va === 'number' && typeof vb === 'number') return (va - vb) * reqSortDir;
    return String(va).localeCompare(String(vb)) * reqSortDir;
  });
  const tb = $('reqBody');
  if (!tb) return;
  // 分页：筛选结果可能变化（数量/排序），把当前页钳在有效范围内。
  const totalPages = Math.max(1, Math.ceil(list.length / REQ_PAGE_SIZE));
  if (reqPage > totalPages) reqPage = totalPages;
  if (reqPage < 1) reqPage = 1;
  const startIdx = (reqPage - 1) * REQ_PAGE_SIZE;
  const pageItems = list.slice(startIdx, startIdx + REQ_PAGE_SIZE);

  tb.innerHTML = pageItems.map(e => {
    const when = e && e.time ? new Date(e.time).toLocaleTimeString('zh-CN', { hour12: false }) : '—';
    const ip = e && e.client_ip ? e.client_ip : '';
    const ua = e && e.user_agent ? e.user_agent : '';
    const rid = e && e.request_id ? e.request_id : '';
    const onCls = (rid && rid === reqDetailRid) ? ' detail-on' : '';
    const chk = selectedRids.has(rid) ? ' checked' : '';
    return '<tr class="req-row' + onCls + '" data-rid="' + esc(rid) + '" tabindex="0"' +
      (onCls ? ' aria-expanded="true"' : ' aria-expanded="false"') +
      ' title="' + esc(requestLogText(e)) + '">' +
      '<td class="chk"><input type="checkbox" class="req-chk" data-rid="' + esc(rid) + '"' + chk + ' aria-label="选择行"></td>' +
      '<td class="num">' + esc(when) + '</td>' +
      '<td>' + reqOutcomeTag(e) + '</td>' +
      '<td>' + esc(e && e.model || '—') + '</td>' +
      '<td>' + esc(e && e.account || '—') + '</td>' +
      '<td>' + (ip ? '<span class="clip ip" title="' + esc(ip) + '">' + esc(ip) + '</span>' : '<span class="muted">—</span>') + '</td>' +
      '<td>' + (ua ? '<span class="clip" title="' + esc(ua) + '">' + esc(ua) + '</span>' : '<span class="muted">—</span>') + '</td>' +
      '<td class="num">' + fmtMs(e && e.duration_ms) + '</td>' +
      '<td class="num">' + reqTokenCell(e) + '</td>' +
      '<td class="num">' + reqCreditCell(e) + '</td>' +
      '<td>' + (rid ? '<span class="clip rid" title="' + esc(rid) + '">' + esc(rid) + '</span>' : '<span class="muted">—</span>') + '</td>' +
      '</tr>';
  }).join('') || '<tr><td colspan="11" class="empty">' +
      (reqEntries.length ? '没有符合当前筛选条件的请求记录' : '暂无请求记录') + '</td></tr>';

  // 行点击：展开/收起详情。点同一行收起，点其他行切换。
  // 键盘可达：行 tabindex="0" 后可聚焦，Enter/Space 展开（读屏/键盘用户不再只能摸鼠标）。
  // 只有「用户主动展开」才滚动详情进视口；轮询刷新（renderRequestTable → renderReqDetail）
  // 不滚动，避免打断阅读。
  function toggleReqDetail(rid, scroll) {
    reqDetailRid = (rid && rid === reqDetailRid) ? null : rid;
    renderRequestTable();
    renderReqDetail();
    if (scroll && reqDetailRid) {
      const host = $('reqDetail');
      if (host && !host.hidden) host.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
  }
  tb.querySelectorAll('tr.req-row').forEach(tr => {
    tr.onclick = (ev) => {
      // 点 checkbox 不触发展开详情。
      if (ev.target.classList.contains('req-chk')) return;
      toggleReqDetail(tr.dataset.rid || '', true);
    };
    tr.onkeydown = (ev) => {
      if (ev.key !== 'Enter' && ev.key !== ' ') return;
      if (ev.target.classList.contains('req-chk')) return;
      ev.preventDefault(); // 空格默认滚动页面，这里用来展开详情
      toggleReqDetail(tr.dataset.rid || '', true);
    };
  });

  // 行 checkbox：切换选中。
  tb.querySelectorAll('.req-chk').forEach(cb => {
    cb.onchange = () => {
      const rid = cb.dataset.rid || '';
      if (cb.checked) selectedRids.add(rid); else selectedRids.delete(rid);
      updateSelButtons();
      syncSelectAll();
    };
  });

  // 全选当前页。
  const sa = $('reqSelectAll');
  if (sa) {
    sa.onchange = () => {
      pageItems.forEach(e => {
        const rid = e && e.request_id || '';
        if (!rid) return;
        if (sa.checked) selectedRids.add(rid); else selectedRids.delete(rid);
      });
      renderRequestTable();
    };
  }
  syncSelectAll();

  // 分页器：>1 页才显示（单页时无噪音）。翻页只重画当前页，不重发请求。
  const pagerHost = $('reqPager');
  if (pagerHost) {
    pagerHost.innerHTML = reqPagerHtml(list.length, reqPage, totalPages);
    pagerHost.querySelectorAll('button[data-page]').forEach(b => {
      b.onclick = () => { reqPage = Number(b.dataset.page); renderRequestTable(); };
    });
  }

  const filtered = list.length !== reqEntries.length;
  // 归档里的旧条目没有来源字段（该功能上线前写入）：这时提示开关/历史原因，
  // 而不是让人以为筛选坏了。
  const hasSource = reqEntries.some(e => e && (e.client_ip || e.user_agent));
  $('reqCount').textContent = !reqEntries.length ? ''
    : (filtered ? '命中 ' + list.length + ' / ' + reqEntries.length + ' 条' : reqEntries.length + ' 条') +
      (totalPages > 1 ? ' · 第 ' + reqPage + '/' + totalPages + ' 页（每页 ' + REQ_PAGE_SIZE + ' 条）' : '') +
      (hasSource ? '' : ' · 来源未记录');
  $('reqCount').className = (filtered || !hasSource) ? 'note src-off' : 'note';

  // 列头箭头：当前排序列显示 ↑/↓，其他列清除。
  document.querySelectorAll('.acc.req thead th.sortable').forEach(th => {
    const k = th.dataset.sort;
    const old = th.querySelector('.arrow');
    if (old) old.remove();
    if (k === reqSortKey) {
      const s = document.createElement('span');
      s.className = 'arrow';
      s.textContent = reqSortDir === -1 ? '▼' : '▲';
      th.appendChild(s);
    }
  });

  // 轮询刷新后详情区跟着同步（条目可能被新数据冲掉，renderReqDetail 内部会自愈收起）。
  renderReqDetail();
}

/* thead 列头排序绑定：一次性绑定（thead 是静态 DOM，不随 renderRequestTable 重建）。 */

/* renderReqDetail 渲染请求详情抽屉。按 reqDetailRid 在全量 reqEntries 里定位
   （不是当前页切片——翻页后再切回仍能看到同一条）。找不到（被新数据冲掉）自动收起。 */
function renderReqDetail() {
  const host = $('reqDetail');
  if (!host) return;
  if (!reqDetailRid) { host.hidden = true; host.innerHTML = ''; return; }
  const e = reqEntries.find(x => x && x.request_id === reqDetailRid);
  if (!e) { reqDetailRid = null; host.hidden = true; host.innerHTML = ''; return; }

  const outcomeLabel = { success: '成功', http_error: 'HTTP 错误', stream_error: '流错误', interrupted: '中断' };
  const promptTok = Number(e.prompt_tokens || 0);
  const compTok = Number(e.completion_tokens || 0);
  const totalTok = Number(e.total_tokens || 0) || (promptTok + compTok);
  const hit = Number(e.cache_hit_tokens || 0), miss = Number(e.cache_miss_tokens || 0);
  const cacheRate = cacheRateText(hit, miss);
  const credit = (e && e.credit_known && Number.isFinite(Number(e.credit)))
    ? trimFixed(Number(e.credit).toFixed(2)) + ' credit' : '—';
  const item = (k, v) => '<div class="rd-item"><span class="rd-k">' + esc(k) + '</span>' +
    '<span class="rd-v">' + esc(v || '—') + '</span></div>';

  host.innerHTML =
    '<div class="rd-head"><b>' + esc(e.model || '—') + '</b>' +
    '<span class="note">' + esc((outcomeLabel[e.outcome] || e.outcome || '—') +
      (e.status != null ? ' · ' + e.status : '')) + '</span>' +
    '<button class="xs ghost rd-close" id="btnReqDetailClose">收起</button></div>' +
    '<div class="rd-grid">' +
    item('请求 ID', e.request_id) +
    item('完整时间', e.time ? new Date(e.time).toLocaleString('zh-CN', { hour12: false }) : '—') +
    item('账号', e.account) +
    item('来源 IP', e.client_ip) +
    item('User-Agent', e.user_agent) +
    item('耗时', fmtMs(e.duration_ms)) +
    item('Prompt tokens', String(promptTok)) +
    item('Completion tokens', String(compTok)) +
    item('Total tokens', String(totalTok)) +
    item('缓存命中', hit + ' / ' + (hit + miss) + (cacheRate !== '—' ? '（' + cacheRate + '）' : '')) +
    item('积分', credit) +
    '</div>';
  host.hidden = false;
  const closeBtn = $('btnReqDetailClose');
  if (closeBtn) closeBtn.onclick = () => { reqDetailRid = null; renderReqDetail(); renderRequestTable(); };
}

/* reqPagerHtml 生成分页器（当前页前后各 2 页窗口）。 */
function reqPagerHtml(total, page, totalPages) {
  if (totalPages <= 1) return '';
  const btn = (label, target, disabled, on) =>
    '<button class="xs' + (on ? ' primary' : '') + '"' +
    (disabled ? ' disabled' : ' data-page="' + target + '"') + '>' + esc(label) + '</button>';
  let nums = '';
  const winStart = Math.max(1, page - 2), winEnd = Math.min(totalPages, page + 2);
  if (winStart > 1) { nums += btn('1', 1, false, page === 1); if (winStart > 2) nums += '<span class="pg-ellipsis">…</span>'; }
  for (let p = winStart; p <= winEnd; p++) nums += btn(String(p), p, false, p === page);
  if (winEnd < totalPages) { if (winEnd < totalPages - 1) nums += '<span class="pg-ellipsis">…</span>'; nums += btn(String(totalPages), totalPages, false, page === totalPages); }
  return '<span class="pg">' +
    btn('‹', page - 1, page <= 1, false) +
    nums +
    btn('›', page + 1, page >= totalPages, false) +
    '</span>';
}

/* 请求记录筛选：搜索框防抖 150ms（最多 1000 行重渲染）。 */
/* 请求记录导出：导出当前筛选后的行（与屏幕所见一致）。 */
/* 导出选中：只导 selectedRids 里的行。 */
/* 清空选中：从内存移除选中行（不影响后端归档文件）。 */
// 时间范围：默认「全部历史」——请求记录页的历史行为就是"取最近 N 条"，
// 加一个默认收窄的区间会让打开页面时看到的条数凭空变少。

function requestLogText(e) {
  const when = e && e.time ? new Date(e.time).toLocaleTimeString('zh-CN', { hour12: false }) : '—';
  const outcomeLabel = { success: '成功', http_error: 'HTTP 错误', stream_error: '流错误', interrupted: '中断' };
  const token = Number(e && e.total_tokens || 0) ||
    (Number(e && e.prompt_tokens || 0) + Number(e && e.completion_tokens || 0));
  let credit = 'credit —';
  if (e && e.credit_known) {
    const value = Number(e.credit);
    if (Number.isFinite(value)) credit = String(Number(value.toFixed(2))) + ' credit';
  }
  return [
    when,
    String(e && e.status || '—') + ' ' + (outcomeLabel[e && e.outcome] || (e && e.outcome) || '—'),
    e && e.model || '—',
    e && e.account || '—',
    e && e.client_ip || '—',
    e && e.user_agent || '—',
    fmtMs(e && e.duration_ms),
    fmtTok(token) + ' tok',
    credit,
    cacheRateText(e && e.cache_hit_tokens, e && e.cache_miss_tokens) === '—' ? '' : '命中 ' + cacheRateText(e && e.cache_hit_tokens, e && e.cache_miss_tokens),
    e && e.request_id || '—',
  ].filter(Boolean).join(' | ');
}

/* 缓存命中率纯文本（issue #92）：requestLogText 与积分表/kpi 卡共用。
   自包含（不依赖 trimFixed）：前端纯函数切片测试只截取本段。 */
function cacheRateText(hit, miss) {
  const h = Number(hit || 0), m = Number(miss || 0), total = h + m;
  if (!total) return '—';
  return String(Math.round(h / total * 1000) / 10) + '%';
}

function fmtBytes(bytes) {
  const n = Number(bytes || 0);
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(1) + ' MB';
}

/* ── API Key 管理（二期，ADR-002）─────────────────────────────────── */
/* 独立视图 view-keys：卡片式列表 + 弹窗表单，复用 GET/POST /panel/api/config。
 * 一期 loadConfigMasked 已把 api_keys[].key 留空 + key_configured 状态字段；
 * mergeConfigMaps 对数组整体替换；normalizeAPIKeys 校验 id/realm/models。
 * 零新增后端接口，纯前端 + go:embed 重编译。
 *
 * UI 设计对齐竞品调研（panel-key-mgmt-ui-competitive-notes.md）：
 * - 卡片式列表（对齐 hub），不是表格行内编辑
 * - 弹窗表单新增/编辑（三家都用弹窗）
 * - 创建后明文一次性展示（对齐 manager，只存哈希）
 * - 胶囊式 realm 开关（对齐 hub），不是 select 下拉
 * - models 三态展示（对齐 ARP）：null=全部/[]=不放行/[a,b]=N个
 * - 空状态区分（对齐 manager）：取数失败≠没有密钥
 * - 提交锁（对齐 manager）：防连点建重复 */

// 明文缓存：创建/重置后本会话内可找回完整接入配置（刷新即清）。
// 服务端只存哈希，关弹窗后只有这里还能拿到明文，是「接入配置」弹窗状态 A 的依据。

function renderKeyList() {
  $('keyAdminStatus').textContent = '管理员密钥(api_key)：' + (keyAdminCfg ? '已配置' : '未配置');
  const list = $('keyBody');
  const empty = $('keyEmpty');
  const loadErr = $('keyLoadError');

  // 取数失败 ≠ 没有密钥（对齐 manager 防误重建）
  if (keyLoadFailed) {
    list.innerHTML = '';
    empty.hidden = true;
    loadErr.hidden = false;
    return;
  }
  loadErr.hidden = true;

  if (!keyData.length) {
    list.innerHTML = '';
    empty.hidden = false;
    return;
  }
  empty.hidden = true;
  list.innerHTML = '';
  keyData.forEach((k) => { list.insertAdjacentHTML('beforeend', keyCardHTML(k)); });
  attachKeyCardEvents();
}

// rpmLiveHTML F6：RPM 实况槽内容（数值 + 12 点迷你柱状曲线，纯 CSS 无依赖）。
// 曲线高度相对 max(限额, 历史峰值) 归一；history 尾元素 = 当前窗口（曲线连续）。
function rpmLiveHTML(s) {
  const hist = (Array.isArray(s.history) && s.history.length) ? s.history : [s.rpm];
  let peak = Math.max(s.limit || 0, 1);
  hist.forEach((v) => { if (v > peak) peak = v; });
  const bars = hist.map((v) =>
    `<i style="height:${Math.max(2, Math.round(18 * v / peak))}px" title="${v}"></i>`
  ).join('');
  return 'RPM ' + (typeof s.rpm === 'number' ? s.rpm : 0)
    + (s.limit ? '/' + s.limit : '')
    + `<span class="rpm-bars">${bars}</span>`;
}

// refreshRpmLive F6：overview.key_rpm 到达后就地刷新已渲染卡片的实况槽
// （不整表重渲、不重绑事件）；stats 早于卡片渲染时槽位缺失 → 随 config 重载补渲。
function refreshRpmLive(list) {
  rpmStatsById = {};
  if (Array.isArray(list)) list.forEach((s) => { if (s && s.key_id != null) rpmStatsById[s.key_id] = s; });
  document.querySelectorAll('.key-card').forEach((card) => {
    const slot = card.querySelector('[data-rpm-live]');
    if (!slot) return;
    const s = rpmStatsById[card.dataset.kid];
    if (!s) { slot.remove(); return; }
    const n = (Array.isArray(s.history) && s.history.length) ? s.history.length : 1;
    slot.title = '最近 ' + n + ' 分钟实际请求/分（含被限流）';
    slot.innerHTML = rpmLiveHTML(s);
  });
}

function keyCardHTML(k) {
  const id = esc(k.id || ''), name = esc(k.name || '') || '—';
  const cfg = !!k.key_configured;
  const en = k.enabled !== false;
  const realm = esc(k.realm || '');
  // F6：RPM 实况槽（overview.key_rpm 按原始 key_id 匹配；无观测时不渲染槽位，
  // stats 晚到后由 loadOverview 就地补渲或随 config 重载出现）。
  const rpmLive = (rpmStatsById && rpmStatsById[k.id])
    ? `<span class="key-badge rpm-live" data-rpm-live title="最近分钟实际请求/分（含被限流）">${rpmLiveHTML(rpmStatsById[k.id])}</span>`
    : '';

  // realm badge（胶囊式，对齐 hub）
  const realmBadge = realm === 'cn'
    ? '<span class="key-badge realm-cn">国内版</span>'
    : realm === 'global'
      ? '<span class="key-badge realm-global">国际版</span>'
      : '<span class="key-badge realm-all">不限</span>';

  // models 三态展示（对齐 ARP）
  let modelsBadge;
  if (k.models == null) {
    modelsBadge = '<span class="key-badge models-all">全部模型</span>';
  } else if (Array.isArray(k.models) && k.models.length === 0) {
    modelsBadge = '<span class="key-badge models-block">不放行</span>';
  } else if (Array.isArray(k.models)) {
    const n = k.models.length;
    const names = k.models.map(esc).join(', ');
    modelsBadge = `<span class="key-badge models-list" title="${names}">${n} 个模型</span>`;
  } else {
    modelsBadge = '<span class="key-badge models-all">全部模型</span>';
  }

  // 密钥状态（脱敏：只显示「已配置/未配置」，不显示明文）
  const keyStatus = cfg
    ? '<span class="tag ok">已配置</span>'
    : '<span class="tag mute">未配置</span>';

  // RPM 限制 badge（>0 时才显示）
  const rpmBadge = (typeof k.rpm === 'number' && k.rpm > 0)
    ? `<span class="key-badge models-list" title="每分钟最多 ${k.rpm} 次请求">${k.rpm} RPM</span>`
    : '';

  // 月配额 badge（>0 时才显示），带当月已用进度。
  const used = (monthlyData && typeof monthlyData[k.id] === 'number') ? monthlyData[k.id] : 0;
  const mqBadge = (typeof k.monthly_token_quota === 'number' && k.monthly_token_quota > 0)
    ? `<span class="key-badge models-list" title="当月已用 ${used.toLocaleString()} / 配额 ${k.monthly_token_quota.toLocaleString()} tokens（跨月自动重置）">${(used / 1e6).toFixed(2)} / ${(k.monthly_token_quota / 1e6).toFixed(1)}M tok/月</span>`
    : '';

  // 账号组 badge（非空才显示）
  const grpBadge = (typeof k.group === 'string' && k.group)
    ? `<span class="key-badge models-list" title="仅使用同组账号">组：${esc(k.group)}</span>`
    : '';

  return `<div class="key-card${en ? '' : ' disabled'}" data-kid="${id}">
    <div class="key-card-main">
      <div class="key-info">
        <span class="key-name">${name}</span>
        <span class="key-id">#${id}</span>
        ${keyStatus}
        ${realmBadge}
        ${modelsBadge}
        ${rpmBadge}
        ${rpmLive}
        ${mqBadge}
        ${grpBadge}
      </div>
      <div class="key-actions">
        <label class="switch" title="${en ? '已启用' : '已禁用'}">
          <input type="checkbox" data-action="toggle" data-kid="${id}" ${en ? 'checked' : ''}>
          <span class="switch-track"></span>
        </label>
        <button class="xs" data-action="importcfg" data-kid="${id}">接入配置</button>
        <button class="xs" data-action="edit" data-kid="${id}">编辑</button>
        <button class="xs danger" data-action="delete" data-kid="${id}">删除</button>
      </div>
    </div>
  </div>`;
}

function attachKeyCardEvents() {
  const list = $('keyBody');
  list.querySelectorAll('[data-action="toggle"]').forEach(el => {
    el.onchange = () => toggleKeyEnabled(el.dataset.kid, el.checked);
  });
  list.querySelectorAll('[data-action="importcfg"]').forEach(el => {
    el.onclick = () => openKeyCfg(el.dataset.kid);
  });
  list.querySelectorAll('[data-action="edit"]').forEach(el => {
    el.onclick = () => openKeyDialog(el.dataset.kid);
  });
  list.querySelectorAll('[data-action="delete"]').forEach(el => {
    el.onclick = () => deleteKey(el.dataset.kid);
  });
}

/* ── 弹窗表单（新增/编辑）────────────────────────────────────────── */

function openKeyDialog(id) {
  const k = id ? keyData.find(x => x.id === id) : null;
  keyEditing = k || null;
  keyIsNew = !k;

  $('keyDlgTitle').textContent = k ? '编辑 Key' : '新增 Key';
  $('keyDlgHint').textContent = k ? `编辑 Key「${k.name || k.id}」` : '配置一条新的 API Key';
  // 注意：input.value 是**纯文本**赋值，不能再过 esc()（HTML 转义）——
  // 曾在这里误用 esc()，导致名字含 & 的 Key 显示成 A&amp;B，保存写回后被二次
  // 转义成 A&amp;amp;B（越改越脏）；kfId 参与鉴权匹配，破坏性更大。
  // 拼进 innerHTML 的字符串（如卡片渲染）才需要 esc，见 keyCardHTML。
  $('kfName').value = k ? (k.name || '') : '';
  $('kfId').value = k ? (k.id || '') : '';
  $('kfId').disabled = !!k; // 编辑时 ID 不可改
  $('kfKey').value = '';
  $('kfKey').type = 'password';
  $('kfKeyHint').textContent = k
    ? '留空 = 不修改密钥；填写 = 更新为新密钥'
    : '必填；点击「生成」可一键生成随机密钥';
  $('kfModels').value = (k && Array.isArray(k.models)) ? k.models.join(', ') : '';
  $('kfRpm').value = (k && typeof k.rpm === 'number') ? k.rpm : 0;
  $('kfMonthlyQuota').value = (k && typeof k.monthly_token_quota === 'number') ? k.monthly_token_quota : 0;
  $('kfGroup').value = (k && typeof k.group === 'string') ? k.group : '';

  // realm 胶囊（默认不限）
  const realm = k ? esc(k.realm || '') : '';
  $('kfRealmPills').querySelectorAll('.realm-pill').forEach(p => {
    p.classList.toggle('on', p.dataset.realm === realm);
  });

  showVeil('keyEditVeil');
  if (!k) $('kfId').focus();
}

function closeKeyDialog() {
  hideVeil('keyEditVeil');
  keyEditing = null;
  keyIsNew = false;
}

function getCurrentRealm() {
  const on = $('kfRealmPills').querySelector('.realm-pill.on');
  return on ? on.dataset.realm : '';
}

function genApiKey() {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  const arr = new Uint8Array(32);
  crypto.getRandomValues(arr);
  let s = 'sk-';
  for (let i = 0; i < 32; i++) s += chars[arr[i] % chars.length];
  return s;
}

function collectKeyFromDialog() {
  const id = $('kfId').value.trim();
  if (!id) { toast('ID 不能为空', 'err'); $('kfId').focus(); return null; }
  if (!/^[A-Za-z0-9_-]+$/.test(id)) { toast('ID 只能含字母、数字、下划线、连字符', 'err'); $('kfId').focus(); return null; }

  const entry = { id };
  const name = $('kfName').value.trim();
  if (name) entry.name = name;

  const key = $('kfKey').value.trim();
  if (keyIsNew) {
    if (!key) { toast('新增 Key 时密钥必填', 'err'); $('kfKey').focus(); return null; }
    entry.key = key;
  } else if (key) {
    entry.key = key; // 编辑时填了新值才更新
  }

  const realm = getCurrentRealm();
  if (realm) entry.realm = realm;

  const models = $('kfModels').value.trim();
  if (models) {
    entry.models = models.split(/[,，\s]+/).filter(Boolean);
  } else if (keyIsNew) {
    entry.models = null; // 新增时不填 = 不限（null 三态）
  }

  const rpm = parseInt($('kfRpm').value, 10);
  entry.rpm = (isNaN(rpm) || rpm < 0) ? 0 : rpm;

  const mq = parseInt($('kfMonthlyQuota').value, 10);
  entry.monthly_token_quota = (isNaN(mq) || mq < 0) ? 0 : mq;

  const grp = $('kfGroup').value.trim();
  if (grp) entry.group = grp;

  return entry;
}

function buildSavePayload(newEntry) {
  // 构建完整 api_keys 数组提交（对齐现有 POST /panel/api/config 整体替换语义）
  const out = [];
  let replaced = false;
  keyData.forEach((k) => {
    if (newEntry && k.id === newEntry.id) {
      // 编辑：合并更新（保留原有字段，覆盖提交的字段）
      const merged = { id: k.id };
      if (k.name) merged.name = k.name;
      if (newEntry.name) merged.name = newEntry.name;
      if (newEntry.key) merged.key = newEntry.key;
      else if (k.key) merged.key = k.key;
      if (newEntry.realm !== undefined) merged.realm = newEntry.realm;
      else if (k.realm) merged.realm = k.realm;
      if (newEntry.models !== undefined) merged.models = newEntry.models;
      else if (Array.isArray(k.models)) merged.models = k.models;
      merged.rpm = (typeof newEntry.rpm === 'number') ? newEntry.rpm : (k.rpm || 0);
      merged.monthly_token_quota = (typeof newEntry.monthly_token_quota === 'number') ? newEntry.monthly_token_quota : (k.monthly_token_quota || 0);
      if (newEntry.group !== undefined) merged.group = newEntry.group;
      else if (k.group) merged.group = k.group;
      merged.enabled = k.enabled !== false;
      out.push(merged);
      replaced = true;
    } else {
      // 保留其他 Key 原样
      const entry = { id: k.id, enabled: k.enabled !== false };
      if (k.name) entry.name = k.name;
      if (k.key) entry.key = k.key;
      if (k.realm) entry.realm = k.realm;
      if (Array.isArray(k.models)) entry.models = k.models;
      entry.rpm = k.rpm || 0;
      entry.monthly_token_quota = k.monthly_token_quota || 0;
      if (k.group) entry.group = k.group;
      out.push(entry);
    }
  });
  if (newEntry && !replaced) out.push(newEntry); // 新增
  return out;
}

async function saveKeyFromDialog() {
  if (keySaving) return; // 提交锁：防连点建重复
  const entry = collectKeyFromDialog();
  if (!entry) return;

  keySaving = true;
  try {
    const payload = buildSavePayload(entry);
    const r = await api('config', { method: 'POST', body: JSON.stringify({ api_keys: payload }) });
    toast('Key 配置已保存，热生效', 'ok');

    // 新增且有明文返回 → 展示一次性明文弹窗
    if (keyIsNew && entry.key) {
      closeKeyDialog();
      showKeyPlaintext(entry.key, entry.id);
    } else {
      closeKeyDialog();
    }
    loadKeys();
  } catch (e) {
    toast('保存失败：' + e.message, 'err');
  } finally {
    keySaving = false;
  }
}

async function toggleKeyEnabled(id, enabled) {
  if (keySaving) return;
  if (toggleKeyEnabled.busy) return; toggleKeyEnabled.busy = true;
  const orig = keyData.find(k => k.id === id), origEn = orig && orig.enabled;
  if (orig) { orig.enabled = enabled; renderKeyList(); }
  const keys = keyData.map(k => { const e = { id: k.id, enabled: k.enabled !== false };
    ['name','key','realm'].forEach(f => { if (k[f]) e[f] = k[f]; }); if (k.models) e.models = k.models; return e; });
  try { await api('config', { method: 'POST', body: JSON.stringify({ api_keys: keys }) }); toast(`Key「${id}」已${enabled ? '启用' : '禁用'}，热生效`, 'ok'); }
  catch (e) { if (orig) { orig.enabled = origEn; renderKeyList(); } toast('切换失败：' + e.message, 'err'); }
  loadKeys(); toggleKeyEnabled.busy = false; }

function deleteKey(id) {
  const k = keyData.find(x => x.id === id);
  const label = (k && (k.name || k.id)) || id;
  if (!confirm(`确认删除 Key「${label}」？\n\n删除后立即生效，该 Key 将无法再用于鉴权。`)) return;
  const remaining = keyData.filter(x => x.id !== id);
  const payload = remaining.length ? remaining.map(k => {
    const entry = { id: k.id, enabled: k.enabled !== false };
    if (k.name) entry.name = k.name;
    if (k.key) entry.key = k.key;
    if (k.realm) entry.realm = k.realm;
    if (Array.isArray(k.models)) entry.models = k.models;
    return entry;
  }) : null;
  saveKeys(payload, `Key「${label}」已删除，热生效`);
}

async function saveKeys(keys, successMsg) {
  if (keySaving) return;
  keySaving = true;
  try {
    await api('config', { method: 'POST', body: JSON.stringify({ api_keys: keys }) });
    toast(successMsg || 'Key 配置已保存，热生效', 'ok');
    loadKeys();
  } catch (e) { toast('保存失败：' + e.message, 'err'); }
  finally { keySaving = false; }
}

/* ── API Key 明文展示弹窗已拆到 app_views_keys.js（2026-11-02 组件库拆分第 4 步）──────────
   只放函数声明、先于 app.js 加载。 */
