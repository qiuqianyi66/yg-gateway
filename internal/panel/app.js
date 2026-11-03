'use strict';
/* ── 状态 ─────────────────────────────────────────────────────────── */
const LS_KEY = 'yg-gateway.key', LS_THEME = 'yg-gateway.theme';
let theme = localStorage.getItem(LS_THEME) || 'auto';   // auto | light | dark
let view = 'accounts';
let overviewData = null, cfgLoaded = null;
let logPin = true, loginState = null, loginTimer = null;
let refTimer = null;
/* 视图级筛选状态（模块级声明放在文件顶部，避免顶层 go() 早于声明执行时踩 TDZ）。 */
let mdFilter = { q: '', realm: '', cap: '', effort: '', promo: '', sort: 'default' };
let mdAll = [], mdProbes = {}, mdProbeOf = () => undefined;
let reqFilter = { q: '', outcome: '' };
let reqEntries = [];
let usDim = 'account', usCreditDim = 'account', usSort = 'total';
let usageData = null;
let reqRangeState = null; // 请求记录的时间范围（用量页的见 trangeState）

const $ = id => document.getElementById(id);

/* ── 主题 ─────────────────────────────────────────────────────────── */
/* 两态翻转（浅/深），首次访问跟随系统偏好；点击总是切换可见外观，符合直觉。 */
function effTheme() {
  return theme === 'auto' ? (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark') : theme;
}
function applyTheme() {
  const eff = effTheme();
  document.documentElement.dataset.theme = eff;
  $('icoTheme').innerHTML = eff === 'light'
    ? '<circle cx="8" cy="8" r="3"/><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3.2 3.2l1.4 1.4M11.4 11.4l1.4 1.4M12.8 3.2l-1.4 1.4M4.6 11.4l-1.4 1.4"/>'
    : '<path d="M13.2 9.6A5.6 5.6 0 0 1 6.4 2.8a5.6 5.6 0 1 0 6.8 6.8z"/>';
  $('btnTheme').title = eff === 'light' ? '切换到深色' : '切换到浅色';
}
/* 主题切换：只在「跟随系统」时响应系统偏好变化。
   此前是 window 上的 `addEventListener('change', applyTheme)` —— 页面上**任何**
   change 事件（每个下拉、每个 checkbox、每个时间输入）都会冒泡到 window 触发
   applyTheme()，而它会重写 #icoTheme 的 innerHTML。配置页有 40+ 控件，每次改动
   都做一次无谓的 DOM 重写。 */
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
  if (theme === 'auto') applyTheme();
});
$('btnTheme').onclick = () => {
  theme = effTheme() === 'light' ? 'dark' : 'light';
  localStorage.setItem(LS_THEME, theme);
  applyTheme();
};
applyTheme();

/* ── 请求 ─────────────────────────────────────────────────────────── */
/* api 面板后端调用。opts 额外支持 signal（AbortSignal）——轮询场景下新一轮开始
   时 abort 掉上一轮未完成的请求，避免「慢响应后到覆盖新响应」。
   典型：/packages 逐账号查上游偶慢（8s），而 5s 轮询的第二轮已返回（1s）——
   没有取消机制时，第一轮的旧数据会覆盖第二轮的新数据。 */
async function api(path, opts = {}) {
  const h = Object.assign({}, opts.headers || {});
  const k = localStorage.getItem(LS_KEY);
  if (k) h['Authorization'] = 'Bearer ' + k;
  if (opts.body) h['Content-Type'] = 'application/json';
  const r = await fetch('/panel/api/' + path, Object.assign({}, opts, { headers: h }));
  if (r.status === 401) { openKey(); throw new Error('密钥无效或未填写'); }
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
  return d;
}
/* abortable 为某类请求维护「最新一轮」的 AbortController：
   调用返回 {signal, done()}，下次同 key 调用会先 abort 上一次。
   用法：
     const ctl = abortable('overview');
     try { const d = await api('overview', { signal: ctl.signal }); ... }
     finally { ctl.done(); }
   注意：被 abort 的请求会抛 AbortError —— 调用方应把 AbortError 当成"本轮作废"
   直接 return（不要 toast 报错）。isAbort 用于统一判断。 */
const abortCtl = new Map();
function abortable(key) {
  const prev = abortCtl.get(key);
  if (prev) prev.ctrl.abort();
  const ctrl = new AbortController();
  const entry = { ctrl, signal: ctrl.signal };
  abortCtl.set(key, entry);
  return {
    signal: ctrl.signal,
    done() { if (abortCtl.get(key) === entry) abortCtl.delete(key); },
  };
}
function isAbort(e) {
  return !!e && (e.name === 'AbortError' || /aborted|abort/i.test(String(e.message || '')));
}
/* toast 操作结果提示。三条改进（深度优化轮 前端 P1）：
   ① 堆叠上限 5 条：连续批量操作此前会把右下角叠满、且无手动关闭；
   ② 错误类停留 8s（成功 3.6s）：报错还没读完就消失是常见抱怨；
   ③ 加手动关闭 ×：长文案/想先复制时不用等自动消失。
   #toasts 已加 aria-live="polite"（见 index.html），读屏能播报结果。 */
function toast(msg, cls) {
  const host = $('toasts');
  const el = document.createElement('div');
  el.className = 'tst ' + (cls || '');
  // 状态图标（游戏化层）：成功/失败配 SVG 形符，aria-hidden 纯装饰；颜色只是第二通道。
  const ic = document.createElement('span');
  ic.className = 'tst-ic';
  ic.setAttribute('aria-hidden', 'true');
  ic.innerHTML = cls === 'err'
    ? '<svg viewBox="0 0 14 14" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M3 3l8 8M11 3l-8 8"/></svg>'
    : cls === 'ok'
      ? '<svg viewBox="0 0 14 14" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M2.5 7.5 5.5 10.5 11.5 3.5"/></svg>'
      : '<svg viewBox="0 0 14 14" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="7" cy="7" r="5"/></svg>';
  el.appendChild(ic);
  const txt = document.createElement('span');
  txt.textContent = msg;
  el.appendChild(txt);
  const x = document.createElement('button');
  x.className = 'tst-x';
  x.type = 'button';
  x.setAttribute('aria-label', '关闭提示');
  x.textContent = '×';
  x.onclick = () => el.remove();
  el.appendChild(x);
  host.appendChild(el);
  // 超上限先挤掉最旧的（FIFO），避免叠满右下角。
  while (host.children.length > 5) host.firstChild.remove();
  setTimeout(() => el.remove(), cls === 'err' ? 8000 : 3600);
  // 同步写入持久化通知中心（错误/成功才记，普通 info 不刷）。
  if (cls === 'err' || cls === 'ok') pushNotif(msg, '', cls === 'err' ? 'error' : 'success');
}

/* ── 通知中心：localStorage 持久化，跨刷新保留 ──────────────────────── */
const NOTIF_KEY = 'yg_notifs_v1';
function getNotifs() { try { return JSON.parse(localStorage.getItem(NOTIF_KEY)) || []; } catch { return []; } }
function setNotifs(n) { try { localStorage.setItem(NOTIF_KEY, JSON.stringify(n.slice(-80))); } catch {} }
function pushNotif(title, body, type = 'info') {
  const n = getNotifs();
  n.unshift({ id: Date.now() + Math.random(), title, body, type, ts: Date.now(), read: false });
  setNotifs(n);
  renderNotifBadge();
}
function renderNotifBadge() {
  const n = getNotifs();
  const unread = n.filter(x => !x.read).length;
  const b = $('notifBadge');
  if (unread > 0) { b.hidden = false; b.textContent = unread > 9 ? '9+' : String(unread); }
  else b.hidden = true;
}
function renderNotifList() {
  const n = getNotifs();
  $('notifList').innerHTML = n.length ? n.map(x => {
    const color = x.type === 'error' ? 'var(--bad)' : x.type === 'success' ? 'var(--ok)' : 'var(--accent)';
    return `<div class="notif-item ${x.read ? '' : 'unread'}" data-id="${x.id}">
      <div style="display:flex;align-items:center;gap:6px"><i style="width:6px;height:6px;border-radius:50%;background:${color};flex:none"></i><span>${x.title}</span></div>
      ${x.body ? `<div style="color:var(--ink-3);font-size:12px;margin-top:3px">${x.body}</div>` : ''}
      <div class="notif-time">${new Date(x.ts).toLocaleString()}</div>
    </div>`;
  }).join('') : '<div style="padding:28px;text-align:center;color:var(--ink-3);font-size:13px">暂无通知</div>';
}
function initNotifCenter() {
  renderNotifBadge();
  $('btnNotif').onclick = () => {
    const p = $('notifPanel');
    p.hidden = !p.hidden;
    if (!p.hidden) {
      renderNotifList();
      const n = getNotifs();
      n.forEach(x => x.read = true);
      setNotifs(n);
      renderNotifBadge();
    }
  };
  $('btnNotifClear').onclick = (e) => { e.stopPropagation(); setNotifs([]); renderNotifList(); renderNotifBadge(); };
  // 全部已读：与打开面板的自动清读同一操作，给用户一个不依赖打开面板的显式入口。
  $('btnNotifReadAll').onclick = (e) => {
    e.stopPropagation();
    const n = getNotifs();
    n.forEach(x => x.read = true);
    setNotifs(n);
    renderNotifList();
    renderNotifBadge();
  };
  // 点外部关闭面板。
  document.addEventListener('click', (e) => {
    if (!e.target.closest('#notifPanel') && !e.target.closest('#btnNotif')) $('notifPanel').hidden = true;
  });
}
/* ── CSV / JSON 导出 ─────────────────────────────────────────────────
   运维面板的常见诉求：把这张表导出去对账（进 Excel 做透视、按 Key 拆账、
   留档）。此前面板完全没有导出能力（无 Blob、无 download）。
   实现要点：
     · BOM（\ufeff）必须加——否则 Excel 打开中文列名会乱码（GBK 环境）；
     · 字段值统一走 csvCell 转义（含逗号/引号/换行时用双引号包裹并把内部 " 翻倍，
       对齐 RFC 4180）；
     · URL.revokeObjectURL 延迟回收，避免大文件下载被提前取消。 */
/* 已拆到 app_core.js（2026-11-02 组件库拆分第 3 步）：csvCell */
function exportCSV(headers, rows, filename) {
  const lines = [headers.map(csvCell).join(',')];
  for (const r of rows) lines.push(r.map(csvCell).join(','));
  // BOM 让 Excel 正确识别 UTF-8 中文
  const blob = new Blob(['\ufeff' + lines.join('\r\n')], { type: 'text/csv;charset=utf-8' });
  downloadBlob(blob, filename);
}
function exportJSON(obj, filename) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json;charset=utf-8' });
  downloadBlob(blob, filename);
}
function downloadBlob(blob, filename) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // 延迟回收：立即 revoke 会让部分浏览器取消下载。
  setTimeout(() => URL.revokeObjectURL(url), 4000);
}
// 导出文件名带时间戳，多次导出不互相覆盖。
function exportStamp() {
  const d = new Date(), p = n => String(n).padStart(2, '0');
  return d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' + p(d.getHours()) + p(d.getMinutes());
}

/* ── 骨架屏 / 加载态 / 就地错误态 ────────────────────────────────────
   与 index.html 的 .skel / .load-error 样式配套。
   动机：加载态此前是纯文本（"正在向上游查询…"），而逐账号查上游可能几十秒——
   纯文本空白期看起来像卡死；失败时又只有一行字，要重试还得滚回顶部找刷新按钮。 */
function skelRows(n, cols) {
  let out = '';
  for (let i = 0; i < n; i++) {
    let cells = '';
    for (let c = 0; c < cols; c++) cells += '<span class="skel w-' + ((c % 4) + 1) + '"></span>';
    out += '<div class="skel-row">' + cells + '</div>';
  }
  return out;
}
function skelCards(n) {
  let out = '';
  for (let i = 0; i < n; i++) {
    out += '<div class="skel-card"><div class="skel"></div><div class="skel"></div>' +
      '<div class="skel" style="width:60%"></div></div>';
  }
  return '<div class="skel-grid">' + out + '</div>';
}
function skelKpis(n) {
  let out = '';
  for (let i = 0; i < n; i++) out += '<div class="skel"></div>';
  return '<div class="skel-kpi">' + out + '</div>';
}
/* 带转圈的加载提示（比裸文本多一个 spinner，明确"在动"）。 */
function loadingMsg(text) {
  return '<div class="skel-msg"><span class="spinner"></span><span>' + esc(text) + '</span></div>';
}
/* 就地错误块：原因 + 重试按钮（data-retry 由各视图的委托监听处理）。 */
function loadErrorHTML(title, detail, retryLabel) {
  return '<div class="load-error"><div class="big">' + esc(title) + '</div>' +
    (detail ? '<div class="detail">' + esc(detail) + '</div>' : '') +
    '<button class="xs" data-retry="1">' + esc(retryLabel || '重试') + '</button></div>';
}

/* 已拆到 app_core.js（2026-11-02 组件库拆分第 3 步）：esc / ago / dur / parseAPITime / fmtLocalDateTime */

/* ── 时间范围控件（用量 / 请求记录共用）────────────────────────────────
   区间由前端算好再发：「今天」必须按浏览器本地时区 00:00 起（服务端时区未必一致）；
   滚动预设（近 N 小时/天）保留 hours 参数（服务端整点对齐，前端自己减会算错边界）。 */
const TRANGE_PRESETS = [
  ['today', '今天'],
  ['24', '近 24 小时'],
  ['72', '近 3 天'],
  ['168', '近 7 天'],
  ['720', '近 30 天'],
  ['0', '全部历史'],
  ['custom', '自定义…'],
];
const TRANGE_DEFAULT = '72';
const trangeStates = new Map(); // hostId → { preset, from: Date|null, to: Date|null }

/* 已拆到 app_core.js（2026-11-02 组件库拆分第 3 步）：dtLocalValue / dtLocalParse */

// trangeMidnight 今天 00:00（本地时区）。
function trangeMidnight() {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  return d;
}

function trangeState(id) {
  if (!trangeStates.has(id)) {
    // 「自定义」的初始值给一段有意义的默认：今天 00:00 → 现在。
    trangeStates.set(id, { preset: TRANGE_DEFAULT, from: trangeMidnight(), to: new Date() });
  }
  return trangeStates.get(id);
}

// trangeRender 画出控件骨架（幂等：重复调用会保留当前状态）。
function trangeRender(id) {
  const host = $(id);
  if (!host) return;
  const st = trangeState(id);
  const custom = st.preset === 'custom';
  host.innerHTML =
    '<select class="tr-preset" aria-label="时间范围">' +
    TRANGE_PRESETS.map(([v, label]) =>
      '<option value="' + v + '"' + (v === st.preset ? ' selected' : '') + '>' + esc(label) + '</option>').join('') +
    '</select>' +
    '<span class="tr-custom"' + (custom ? '' : ' hidden') + '>' +
    '<input type="datetime-local" class="tr-from" value="' + esc(st.from ? dtLocalValue(st.from) : '') + '" aria-label="起始时间">' +
    '<span class="tr-sep">→</span>' +
    '<input type="datetime-local" class="tr-to" value="' + esc(st.to ? dtLocalValue(st.to) : '') + '" aria-label="结束时间">' +
    '</span>';
  const preset = host.querySelector('.tr-preset');
  if (preset) preset.onchange = () => {
    st.preset = preset.value;
    // 从别的预设切到自定义时，把区间重置为"今天 00:00 → 现在"，
    // 免得用户上次留下的半年区间被无声沿用。
    if (st.preset === 'custom' && (!st.from || !st.to)) { st.from = trangeMidnight(); st.to = new Date(); }
    trangeRender(id);
    trangeEmit(id);
  };
  const fromEl = host.querySelector('.tr-from');
  const toEl = host.querySelector('.tr-to');
  const readCustom = () => {
    st.from = dtLocalParse(fromEl.value);
    st.to = dtLocalParse(toEl.value);
    // 起止颠倒就地标红（不静默纠正：用户可能正输到一半）。
    const bad = st.from && st.to && st.from > st.to;
    fromEl.classList.toggle('tr-bad', !!bad);
    toEl.classList.toggle('tr-bad', !!bad);
    if (bad) return;
    trangeEmit(id);
  };
  if (fromEl) fromEl.onchange = readCustom;
  if (toEl) toEl.onchange = readCustom;
}

const trangeHandlers = new Map();
// trangeBind 渲染控件并登记变化回调。**不**在绑定时触发回调：各视图的首次加载
// 由 go() 统一驱动，这里再触发一次会让打开页面时打两遍接口。
function trangeBind(id, onChange, preset) {
  trangeHandlers.set(id, onChange);
  if (preset) trangeState(id).preset = preset;
  trangeRender(id);
}
function trangeEmit(id) {
  const fn = trangeHandlers.get(id);
  if (fn) fn();
}

// trangeQuery 把当前选择翻译成查询参数。
//   rolling=true  → 滚动预设发 hours（服务端整点对齐），今天/自定义发 from/to
//   rolling=false → 一律发 from/to（归档是线性日志，前端算区间更直观）
// 「全部历史」两者都不发。
function trangeQuery(id, rolling) {
  const st = trangeState(id);
  const q = new URLSearchParams();
  const sec = d => Math.floor(d.getTime() / 1000);
  if (st.preset === 'custom') {
    if (st.from) q.set('from', sec(st.from));
    if (st.to) q.set('to', sec(st.to));
    return q;
  }
  if (st.preset === 'today') {
    q.set('from', sec(trangeMidnight()));
    return q;
  }
  // 全部历史：滚动端点（用量）必须**显式**传 hours=0。
  //
  // 后端对「什么都不给」的缺省是 72 小时（见 panel.go 的说明：
  // 「都不给：等同于 hours=72（保持旧调用方行为）」），所以这里返回空 query 会被
  // 当成「近 3 天」—— 正是 issue #121 报的现象：选了「全部历史」，数字却和
  // 「近 3 天」一模一样。
  //
  // 非滚动端点（请求记录）没有缺省窗口：不传 from/to 即"不限起点"，保持空 query。
  if (st.preset === '0') {
    if (rolling) q.set('hours', '0');
    return q;
  }
  if (rolling) { q.set('hours', st.preset); return q; }
  q.set('from', sec(new Date(Date.now() - Number(st.preset) * 3600 * 1000)));
  return q;
}

// trangeLabel 人读口径，用于「用量总览」右上角这类需要回显区间的位置。
function trangeLabel(id) {
  const st = trangeState(id);
  const found = TRANGE_PRESETS.find(p => p[0] === st.preset);
  if (st.preset !== 'custom') return found ? found[1] : '';
  if (!st.from && !st.to) return '自定义';
  const f = d => d ? (d.getMonth() + 1) + '-' + String(d.getDate()).padStart(2, '0') + ' ' +
    String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0') : '…';
  return f(st.from) + ' → ' + f(st.to);
}
function rateLimitMeta(row, now) {
  const model = String(row && row.model || '未知模型');
  const kind = String(row && row.kind || 'rate_limit');
  const resetAt = parseAPITime(row && row.reset_at);
  const until = parseAPITime(row && row.until);
  const deadline = resetAt || until;
  const remaining = deadline > now ? Math.round((deadline - now) / 1000) : 0;
  if (kind === 'model_unavailable') {
    return {
      model,
      kind,
      detail: remaining ? '预计 ' + dur(remaining) + ' 后重试' : '等待重新探测',
      title: model + '\n模型当前不可用' + (deadline ? '\n最早重试：' + fmtLocalDateTime(deadline) : ''),
    };
  }
  let detail = resetAt
    ? '预计 ' + fmtLocalDateTime(resetAt) + ' 解封' + (remaining ? '（剩余 ' + dur(remaining) + '）' : '')
    : (until ? '预计 ' + fmtLocalDateTime(until) + ' 恢复（剩余 ' + dur(remaining) + '）' : '预计解封时间未知');
  const title = [model, resetAt ? '上游重置：' + fmtLocalDateTime(resetAt) : '上游重置：时间未知'];
  if (until && resetAt && until < resetAt) {
    detail += ' · 网关最快 ' + dur(Math.max(0, Math.round((until - now) / 1000))) + ' 后重试';
    title.push('网关最早重试：' + fmtLocalDateTime(until));
  }
  return { model, kind, detail, title: title.join('\n') };
}
function rateLimitRowsHtml(rows, now) {
  const list = Array.isArray(rows) ? rows.filter(row => row && row.model) : [];
  if (!list.length) return '';
  return '<div class="rate-limits">' + list.map(row => {
    const m = rateLimitMeta(row, now);
    return '<div class="rate-limit ' + (m.kind === 'model_unavailable' ? 'model-unavailable' : '') +
      '" title="' + esc(m.title) + '"><b>' + esc(m.model) + '</b><span>' + esc(m.detail) + '</span></div>';
  }).join('') + '</div>';
}

/* 已拆到 app_core.js（2026-11-02 组件库拆分第 3 步）：formatTokenCount / formatLatency / formatRate */

/* ── 密钥门 ───────────────────────────────────────────────────────── */
function openKey() { $('keyVeil').classList.add('on'); setTimeout(() => $('keyInput').focus(), 60); }
$('btnKey').onclick = async () => {
  const v = $('keyInput').value.trim();
  if (!v) return;
  localStorage.setItem(LS_KEY, v);
  try {
    await api('overview');
    $('keyErr').hidden = true;
    $('keyVeil').classList.remove('on');
    start();
  } catch (e) { $('keyErr').hidden = false; }
};
$('keyInput').addEventListener('keydown', e => { if (e.key === 'Enter') $('btnKey').click(); });
// 密钥门表单回车提交：拦住默认行为（刷新页面会丢 localStorage 密钥、回到 401 循环）。
// 原 onsubmit="return false" 内联属性被 CSP script-src 'self' 拦截失效，改为这里统一处理。
$('keyGateForm').addEventListener('submit', e => { e.preventDefault(); $('btnKey').click(); });

/* ── 路由 ─────────────────────────────────────────────────────────── */
/* 积分包明细共享缓存：「积分构成」视图与首页「积分到期提醒」卡片共用同一份
   /panel/api/packages 数据（逐账号实时查上游，能省一次是一次）。声明在路由区
   是因为 go() 的初始调用就会触发首页卡片的渲染，必须先于它就位。 */
let lastPackages = null;
let lastPackagesAt = 0;
let expFetching = false;                       // 到期卡片在途标记（防重复打上游）
const EXP_FRESH_MS = 2 * 60 * 1000;            // 缓存新鲜窗口：2 分钟内复用

const TITLES = { accounts: '账号池', usage: '用量', packages: '积分构成', taskscenter: '任务中心', models: '模型与档位', keys: 'API Key 管理', config: '配置', logs: '运行日志', opsagent: '运维 Agent' };

/* cfgDirty 配置页未保存标记。
   为什么需要：go() 进配置页会无条件 loadConfig() 重拉并整体回填表单。用户改了
   若干字段后点别的导航，再点回来，改动被静默覆盖（savebar 的「放弃修改」是显式
   入口，导航却是隐式入口却同效）——这是"配置页改了半天全没了"的根因。
   置位：表单任意 input/change（见 CFG_MAP 绑定处）；清位：loadConfig 回填后、
   保存成功后。 */
let cfgDirty = false;
function markCfgDirty() {
  if (cfgDirty) return;
  cfgDirty = true;
  const n = $('cfgNote');
  if (n) { n.textContent = '● 有未保存的改动'; n.classList.add('cfg-dirty'); }
}
function clearCfgDirty() {
  cfgDirty = false;
  const n = $('cfgNote');
  if (n) { n.textContent = ''; n.classList.remove('cfg-dirty'); }
}

/* go 切换视图。skipGuard=true 用于初始深链（无用户改动可丢）。
   返回 true 表示切换完成、false 表示被守卫拦下（用户在配置页有未保存改动）。 */
function go(v, skipGuard) {
  if (!skipGuard && view === 'config' && v !== 'config' && cfgDirty) {
    if (!confirm('配置页有未保存的改动，切换视图会丢弃它们。确认离开？')) return false;
    clearCfgDirty();
  }
  view = v;
  document.querySelectorAll('.view').forEach(s => s.hidden = s.id !== 'view-' + v);
  document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('on', a.dataset.view === v));
  $('ttl').textContent = TITLES[v];
  // 切视图回顶：从很长的页面（积分构成/模型表）切到别的视图，若沿用旧滚动位置
  // 会停在页面中部，看起来像"页面坏了"。初始深链由调用方传 skipGuard，此处只
  // 在用户主动切换时回顶。
  if (!skipGuard) window.scrollTo({ top: 0 });
  if (v === 'models' && !$('mdBody').children.length) loadModels();
  if (v === 'keys') loadKeys();
  if (v === 'config') loadConfig();
  if (v === 'logs') loadLogs();
  if (v === 'usage') loadUsage();
  if (v === 'packages') loadPackages();
  // 到期提醒卡片：默认可见，进页面即用 overview 快照渲染（零上游请求，随首页统计
  // 每轮刷新）。不再「打开账号池就打上游」——逐账号实时查在账号多时会卡死面板。
  // 完整逐包明细由卡片上的「检查」按钮显式触发（loadPackages/loadExpiry）。
  if (v === 'taskscenter') reattachQueueView();
  return true;
}

/* 导航点击：用 pushState 写 hash，让浏览器前进/后退真正可用。
   此前只 replaceState，历史里没有记录 → 按返回键直接离开面板（用户报"回不去"）。 */
document.querySelectorAll('.nav a').forEach(a => a.onclick = e => {
  e.preventDefault();
  if (go(a.dataset.view) === false) return; // 被配置页守卫拦下：不改 hash
  history.pushState(null, '', '#' + a.dataset.view);
});
/* hashchange：浏览器前进/后退、手改地址栏 hash 都走这里。
   与导航点击去重：点击已 pushState 过，hashchange 只在 hash 真的变化时触发，
   且此时 view 已等于目标 → 直接 return，不会重复 go()。 */
addEventListener('hashchange', () => {
  const h = (location.hash || '#accounts').slice(1);
  if (!(h in TITLES) || h === view) return;
  go(h); // 注意：此处改动不应再写 hash（已在 URL 里）
});

/* 首次进入延到本轮脚本求值之后再 go()。
   原因：go() 会同步触发视图的数据加载（loadUsage/loadLogs/loadPackages…），而这些
   函数读到的模块级 let/const（usageRateWarmAt、PK_* 等）在文件后半段才初始化——
   直接深链 #usage / #packages 打开页面时会踩 TDZ（"Cannot access 'x' before
   initialization"），表现为该页永远显示"读取失败"，而点导航进去一切正常。
   延迟 0ms 让整份脚本先求值完，是修这一类问题最省事也最不容易再犯的办法。 */
setTimeout(() => {
  const hash = (location.hash || '#accounts').slice(1);
  go(hash in TITLES ? hash : 'accounts', true); // skipGuard：初始进入无改动可丢，且不回顶
}, 0);

/* ── 账号池视图已拆到 app_views_accounts.js（2026-11-02 组件库拆分第 4 步）────────────
   只放函数声明、先于 app.js 加载；顶层事件绑定与签名状态仍留在下方。 */

// 模型筛选控件的顶层绑定（从 app_views_accounts.js 搬回）：引用 $，必须留在 app.js。
// 筛选控件：输入框防抖 120ms（长列表逐字符重排不必每键一次），下拉即时。
for (const [id, key] of [['mdRealm', 'realm'], ['mdCap', 'cap'], ['mdEffort', 'effort'], ['mdPromo', 'promo'], ['mdSort', 'sort']]) {
  const el = $(id);
  if (!el) continue;
  el.onchange = () => { mdFilter[key] = el.value; renderModels(); };
}

let accRenderSig = '';
let mlRenderSig = '';
$('accBody').addEventListener('click', async ev => {
  const b = ev.target.closest('button[data-a]');
  if (!b) return;
  const u = b.dataset.u, a = b.dataset.a;
  if (a === 'remove' && !confirm('移除账号将删除池状态与 auths/ 下的凭证文件，且不可恢复。确认移除？')) return;
  if (a === 'disable' && !confirm('禁用后该账号不再参与选号（保号任务默认也跳过），需手动解冻才能恢复。若只是想临时让位、仍要保号，请改用「暂停选号」。确认禁用？')) return;
  b.disabled = true;
  try {
    if (a === 'checkin') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/checkin', { method: 'POST' });
      toast('签到完成' + (r.credits != null ? '，积分 ' + r.credits + (r.credits_total > 0 ? '/' + r.credits_total : '') : '') + (r.checkin_message ? '（' + r.checkin_message + '）' : ''), 'ok');
      celebrate(); noteStreak();   // 游戏化层：签到成功撒花 + 记本机操作日
    } else if (a === 'balance') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/balance', { method: 'POST' });
      toast('余额已更新：' + r.credits + (r.credits_total > 0 ? ' / ' + r.credits_total : ''), 'ok');
    } else if (a === 'revive') {
      await api('accounts/' + encodeURIComponent(u) + '/revive', { method: 'POST' });
      toast('已解冻', 'ok');
    } else if (a === 'disable') {
      await api('accounts/' + encodeURIComponent(u) + '/disable', { method: 'POST' });
      toast('已禁用', 'ok');
    } else if (a === 'pause') {
      await api('accounts/' + encodeURIComponent(u) + '/pause', { method: 'POST' });
      toast('已暂停选号（签到 / 保活照常）', 'ok');
    } else if (a === 'resume') {
      await api('accounts/' + encodeURIComponent(u) + '/resume', { method: 'POST' });
      toast('已恢复选号', 'ok');
    } else if (a === 'tasks') {
      openTasks(u);
    } else if (a === 'remove') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/remove', { method: 'POST' });
      toast(r.file_error ? '已移除（凭证文件删除失败：' + r.file_error + '）' : '已移除', 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; loadOverview(true); }
});
$('btnCheckinAll').onclick = async () => {
  try { await api('checkin_all', { method: 'POST' }); toast('全部签到已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnKeepaliveAll').onclick = async () => {
  try { await api('keepalive_all', { method: 'POST' }); toast('全部保活已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnTravelAll').onclick = async () => {
  try { await api('travel_all', { method: 'POST' }); toast('旅行巡检已开始（含领养链路），结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnActivityAll').onclick = async () => {
  try { await api('activity_all', { method: 'POST' }); toast('活跃上报已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
let mdQTimer = null;
$('mdQ').oninput = () => {
  clearTimeout(mdQTimer);
  mdQTimer = setTimeout(() => { mdFilter.q = $('mdQ').value.trim(); renderModels(); }, 120);
};
$('mdReset').onclick = resetModelFilter;
$('btnModels').onclick = loadModels;
/* ── 日志视图已拆到 app_views_logs.js（2026-11-02 组件库拆分第 4 步）────────────
   只许声明、先于 app.js 加载；顶层事件绑定与状态仍留在下方。 */
let logCh = 'all';
$('logChips').addEventListener('click', ev => {
  const b = ev.target.closest('button[data-ch]');
  if (!b) return;
  logCh = b.dataset.ch;
  document.querySelectorAll('#logChips .chip').forEach(c => c.classList.toggle('on', c === b));
  loadLogs();
});
const REQ_PAGE_SIZE = 50;
let reqPage = 1;
let reqSortKey = 'time';
let reqSortDir = -1; // 1=升序, -1=降序
/* 当前展开详情的请求 ID（null = 收起）。按 request_id 定位：翻页/重渲染后仍记得。 */
let reqDetailRid = null;
let selectedRids = new Set();
document.querySelectorAll('.acc.req thead th.sortable').forEach(th => {
  th.addEventListener('click', () => {
    const k = th.dataset.sort;
    if (reqSortKey === k) { reqSortDir = -reqSortDir; }
    else { reqSortKey = k; reqSortDir = (k === 'time' || k === 'duration_ms' || k === 'tokens' || k === 'credit') ? -1 : 1; }
    reqPage = 1;
    renderRequestTable();
  });
});
let reqQTimer = null;
if ($('reqQ')) $('reqQ').oninput = () => {
  clearTimeout(reqQTimer);
  reqQTimer = setTimeout(() => { reqFilter.q = $('reqQ').value.trim(); reqPage = 1; renderRequestTable(); }, 150);
};
if ($('reqOutcome')) $('reqOutcome').onchange = () => {
  reqFilter.outcome = $('reqOutcome').value;
  reqPage = 1; // 筛选变化：从第一页开始看，避免停在越界页
  renderRequestTable();
};
if ($('reqLimit')) $('reqLimit').onchange = () => { reqPage = 1; loadLogs(); };
if ($('btnReqReload')) $('btnReqReload').onclick = () => { reqPage = 1; loadLogs(); };
if ($('btnReqExport')) $('btnReqExport').onclick = () => {
  const list = reqEntries.filter(e => reqMatch(e));
  if (!list.length) { toast('当前没有可导出的请求记录', 'err'); return; }
  const headers = ['时间', '结果', '状态码', '模型', '账号', '来源IP', 'User-Agent', '耗时ms', 'Token', '积分', '请求ID'];
  const rows = list.map(e => {
    const total = Number(e && e.total_tokens || 0) ||
      (Number(e && e.prompt_tokens || 0) + Number(e && e.completion_tokens || 0));
    const credit = e && e.credit_known && Number.isFinite(Number(e.credit))
      ? trimFixed(Number(e.credit).toFixed(2)) : '';
    return [
      e && e.time ? new Date(e.time).toLocaleString('zh-CN', { hour12: false }) : '',
      ({ success: '成功', http_error: 'HTTP 错误', stream_error: '流错误', interrupted: '中断' })[e && e.outcome] || (e && e.outcome) || '',
      e && e.status != null ? e.status : '',
      e && e.model || '', e && e.account || '', e && e.client_ip || '', e && e.user_agent || '',
      e && e.duration_ms != null ? e.duration_ms : '', total || '',
      credit, e && e.request_id || '',
    ];
  });
  exportCSV(headers, rows, 'requests-' + exportStamp() + '.csv');
  toast('已导出 ' + rows.length + ' 条请求记录', 'ok');
};
if ($('btnReqSelExport')) $('btnReqSelExport').onclick = () => {
  const list = reqEntries.filter(e => e && e.request_id && selectedRids.has(e.request_id));
  if (!list.length) { toast('没有选中行', 'err'); return; }
  const headers = ['时间', '结果', '状态码', '模型', '账号', '来源IP', 'User-Agent', '耗时ms', 'Token', '积分', '请求ID'];
  const rows = list.map(e => {
    const total = Number(e && e.total_tokens || 0) ||
      (Number(e && e.prompt_tokens || 0) + Number(e && e.completion_tokens || 0));
    const credit = e && e.credit_known && Number.isFinite(Number(e.credit))
      ? trimFixed(Number(e.credit).toFixed(2)) : '';
    return [
      e && e.time ? new Date(e.time).toLocaleString('zh-CN', { hour12: false }) : '',
      ({ success: '成功', http_error: 'HTTP 错误', stream_error: '流错误', interrupted: '中断' })[e && e.outcome] || (e && e.outcome) || '',
      e && e.status != null ? e.status : '',
      e && e.model || '', e && e.account || '', e && e.client_ip || '', e && e.user_agent || '',
      e && e.duration_ms != null ? e.duration_ms : '', total || '',
      credit, e && e.request_id || '',
    ];
  });
  exportCSV(headers, rows, 'requests-selected-' + exportStamp() + '.csv');
  toast('已导出选中 ' + rows.length + ' 条', 'ok');
};
if ($('btnReqSelClear')) $('btnReqSelClear').onclick = () => {
  const before = reqEntries.length;
  reqEntries = reqEntries.filter(e => !(e && e.request_id && selectedRids.has(e.request_id)));
  const removed = before - reqEntries.length;
  selectedRids.clear();
  renderRequestTable();
  updateSelButtons();
  toast('已清空 ' + removed + ' 条选中记录', 'ok');
};
if ($('reqRange')) trangeBind('reqRange', loadLogs, '0');
$('btnLogPin').onclick = () => {
  logPin = !logPin;
  $('btnLogPin').textContent = '自动滚动：' + (logPin ? '开' : '关');
};
let keyData = null;       // GET config 返回的 api_keys 数组（脱敏后）
let keyAdminCfg = false;  // api_key_configured 状态
let monthlyData = null;   // GET usage/monthly 返回的 {id: tokens}（P1-A 面板展示当月已用）
let keyEditing = null;    // 当前编辑中的 Key 对象（null=未编辑）
let keyIsNew = false;     // true=新增模式, false=编辑模式
let keySaving = false;    // 提交锁：防连点建重复 Key
let keyLoadFailed = false; // 取数失败标记（区分空状态）
// F6：overview.key_rpm 实况索引（key_id → {limit, rpm, total, history[]}）；
// 无观测时保持 null（不是空对象，区分「未观测」与「观测到 0」）。
let rpmStatsById = null;
let lastKeyPlaintext = null;
let lastKeyPlaintextKid = null; // 明文对应的 Key id（防止串卡）
let keyCfgKid = null;     // 当前「接入配置」弹窗打开的 Key id

async function loadKeys() {
  try {
    const [d, m] = await Promise.all([api('config'), api('usage/monthly').catch(() => null)]);
    keyData = Array.isArray(d.config.api_keys) ? d.config.api_keys : [];
    keyAdminCfg = !!d.config.api_key_configured;
    monthlyData = (m && m.keys && typeof m.keys === 'object') ? m.keys : null;
    keyLoadFailed = false;
    renderKeyList();
  } catch (e) {
    keyLoadFailed = true;
    keyData = [];
    renderKeyList();
    $('keyLoadErrMsg').textContent = e.message || '未知错误';
  }
}
/* ── 事件绑定 ────────────────────────────────────────────────────── */

$('btnKeyReload').onclick = loadKeys;
$('btnKeyRetry').onclick = loadKeys;
$('btnKeyAdd').onclick = () => openKeyDialog(null);
$('btnKeyCancel').onclick = closeKeyDialog;
$('btnKeySave').onclick = saveKeyFromDialog;
$('btnKeyGen').onclick = () => {
  const input = $('kfKey');
  input.value = genApiKey();
  input.type = 'text';
};
$('btnKeyCopy').onclick = copyKeyPlaintext;
$('btnKeyShowClose').onclick = () => hideVeil('keyShowVeil');
$('btnCopyCfgOpenAI').onclick = () => copyCfgText('cfgOpenAI');
$('btnCopyCfgAnthropic').onclick = () => copyCfgText('cfgAnthropic');
$('btnCfgDownload').onclick = downloadEnvFile;
$('btnKeyCfgClose').onclick = () => hideVeil('keyCfgVeil');
$('btnKeyReset').onclick = resetKeySecret;
$('btnCopyKeyCfgOpenAI').onclick = () => copyCfgText('keyCfgOpenAI');
$('btnCopyKeyCfgAnthropic').onclick = () => copyCfgText('keyCfgAnthropic');

// realm 胶囊切换
$('kfRealmPills').querySelectorAll('.realm-pill').forEach(p => {
  p.onclick = () => {
    $('kfRealmPills').querySelectorAll('.realm-pill').forEach(x => x.classList.remove('on'));
    p.classList.add('on');
  };
});

/* ── 弹窗统一关闭（Esc / 点击遮罩）────────────────────────────────────
   此前只有 Key 的两个弹窗有 Esc 与遮罩关闭：addVeil（添加账号）、taskVeil（积分
   任务，940px 大弹窗）、vcVeil（券码）、keyVeil（密钥门）都没有出口——键盘用户
   只能摸鼠标找「关闭」。
   这里统一登记：每个 veil 一个关闭回调；Esc 从后往前关当前打开的那个（与视觉层级
   一致）；点遮罩空白处关闭（data-lock 的弹窗禁止，如密钥门必须输入密钥）。 */
const VEIL_CLOSERS = {
  // 用直接 DOM 操作 + 必要的状态清理，而不是调用 closeAdd()/closeTasks()：
  // 后者声明在本文件更下方（函数声明虽会 hoist，但这里刻意不依赖求值顺序，
  // 也让「关弹窗」这件事的语义在本地一目了然）。
  addVeil: () => { stopPoll(); loginState = null; $('addVeil').classList.remove('on'); },
  taskVeil: () => { $('taskVeil').classList.remove('on'); taskUID = null; },
  vcVeil: () => $('vcVeil').classList.remove('on'),
  keyEditVeil: () => closeKeyDialog(),
  keyShowVeil: () => hideVeil('keyShowVeil'),
  keyCfgVeil: () => hideVeil('keyCfgVeil'),
  keyVeil: () => $('keyVeil').classList.remove('on'), // data-lock 时由下方逻辑跳过
  pgVeil: () => { pgStop(); $('pgVeil').classList.remove('on'); },
};

// 点击遮罩关闭（事件目标就是 veil 本身 = 点在遮罩空白处，不是在 .dlg 上）
Object.entries(VEIL_CLOSERS).forEach(([id, close]) => {
  const el = $(id);
  if (!el) return;
  el.onclick = (e) => {
    if (e.target !== el) return;      // 点在弹窗内容里，不关
    if (el.dataset.lock) return;      // data-lock="1"：必须显式操作才能关（如密钥门）
    close();
  };
});

// Esc 关闭：从后往前找当前打开的 veil，只关最上面那个（一次一个，符合直觉）。
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  const ids = Object.keys(VEIL_CLOSERS);
  for (let i = ids.length - 1; i >= 0; i--) {
    const el = $(ids[i]);
    if (el && el.classList.contains('on')) {
      if (el.dataset.lock) continue; // data-lock：Esc 也不关（须输入密钥）
      VEIL_CLOSERS[ids[i]]();
      return;
    }
  }
});

/* ── 配置页视图已拆到 app_views_config.js（2026-11-02 组件库拆分第 4 步）────────────
   只许声明、先于 app.js 加载；顶层事件绑定与状态仍留在下方。 */
const CFG_MAP = {
  listen: ['listen'], api_key: ['api_key'],
  package_detail_limit: ['panel', 'package_detail_limit'],
  checkin_hours: ['schedule', 'checkin_hours'], checkin_enabled: ['schedule', 'checkin_enabled'], growth_hours: ['schedule', 'growth_hours'], growth_enabled: ['schedule', 'growth_enabled'],
  travel_hours: ['schedule', 'travel_hours'], travel_enabled: ['schedule', 'travel_enabled'],
  activity_hours: ['schedule', 'activity_hours'], activity_enabled: ['schedule', 'activity_enabled'],
  keepalive_hours: ['schedule', 'keepalive_hours'], keepalive_enabled: ['schedule', 'keepalive_enabled'],
  balance_refresh_enabled: ['schedule', 'balance_refresh_enabled'], balance_refresh_minutes: ['schedule', 'balance_refresh_minutes'],
  include_disabled_in_tasks: ['schedule', 'include_disabled_in_tasks'],
  max_in_flight: ['pool', 'max_in_flight'], max_in_flight_global: ['pool', 'max_in_flight_global'],
  breaker_threshold: ['pool', 'breaker_threshold'],
  degrade_threshold: ['pool', 'degrade_threshold'], degrade_cooldown: ['pool', 'degrade_cooldown'],
  degrade_cooldown_max: ['pool', 'degrade_cooldown_max'],
  cost_explore_interval: ['pool', 'cost_explore_interval'],
  credit_floor: ['pool', 'credit_floor'],
  prefer_expiring: ['pool', 'prefer_expiring'], expiring_soon: ['pool', 'expiring_soon'],
  soft_rate: ['cooldown', 'soft_rate'], soft_rate_max: ['cooldown', 'soft_rate_max'],
  breaker_cooldown: ['pool', 'breaker_cooldown'], breaker_cooldown_max: ['pool', 'breaker_cooldown_max'],
  breaker_probe_gap: ['pool', 'breaker_probe_gap'],
  global_max_in_flight: ['pool', 'global_max_in_flight'],
  idle_weight_per_hour: ['pool', 'idle_weight_per_hour'], idle_weight_max: ['pool', 'idle_weight_max'],
  ttl: ['session_sticky', 'ttl'],
  timeout_seconds: ['upstream', 'timeout_seconds'], header_timeout_seconds: ['upstream', 'header_timeout_seconds'],
  idle_timeout_seconds: ['upstream', 'idle_timeout_seconds'], user_agent: ['upstream', 'user_agent'],
  prompt_mode: ['prompt', 'mode'], prompt_file: ['prompt', 'file'],
  sanitize_blacklist_fingerprints: ['features', 'sanitize_blacklist_fingerprints'],
  session_sticky_enabled: ['session_sticky', 'enabled'],
  session_sticky_derive: ['session_sticky', 'derive'],
  request_client_info: ['logging', 'request_client_info'],
  usage_credit_cny_rate: ['usage', 'credit_cny_rate'],
  alerting_webhook_url: ['alerting', 'webhook_url'],
  alerting_min_credit_threshold: ['alerting', 'min_credit_threshold'],
};
const CLEARABLE_CFG = new Set(['user_agent', 'prompt_file', 'alerting_webhook_url']);
const DURATION_RE = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;
const DURATION_FIELDS = ['soft_rate', 'soft_rate_max', 'breaker_cooldown', 'breaker_cooldown_max',
  'degrade_cooldown', 'degrade_cooldown_max', 'cost_explore_interval', 'expiring_soon', 'ttl',
  'breaker_probe_gap'];
const DURATION_TIP = '格式应为 Go 时长：30m / 2h / 600s / 1h30m';
$('cfgForm').addEventListener('input', ev => {
  markCfgDirty(); // 任何改动都置脏（供 go() 的切换守卫与 #cfgNote 提示）
  if (DURATION_FIELDS.includes(ev.target.name)) markDurationFields();
});
$('cfgForm').addEventListener('change', markCfgDirty);
$('btnEye').onclick = () => {
  const el = $('cfgKey');
  const show = el.type === 'password';
  el.type = show ? 'text' : 'password';
  $('btnEye').textContent = show ? '隐藏' : '显示';
};
$('btnCfgReload').onclick = loadConfig;
$('cfgForm').onsubmit = async ev => {
  ev.preventDefault();
  // 时长字段脏值拦截：标红 + toast 点名，不发保存请求（后端同样会拒，这里前置）。
  markDurationFields();
  const firstBad = DURATION_FIELDS.find(durationBad);
  if (firstBad) {
    const el = $('cfgForm').elements[firstBad];
    el.focus();
    toast('「' + (el.closest('.fld')?.querySelector('.lb')?.textContent || firstBad) + '」' + DURATION_TIP, 'err');
    return;
  }
  const btn = $('btnCfgSave');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    const r = await api('config', { method: 'POST', body: JSON.stringify(collectConfig()) });
    const n = (r.restart_required || []).length;
    toast(n ? '配置已保存，其中 ' + n + ' 项需重启进程生效' : '配置已保存并立即生效', 'ok');
    // 密钥可能已改：本次会话沿用新值，避免下一次轮询被 401。
    const k = $('cfgKey').value.trim();
    if (k) localStorage.setItem(LS_KEY, k);
    loadConfig();
    loadOverview(true);
  } catch (e) { toast('保存失败：' + e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '保存配置'; }
};
/* ── 添加账号 ─────────────────────────────────────────────────────── */
function openAdd() {
  $('addVeil').classList.add('on');
  // 重置到登录标签
  switchAddTab('login');
  $('addPick').hidden = false;
  $('addLoad').hidden = true; $('addReady').hidden = true;
  $('addDone').hidden = true; $('addErr').hidden = true;
  $('importDone').hidden = true; $('importErr').hidden = true;
  $('btnCopyUrl').hidden = true; $('btnOpenUrl').hidden = true;
  $('btnStartLogin').hidden = false; $('btnStartLogin').disabled = false;
  stopPoll();
}
function switchAddTab(tab) {
  document.querySelectorAll('#addTabs .tab').forEach(b => b.classList.toggle('on', b.dataset.tab === tab));
  $('addTabLogin').hidden = tab !== 'login';
  $('addTabImport').hidden = tab !== 'import';
}
document.querySelectorAll('#addTabs .tab').forEach(b => {
  b.onclick = () => switchAddTab(b.dataset.tab);
});
function startAddLogin() {
  const realm = (document.querySelector('input[name="addRealm"]:checked') || {}).value || 'cn';
  $('btnStartLogin').disabled = true;
  $('addLoad').hidden = false; $('addErr').hidden = true;
  api('login/start', { method: 'POST', body: JSON.stringify({ realm }) }).then(r => {
    loginState = r.state;
    $('addUrl').textContent = r.url;
    $('addPick').hidden = true; // 选域锁定（会话已按该域发起）
    $('addLoad').hidden = true; $('addReady').hidden = false;
    $('btnStartLogin').hidden = true;
    $('btnCopyUrl').hidden = false; $('btnOpenUrl').hidden = false;
    loginTimer = setInterval(pollLogin, 3000);
  }).catch(e => {
    $('addLoad').hidden = true;
    $('btnStartLogin').disabled = false;
    $('addErr').hidden = false;
    $('addErr').textContent = e.message;
  });
}
function stopPoll() { if (loginTimer) { clearInterval(loginTimer); loginTimer = null; } }
async function pollLogin() {
  if (!loginState) return;
  try {
    const r = await api('login/poll?state=' + encodeURIComponent(loginState));
    if (r.done) {
      stopPoll();
      $('addReady').hidden = true;
      $('addDone').hidden = false;
      $('addDone').textContent = '已添加 ' + (r.nickname || r.uid) + (r.realm === 'global' ? '（国际版）' : '') + (r.credits >= 0 ? ' · 积分 ' + r.credits + (r.credits_total > 0 ? '/' + r.credits_total : '') : '') + '，账号已载入池中';
      celebrate(); noteStreak();   // 游戏化层：入池成功撒花 + 记本机操作日
      setTimeout(() => { closeAdd(); loadOverview(true); }, 1600);
    }
  } catch (e) {
    stopPoll();
    $('addReady').hidden = true;
    $('addErr').hidden = false;
    $('addErr').textContent = e.message + '（关闭后重新添加）';
  }
}
function closeAdd() { stopPoll(); loginState = null; $('addVeil').classList.remove('on'); }
$('btnCloseAdd').onclick = closeAdd;
$('btnStartLogin').onclick = startAddLogin;
$('btnOpenUrl').onclick = () => open($('addUrl').textContent, '_blank');
$('btnCopyUrl').onclick = () => navigator.clipboard.writeText($('addUrl').textContent)
  .then(() => toast('链接已复制', 'ok'), () => toast('复制失败，请手动选择复制', 'err'));
$('importFile').onchange = async () => {
  const file = $('importFile').files[0];
  if (!file) return;
  $('importDone').hidden = true; $('importErr').hidden = true;
  const fd = new FormData();
  fd.append('file', file);
  const h = {};
  const k = localStorage.getItem(LS_KEY);
  if (k) h['Authorization'] = 'Bearer ' + k;
  try {
    const r = await fetch('/panel/api/import/cockpit', { method: 'POST', body: fd, headers: h });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    $('importDone').hidden = false;
    $('importDone').textContent = '导入完成：成功 ' + d.imported + ' 个' + (d.skipped ? '，跳过 ' + d.skipped + ' 个' : '');
    if (d.errors && d.errors.length) {
      console.warn('import errors:', d.errors);
    }
    loadOverview(true);
  } catch (e) {
    $('importErr').hidden = false;
    $('importErr').textContent = '导入失败：' + e.message;
  }
  $('importFile').value = '';
};

/* ── 顶部动作 ─────────────────────────────────────────────────────── */
$('btnAdd').onclick = openAdd;
/* 顶栏「刷新」= 只刷新视图（重拉 overview + 当前视图数据），**不打上游**。
   此前这里直接调 balance_all（对所有账号向上游查余额，是重量级操作），
   但按钮文案是「刷新」——用户只想刷新视图时却触发了整池上游请求。
   真实的「从上游刷新余额」已挪到账号池 header（见 btnBalanceAll），与其它批量
   按钮同处，且带二次确认。 */
$('btnRefresh').onclick = async () => {
  const b = $('btnRefresh');
  b.disabled = true; b.textContent = '刷新中…';
  try {
    await loadOverview(true);
    if (view === 'logs') await loadLogs();
    else if (view === 'usage') await loadUsage();
    else if (view === 'keys') await loadKeys();
    else if (view === 'models') await loadModels();
    else if (view === 'packages') await loadPackages(true);
  } catch (e) { toast('刷新失败：' + e.message, 'err'); }
  finally { b.disabled = false; b.textContent = '刷新'; }
};

/* 「从上游刷新余额」：独立的重量级操作（逐账号查上游），故带二次确认。 */
$('btnBalanceAll').onclick = async () => {
  if (!confirm('将从上游逐账号查询余额并写回积分快照。\n账号多时会持续数秒到数十秒，确认继续？')) return;
  const b = $('btnBalanceAll');
  b.disabled = true; b.textContent = '刷新中…';
  try {
    await api('balance_all', { method: 'POST' });
    await loadOverview(true);
    toast('余额已从上游刷新', 'ok');
  } catch (e) { toast('刷新失败：' + e.message, 'err'); }
  finally { b.disabled = false; b.textContent = '刷新余额'; }
};

/* ── 轮询 ─────────────────────────────────────────────────────────── */
function refreshVisible() {
  if (view === 'accounts') loadOverview(true);
  else if (view === 'logs') loadLogs();
  else if (view === 'taskscenter') reattachQueueView();
}
function start() {
  loadOverview(true);
  renderStreak();   // 游戏化层：启动即渲染本机连续操作天数条（含后台切回时重算）
  renderAchievements(); // 游戏化层：启动即渲染本机成就带（数据同源 localStorage）
  if (refTimer) clearInterval(refTimer);
  // 页面隐藏（切到别的标签页/最小化）时不轮询：此前无视 document.hidden，
  // 后台标签页每 5 秒打一次 overview（日志页还会并发 3 个接口），纯浪费
  // CPU 与上游配额。可见时由 visibilitychange 恢复。
  if (!document.hidden) refTimer = setInterval(refreshVisible, 5000);
  checkAuthGate();
}
async function checkAuthGate() {
  try { await api('overview'); }
  catch (e) { if (String(e.message).includes('密钥') || String(e.message).includes('api_key')) return; }
}
/* 可见性变化：隐藏时停表，恢复时立刻刷新一次并重新开始轮询。
   立刻刷一次是必要的——后台期间数据可能已变，回到前台应看到最新值。 */
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    if (refTimer) { clearInterval(refTimer); refTimer = null; }
    // queueTimer 声明在本文件更下方（let，存在 TDZ）：用 typeof 守卫避免
    // 在声明前触发 visibilitychange 时抛 ReferenceError。
    if (typeof queueTimer !== 'undefined' && queueTimer) { clearInterval(queueTimer); queueTimer = null; }
  } else {
    start();
    if (view === 'taskscenter') reattachQueueView();
  }
});
start();

/* ── 积分任务 + 任务中心已拆到 app_views_tasks.js（2026-11-02 组件库拆分第 4 步）──────────
   只许声明、先于 app.js 加载；顶层事件绑定与状态仍留在下方。 */
let taskUID = null;
const AUTO_TASKS = {
  'chat_5': '上报 5 条对话活跃事件（自动补足差额）',
  'first_buddy': '上报解锁 → 同意协议 → 领取第一只 Buddy',
  'Model_chat_GLM5.2': '接受任务 → glm-5.2 真实对话一次 → 对齐模型上报',
  'RichMeow_Chat': '桌面指纹事件链上报（已验证可点亮）',
  'Buddy_App': '上报「进入 Buddy 应用」事件链（已验证可点亮）',
  'Buddy_App_QQ': '上报「进入企鹅教师助手」事件链（已验证可点亮）',
  'automation_1': '上报「定时任务创建」事件（已验证可点亮）',
  'Library_read': '上报「读资料库介绍」事件（已验证可点亮）',
  'template_5': '上报「使用模板创建任务」事件组 ×5（三账号实测点亮）',
  'playbook_prompt': '上报「灵感案例做同款发送 Prompt」事件组（三账号实测点亮）',
  'create_canvas': '上报「设计创意画布创建」事件组（三账号实测点亮，+300 分）',
  'expert_5': '真实专家召唤+使用链 ×5（专家市场+真实 chat，三账号实测点亮）',
  'Expert_team_use_3': '真实专家团召唤+使用链 ×3（三账号实测点亮）',
  'Hp_Appearance': '设置主题 API + 皮肤生效事件（两账号实测点亮）',
  'black_cat': '夜猫子：23:00–08:00 窗口内 glm-5.2 对话补足（窗口外提示等 23 点排程）',
  'Expert_lighthouse': '真实轻量云专家召唤+使用链（真实对话 requestId，两账号实测点亮）',
  'skill_1': '真实对话 + skill_info 技能加载事件（实测点亮）',
  'school_season': '校园日（小程序口径）：accept → mini 对话+activityId 上报 → 领奖（+100c+5e）',
  'Sequential_Tasks_1': '小程序首对话（小程序口径）：accept → mini 对话上报 → 领奖（+100c+5e）',
  'Sequential_Tasks_2': '小程序选专家对话（小程序口径）：市场专家 id → accept → expert_actual_use 上报 → 领奖（+200c+5e）',
  'Sequential_Tasks_3': '小程序五次对话（小程序口径）：accept → mini 对话上报 ×5（自动补差额）→ 领奖（+300c+5e）',
  'Sequential_Tasks_4': '小程序定时任务（预留，每日零点解锁一环）：accept → 定时任务创建事件 → 领奖（判据待解锁验证）',
  'Sequential_Tasks_5': '小程序使用 GLM5.2（预留）：accept → 带模型字段的 mini 对话上报 → 领奖（判据待解锁验证）',
  'Sequential_Tasks_6': '小程序十次对话（预留）：accept → mini 对话上报 ×target（自动补差额）→ 领奖',
  'Sequential_Tasks_7': '体验灵感功能（预留，疑 PC 口径）：accept → 灵感事件组（PC+mp 双形态）→ 领奖（判据待解锁验证）'
};
$('btnCloseTask').onclick = closeTasks;
$('btnTaskReload').onclick = loadTasks;
$('btnTaskAcceptAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskAcceptAll');
  btn.disabled = true; btn.textContent = '接受中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/accept_all', { method: 'POST' });
    const n = r.accepted || 0;
    if (r.failed && r.failed.length) {
      toast(`已接受 ${n} 个，${r.failed.length} 个被上游拒绝（可重试）`, 'err');
    } else {
      toast(n ? `已接受 ${n} 个任务` : (r.message || '所有任务均已接受'), 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '全部接受'; loadTasks(); }
};
$('btnTaskAutoAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskAutoAll');
  if (!confirm('将依次执行：补报对话事件、领取 Buddy、glm-5.2 对话、尝试上报。\n过程约 1-2 分钟（含真实对话），确认继续？')) return;
  btn.disabled = true; btn.textContent = '执行中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/auto_all', { method: 'POST' });
    const okN = (r.results || []).filter(x => x.status === 'done').length;
    const skipN = (r.results || []).filter(x => x.status === 'skipped').length;
    const errN = (r.results || []).filter(x => x.status === 'error').length;
    toast(`执行完成：成功 ${okN} 项，跳过 ${skipN} 项${errN ? '，失败 ' + errN + ' 项' : ''}`, errN ? 'err' : 'ok');
    console.log('auto_all results:', r.results);
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '一键完成可自动任务'; loadTasks(); }
};
$('taskBody').addEventListener('click', async ev => {
  const b = ev.target.closest('button[data-t]');
  if (!b || !taskUID) return;
  const kind = b.dataset.t, code = b.dataset.c;
  b.disabled = true;
  try {
    if (kind === 'auto') {
      // 一键完成：后端执行动作 → 回读进度 → 汇报（耗时可到分钟级，含真实对话）
      b.textContent = '执行中…';
      const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/auto', {
        method: 'POST', body: JSON.stringify({ task_code: code })
      });
      if (r.skipped) {
        toast(r.message || '已跳过', 'ok');
      } else {
        const advanced = r.progress_before !== r.progress_after;
        let msg = r.message || '已执行';
        if (r.progress_after) msg += `（进度 ${r.progress_before} → ${r.progress_after}）`;
        if (r.claimed) msg += '，奖励已自动到账';
        else if (r.claimable) msg += r.claim_error ? '，可点「领取」重试' : '';
        else if (r.attempt && !advanced) msg += '；进度未动，该任务可能需要官方客户端';
        toast(msg, (r.claimed || advanced) ? 'ok' : 'err');
      }
      loadOverview(true);
    } else {
      const path = 'accounts/' + encodeURIComponent(taskUID) + '/tasks/' + (kind === 'claim' ? 'claim' : 'accept');
      const body = kind === 'claim' ? { task_code: code } : { task_codes: [code] };
      await api(path, { method: 'POST', body: JSON.stringify(body) });
      toast(kind === 'claim' ? '已领取奖励' : '已接受任务', 'ok');
      if (kind === 'claim') { loadOverview(true); celebrate(); noteStreak(); }   // 游戏化层：领取成功撒花 + 记本机操作日
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { loadTasks(); }
});
const LUCK_SVG = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M3.2 5.2 5 1.8l3 2.4 3-2.4 1.8 3.4-1.4 2.6 1.4 2.6-3.4 2.2H6l-3.4-2.2 1.4-2.6z" opacity=".9"/><circle cx="8" cy="9" r="1.1" fill="currentColor" stroke="none"/></svg>';
const SCHOOL_TITLES = {
  share_invite: '分享活动 +100c', desktop_chat_1_time: '桌面端体验 +100c（单次）',
  chat_3_times: '和 AI 对话 3 次 +50c', expert_use: '召唤开学季专家 +50c',
  task_student_verify: '学生认证 +100c（需真实认证，不做）',
};
$('btnSchoolVouchers').onclick = loadSchoolVouchers;
$('btnVcClose').onclick = () => $('vcVeil').classList.remove('on');
$('btnVcRefresh').onclick = loadSchoolVouchers;
let queueTimer = null, lastQueueSeq = 0;
const GROWTH_TITLES = {}; // code → 展示名（扫描时从任务列表带出）
async function runScanAll(btn) {
  // 停掉队列轮询：显式扫描 = 切到待办视图（否则在途队列下一 tick 会冲掉结果）。
  if (queueTimer) { clearInterval(queueTimer); queueTimer = null; }
  const b = btn || $('btnScanAll');
  b.disabled = true; b.textContent = '扫描中…';
  try {
    const d = await api('tasks/scan_all', { method: 'POST' });
    renderQueue(groupItems(d), null, '没有待办任务 🎉', '全部账号的成长任务与开学季活动都已完成，明日再来。');
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; b.textContent = '扫描待办'; }
}
$('btnScanAll').onclick = () => runScanAll($('btnScanAll'));
if ($('btnScanAllEmpty')) $('btnScanAllEmpty').onclick = () => {
  const b = $('btnScanAllEmpty');
  const orig = b.textContent;
  b.textContent = '扫描中…'; b.disabled = true;
  runScanAll(b).finally(() => { b.disabled = false; b.textContent = orig; });
};
$('btnRunQueue').onclick = async () => {
  const conc = Number($('qcConc').value) || 1;
  if (!confirm('扫描全部账号待办并排队执行（账号并发 ' + conc + '，账号内串行）。\n含真实对话的任务耗时较长，确认继续？')) return;
  const b = $('btnRunQueue');
  b.disabled = true; b.textContent = '启动中…';
  try {
    const r = await api('tasks/run_queue', { method: 'POST', body: JSON.stringify({ concurrency: conc }) });
    if (!r.started) { toast(r.message || '没有待办任务', 'ok'); return; }
    lastQueueSeq = r.seq || 0;
    toast('队列已启动：' + r.total + ' 项（并发 ' + conc + '）', 'ok');
    startQueuePolling();
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; b.textContent = '执行全部待办'; }
};
const ST_WORDS = { done: '完成', running: '执行中', error: '失败', skipped: '跳过', pending: '排队', scan: '待执行' };
if ($('usDimTabs')) $('usDimTabs').addEventListener('click', ev => {
  const b = ev.target.closest('button[data-dim]');
  if (!b) return;
  usDim = b.dataset.dim;
  renderUsageDim();
});
if ($('usCreditTabs')) $('usCreditTabs').addEventListener('click', ev => {
  const b = ev.target.closest('button[data-dim]');
  if (!b) return;
  usCreditDim = b.dataset.dim;
  renderCreditDim();
});
if ($('usSort')) $('usSort').onchange = () => {
  usSort = $('usSort').value;
  renderUsageDim();
};
if ($('btnUsage')) $('btnUsage').onclick = loadUsage;
if ($('btnUsExport')) $('btnUsExport').onclick = () => {
  const d = usageData || {};
  const m = US_DIMS[usDim] || US_DIMS.account;
  const rows = usSortRows(d[m.key] || [], usSort);
  if (!rows.length) { toast('当前维度没有可导出的数据', 'err'); return; }
  const headers = ['维度', m.title, '域', '请求', '失败', 'Prompt', 'Completion', '合计Token', '均延迟ms', '均速率tok/s'];
  const dimLabel = { account: '账号', model: '模型', realm: '域', key: 'API Key' }[usDim] || usDim;
  const out = rows.map(x => [
    dimLabel,
    usDim === 'account' ? String(x.key || '').slice(0, 8) : (x.key || ''),
    x.realm || '', x.requests || 0, x.errors || 0,
    x.prompt_tokens || 0, x.completion_tokens || 0, x.total_tokens || 0,
    Math.round(x.avg_latency_ms || 0), (x.avg_tokens_per_second || 0).toFixed(1),
  ]);
  exportCSV(headers, out, 'usage-' + dimLabel + '-' + exportStamp() + '.csv');
  toast('已导出 ' + out.length + ' 行用量明细', 'ok');
};
if ($('btnCreditExport')) $('btnCreditExport').onclick = () => {
  const d = usageData || {};
  let headers, out, dimLabel;
  if (usCreditDim === 'model') {
    dimLabel = '按模型';
    headers = ['模型', '积分倍率', '请求', '扣除积分', '有效样本Token', '积分/1M Token', '缓存命中率%'];
    out = (d.credit_by_model || []).map(r => [
      r.key || '', fmtModelRate(r.rate), r.requests || 0, r.credits || 0,
      r.credit_tokens || 0,
      r.credit_samples ? Number(r.credits_per_1m_tokens || 0).toFixed(4) : '',
      (r.cache_hit_tokens + r.cache_miss_tokens) ? cacheRateText(r.cache_hit_tokens, r.cache_miss_tokens).replace('%', '') : '',
    ]);
  } else if (usCreditDim === 'key') {
    dimLabel = '按Key';
    headers = ['API Key', '名称', '请求', '扣除积分', '有效样本Token', '积分/1M Token', '缓存命中率%'];
    out = (d.credit_by_key || []).map(r => [
      r.key || '', r.nickname || '', r.requests || 0, r.credits || 0,
      r.credit_tokens || 0,
      r.credit_samples ? Number(r.credits_per_1m_tokens || 0).toFixed(4) : '',
      (r.cache_hit_tokens + r.cache_miss_tokens) ? cacheRateText(r.cache_hit_tokens, r.cache_miss_tokens).replace('%', '') : '',
    ]);
  } else {
    dimLabel = '按账号';
    headers = ['账号', '昵称', '域', '请求', '扣除积分', '有效样本Token', '积分/1M Token', '缓存命中率%'];
    out = (d.credit_by_account || []).map(r => [
      String(r.key || '').slice(0, 8), r.nickname || '', r.realm || '', r.requests || 0,
      r.credits || 0, r.credit_tokens || 0,
      r.credit_samples ? Number(r.credits_per_1m_tokens || 0).toFixed(4) : '',
      (r.cache_hit_tokens + r.cache_miss_tokens) ? cacheRateText(r.cache_hit_tokens, r.cache_miss_tokens).replace('%', '') : '',
    ]);
  }
  if (!out.length) { toast('当前维度没有可导出的积分记录', 'err'); return; }
  exportCSV(headers, out, 'credits-' + dimLabel + '-' + exportStamp() + '.csv');
  toast('已导出 ' + out.length + ' 行积分记录', 'ok');
};
if ($('usRange')) trangeBind('usRange', loadUsage);
const US_DIMS = {
  account: { key: 'by_account', title: '账号', withRealm: true, withPerf: true, span: 11 },
  model: { key: 'by_model', title: '模型', withRealm: false, withPerf: false, span: 8 },
  realm: { key: 'by_realm', title: 'realm', withRealm: false, withPerf: false, span: 8 },
  // Key 维度带性能两列（谁在拖慢网关，按 Key 看得出来），故 span 与 account 同口径。
  key: { key: 'by_key', title: 'API Key', withRealm: false, withPerf: true, span: 10 },
};
const US_DIM_TABS = [['account', '按账号'], ['model', '按模型'], ['realm', '按域'], ['key', '按 Key']];
const US_CREDIT_TABS = [['account', '按账号'], ['model', '按模型'], ['key', '按 Key']];
let usageRateWarmAt = 0;
/* ── 积分构成 ─────────────────────────────────────────────────────── */
/* 账号余额 = 若干积分包之和（按次发放，面额 6-1500）。同色即同类，跨账号对比。 */

const PK_COLORS = ['#4f8cff', '#25b08b', '#e8a33d', '#c96bd6', '#e2607a',
                   '#5aa9e6', '#8fbf3f', '#b58b5a', '#7d8fa8', '#d4785c'];
const PK_ACCOUNT_COLORS = ['#4f8cff', '#25b08b', '#e8a33d', '#c96bd6',
                           '#e2607a', '#20a4a4', '#8fbf3f', '#d4785c',
                           '#7c83db', '#c48a2f', '#b45f8c', '#5aa9e6'];

function pkColor(i) { return PK_COLORS[i % PK_COLORS.length]; }

// pkAccountColorMap 按 UID 稳定分配颜色：排序后分配，账号刷新/重排不会换色。
function pkAccountColorMap(list) {
  const uids = (list || [])
    .filter(a => a && !a.error && a.uid)
    .map(a => String(a.uid))
    .sort();
  const colors = new Map();
  uids.forEach((uid, i) => colors.set(uid, PK_ACCOUNT_COLORS[i % PK_ACCOUNT_COLORS.length]));
  return colors;
}

/* pkBySource 把包按名称归并，得到「来源 → 面额/余额/个数」。这是对比的关键视图：
   两个号的差异一定体现在某几个来源的面额上。 */
function pkBySource(packs) {
  const m = new Map();
  for (const p of packs) {
    // 分组键用 code + name（上游同 name 可能是不同活动包）。
    const k = (p.package_code || '') + '|' + (p.name || '(未命名)');
    const e = m.get(k) || {
      key: k, name: p.name || '(未命名)', code: p.package_code || '',
      n: 0, remain: 0, size: 0, used: 0, minEnd: '', minCreated: '',
    };
    e.n += 1;
    e.remain += Number(p.remain || 0);
    e.size += Number(p.size || 0);
    e.used += Number(p.used || 0);
    const t = (p.end_time || '').slice(0, 10);
    if (t && (!e.minEnd || t < e.minEnd)) e.minEnd = t;
    const c = (p.created_at || '').slice(0, 10);
    if (c && (!e.minCreated || c < e.minCreated)) e.minCreated = c;
    m.set(k, e);
  }
  return [...m.values()].sort((a, b) => b.size - a.size);
}

const PK_DEFAULT_DETAIL_LIMIT = 5;

function pkDetailLimitValue(raw) {
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : PK_DEFAULT_DETAIL_LIMIT;
}

function pkDetailLimit(cfg) {
  return pkDetailLimitValue(cfg && cfg.panel && cfg.panel.package_detail_limit);
}

const PK_DAY_MS = 24 * 3600 * 1000;

function pkExpiryMs(p) {
  const raw = Number(p && p.expires_at);
  if (Number.isFinite(raw) && raw > 0) return raw;
  const text = String((p && p.end_time) || '').trim();
  if (!text) return null;
  let iso = text.includes('T') ? text : text.replace(' ', 'T');
  if (!/(?:Z|[+-]\d\d:\d\d)$/.test(iso)) iso += '+08:00';
  const parsed = Date.parse(iso);
  return Number.isFinite(parsed) ? parsed : null;
}

// pkDetailCompare：正余额包先按到期时间，其余折叠；同到期按面额降序。
// mode 跟随 pkSortMode（end_asc / size_desc），typeof 守卫回落 end_asc。
function pkDetailCompare(a, b) {
  const sizeOf = p => {
    const n = Number(p && p.size);
    return Number.isFinite(n) ? n : 0;
  };
  const mode = (typeof pkSortMode === 'string' && pkSortMode) || 'end_asc';
  if (mode === 'size_desc') {
    const d = sizeOf(b) - sizeOf(a);
    if (d !== 0) return d;
  }
  const ea = pkExpiryMs(a), eb = pkExpiryMs(b);
  if (ea == null && eb != null) return 1;
  if (ea != null && eb == null) return -1;
  if (ea != null && eb != null && ea !== eb) return ea - eb;
  return sizeOf(b) - sizeOf(a);
}

/* ── 积分包视图已拆到 app_views_packages.js（2026-11-02 组件库拆分第 4 步）────────────
   只放函数声明、先于 app.js 加载；pkSortMode 等状态仍留在下方（本文件函数只在异步/事件中调用）。 */

// 积分包明细展开/收起的顶层事件绑定：必须留在 app.js（引用 $ 与 fmtTok，
// 且 app_views_packages.js 先于 app.js 加载，放在那边会在 $ 初始化前执行 → 白屏）。
if ($('pkDetail')) $('pkDetail').addEventListener('click', ev => {
  const btn = ev.target.closest('button[data-pk-group]');
  if (!btn) return;
  const body = btn.closest('tbody');
  if (!body) return;
  const group = btn.dataset.pkGroup;
  const expanded = btn.getAttribute('aria-expanded') === 'true';
  body.querySelectorAll('tr[data-pk-row="' + group + '"]').forEach(row => { row.hidden = expanded; });
  const count = btn.dataset.count || '0';
  const size = btn.dataset.size || '0';
  const remain = btn.dataset.remain || '0';
  btn.setAttribute('aria-expanded', String(!expanded));
  if (group === 'rest') {
    btn.textContent = expanded
      ? '其余未用完 ' + count + ' 个包（面额合计 ' + fmtTok(size) + ' · 剩余 ' +
        fmtTok(remain) + '），展开'
      : '收起其余未用完 ' + count + ' 个包';
  } else {
    btn.textContent = expanded
      ? '已用完 ' + count + ' 个包（面额合计 ' + fmtTok(size) + '），展开'
      : '收起已用完 ' + count + ' 个包';
  }
});

const LS_PK_SORT = 'pkSortMode';
let pkSortMode = localStorage.getItem(LS_PK_SORT) || 'end_asc';
const PK_SORT_LABELS = { end_asc: '按到期升序 · 近的在前', size_desc: '按面额降序' };
// 排序切换时重排明细需要 detailLimit（上游折叠配置），loadPackages 拉到后缓存。
let lastDetailLimit = PK_DEFAULT_DETAIL_LIMIT;

// 排序规则控件：恢复上次选择并绑定切换。
if ($('pkSort')) {
  $('pkSort').value = pkSortMode;
  if ($('pkSort').value !== pkSortMode) {        // localStorage 里存了废弃值：回落默认
    pkSortMode = 'end_asc';
    localStorage.setItem(LS_PK_SORT, pkSortMode);   // 不用 removeItem：harness 桩无此方法
  }
  $('pkSort').onchange = () => {
    pkSortMode = $('pkSort').value;
    localStorage.setItem(LS_PK_SORT, pkSortMode);
    if (lastPackages) renderPackages(lastPackages, lastDetailLimit);   // 数据在内存，直接重排
  };
}


/* 快照版到期提醒：不查上游，直接用 /overview 已有数据（默认每 5 分钟更新）。
   只有最早一批的日期与数量；完整明细点「检查」再触发上游查询。 */
function renderExpirySnapshot(d) {
  const list = (d && d.accounts) || [];
  const today = new Date(); today.setHours(0, 0, 0, 0);
  const keyed = list.map(a => {
    // 后端给的是 RFC3339 带时区；无到期信息（长期包/未刷新）的账号排在最后。
    let key = null, ms = 0;
    if (a.credits_earliest_expiry) {
      const t = new Date(a.credits_earliest_expiry);
      if (!isNaN(t.getTime())) { ms = t.getTime(); key = ms; }
    }
    return { a, key, ms };
  });
  keyed.sort((x, y) => {
    if (x.key === null && y.key === null) return 0;
    if (x.key === null) return 1;
    if (y.key === null) return -1;
    return x.key - y.key;
  });
  const rows = keyed.map(({ a, ms }) => {
    const nm = esc(a.nickname || String(a.uid || '').slice(0, 8));
    if (!ms || !a.credits_earliest_remaining) {
      return '<div class="exp-row lv-none">' +
        '<span class="exp-nm">' + nm + '</span>' +
        '<span class="exp-main">无近期到期批次</span></div>';
    }
    const days = Math.max(0, Math.ceil((ms - today.getTime()) / 86400000));
    const remain = Number(a.credits_earliest_remaining || 0);
    const daily = Math.ceil(remain / Math.max(1, days));
    const lv = days <= 3 ? 'lv-bad' : days <= 7 ? 'lv-warn' : 'lv-ok';
    const dayWord = days === 0 ? '今天' : days === 1 ? '明天' : days + ' 天';
    const when = new Date(ms).toISOString().slice(0, 10);
    return '<div class="exp-row ' + lv + '" title="' + esc(String(a.uid || '')) + '">' +
      '<span class="exp-nm">' + nm + '</span>' +
      '<span class="exp-when">' + esc(when) + '</span>' +
      '<span class="exp-days ' + lv + '">' + esc(dayWord) + '</span>' +
      '<span class="exp-amt">' + fmtTok(remain) + '</span>' +
      '<span class="exp-rate">≥' + fmtTok(daily) + '/天</span>' +
      '</div>';
  }).join('');
  // 与 renderExpiry 同款表头：两个数据源切换时列位不跳变。
  const head = '<div class="exp-head"><span>账号</span><span>最近到期</span>' +
    '<span>距到期</span><span>该批积分</span><span>日均需耗</span></div>';
  $('expList').innerHTML = (rows ? head + rows : '') || '<div class="empty">没有账号</div>';
  $('expNote').textContent = list.length + ' 个账号 · 快照（来自本地状态，点「检查」拉实时明细）';
  $('expBox').hidden = false;
}

async function loadExpiry(force) {
  if (!$('expBox')) return;
  if (expFetching) return;
  const fresh = lastPackages && (Date.now() - lastPackagesAt) < EXP_FRESH_MS;
  if (fresh && !force) { renderExpiry(lastPackages); return; }
  expFetching = true;
  $('expBox').hidden = false;
  if (!$('expList').children.length) $('expList').innerHTML = '<div class="empty">查询中…（逐账号向上游实时查询）</div>';
  $('expNote').textContent = '查询中…';
  try {
    const d = await api('packages');
    lastPackages = d;                            // 与「积分构成」视图共用同一份缓存
    lastPackagesAt = Date.now();
    renderExpiry(d);
  } catch (e) {
    $('expNote').textContent = '查询失败：' + esc(e.message);
  }
  expFetching = false;
}

if ($('btnExp')) $('btnExp').onclick = () => loadExpiry(true);

// 「刷新」始终实时查（显式 force，不依赖事件对象真值）
if ($('btnPk')) $('btnPk').onclick = () => loadPackages(true);

// 初始化通知中心
initNotifCenter();

/* ── 命令面板（Ctrl+K） ─────────────────────────────────────────── */
// 跳转命令统一走 cmdGo（与侧栏导航同路径，带 cfgDirty 守卫 + hash 同步）。
function cmdGo(v) {
  if (go(v)) history.pushState(null, '', '#' + v);
}
const CMD_ITEMS = [
  { cat: '跳转', label: '账号池', action: () => cmdGo('accounts') },
  { cat: '跳转', label: '用量', action: () => cmdGo('usage') },
  { cat: '跳转', label: '任务中心', action: () => cmdGo('taskscenter') },
  { cat: '跳转', label: '模型与档位', action: () => cmdGo('models') },
  { cat: '跳转', label: 'API Key 管理', action: () => cmdGo('keys') },
  { cat: '跳转', label: '运行日志', action: () => cmdGo('logs') },
  { cat: '跳转', label: '配置', action: () => cmdGo('config') },
  { cat: '跳转', label: '积分构成', action: () => cmdGo('packages') },
  { cat: '操作', label: '打开 API 调试台', action: () => openPlayground() },
  { cat: '操作', label: '添加账号', action: () => openAdd() },
  { cat: '操作', label: '扫描全部账号待办', action: () => $('btnScanAll') && $('btnScanAll').click() },
  { cat: '操作', label: '查询开学季券码', action: () => $('btnSchoolVouchers') && $('btnSchoolVouchers').click() },
  { cat: '操作', label: '刷新当前页', action: () => $('btnRefresh') && $('btnRefresh').click() },
  { cat: '操作', label: '导出请求 CSV', action: () => $('btnReqExport') && $('btnReqExport').click() },
  { cat: '操作', label: '导出用量 CSV', action: () => $('btnUsExport') && $('btnUsExport').click() },
  { cat: '操作', label: '导出积分 CSV', action: () => $('btnCreditExport') && $('btnCreditExport').click() },
  { cat: '操作', label: '清空通知', action: () => { setNotifs([]); renderNotifList(); renderNotifBadge(); } },
  { cat: '操作', label: '切换明暗主题', action: () => $('btnTheme') && $('btnTheme').click() },
];
let cmdFiltered = [], cmdActive = 0;
function openCmd() { $('cmdOverlay').hidden = false; $('cmdInput').value = ''; renderCmdList(''); setTimeout(() => $('cmdInput').focus(), 10); }
function closeCmd() { $('cmdOverlay').hidden = true; }
function renderCmdList(q) {
  q = (q || '').toLowerCase().trim();
  cmdFiltered = q ? CMD_ITEMS.filter(x => x.label.toLowerCase().includes(q) || x.cat.toLowerCase().includes(q)) : CMD_ITEMS.slice();
  cmdActive = 0;
  $('cmdList').innerHTML = cmdFiltered.length ? cmdFiltered.map((x, i) =>
    `<div class="cmd-item ${i === 0 ? 'active' : ''}" data-i="${i}"><span class="cmd-cat">${x.cat}</span><span>${x.label}</span></div>`
  ).join('') : '<div style="padding:20px;text-align:center;color:var(--ink-3);font-size:13px">无匹配命令</div>';
}
function initCmdPalette() {
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'k') { e.preventDefault(); $('cmdOverlay').hidden ? openCmd() : closeCmd(); }
    if (e.key === 'Escape' && !$('cmdOverlay').hidden) closeCmd();
    if ($('cmdOverlay').hidden) return;
    if (e.key === 'ArrowDown') { e.preventDefault(); cmdActive = Math.min(cmdActive + 1, cmdFiltered.length - 1); updateCmdActive(); }
    if (e.key === 'ArrowUp') { e.preventDefault(); cmdActive = Math.max(cmdActive - 1, 0); updateCmdActive(); }
    if (e.key === 'Enter' && cmdFiltered[cmdActive]) { e.preventDefault(); const item = cmdFiltered[cmdActive]; closeCmd(); item.action(); }
  });
  $('cmdInput').addEventListener('input', () => renderCmdList($('cmdInput').value));
  $('cmdList').addEventListener('click', (e) => {
    const el = e.target.closest('.cmd-item'); if (!el) return;
    const i = Number(el.dataset.i); const item = cmdFiltered[i]; if (!item) return;
    closeCmd(); item.action();
  });
  $('cmdOverlay').addEventListener('click', (e) => { if (e.target === $('cmdOverlay')) closeCmd(); });
}
function updateCmdActive() {
  document.querySelectorAll('.cmd-item').forEach((el, i) => el.classList.toggle('active', i === cmdActive));
  const active = document.querySelector('.cmd-item.active'); if (active) active.scrollIntoView({ block: 'nearest' });
}
initCmdPalette();

/* ── API 调试台（Playground） ────────────────────────────────────────
   对标 new-api / ARP：不离开面板即可验证模型可用 / 鉴权 / 流式（同源 fetch）。 */

let pgModels = null;   // 模型缓存（首次打开拉一次 /panel/api/models）
let pgAbort = null;    // 当前请求的 AbortController（停止按钮用）
let pgBusy = false;    // 发送中标记（防连点）

function pgModelOptions() {
  if (!Array.isArray(pgModels) || !pgModels.length) return '<option value="">模型列表为空</option>';
  // 默认推荐低成本主力（积分优化，对应 .harness/rules/COST-CONTROL.md）：
  // deepseek-v4.1-flash 实测 ~1 积分/M，而目录首个模型未必便宜（hy4 系实测
  // 110~133 积分/M）。找不到时保持第一个（不破坏上游目录顺序语义）。
  const pref = pgModels.find(m => /deepseek-v4\.1-flash$/i.test(m.id)) || pgModels[0];
  const prefId = pref && pref.id;
  // 同名区分：跨域/同域变体共用展示名时追加完整 id。
  return pgModels.map(m => {
    const tip = m.promo_note || m.description || '';
    const dup = pgModels.some(o => o !== m && String(o.name || o.id || '') === String(m.name || m.id || '')) ? m.id : '';
    return '<option value="' + esc(m.id) + '"' + (m.id === prefId ? ' selected' : '') +
      (tip ? ' title="' + esc(tip) + '"' : '') + '>' +
      esc(pgModelLabel(m) + (dup ? ' (' + dup + ')' : '')) + '</option>';
  }).join('');
}

/* pgModelLabel：模型名 · 倍率 · 标签（倍率缺失标「未知」）。 */
function pgModelLabel(m) {
  const parts = [String(m.name || m.id || '')];
  const eff = (m.promo_credits != null && m.promo_credits !== '') ? m.promo_credits : m.credits;
  const rate = String(eff || '').trim();
  parts.push(rate ? rate : '倍率未知');
  const factor = m.promo_factor == null ? null : Number(m.promo_factor);
  const creditsNum = parseFloat(String(m.credits || '').replace(/[^\d.]/g, ''));
  const isFree = factor === 0 || (Number.isFinite(creditsNum) && creditsNum === 0);
  if (isFree) parts.push(m.promo_label || '免费');
  else if (m.promo_label) parts.push(m.promo_label);
  else if (m.supports_images) parts.push('视觉');
  return parts.join(' · ');
}
async function pgLoadModels() {
  const sel = $('pgModel');
  if (!sel) return;
  if (pgModels) { sel.innerHTML = pgModelOptions(); return; }
  try {
    const d = await api('models');
    // /panel/api/models 返回 { models: [...] }；个别版本直接返回数组，兼容两种。
    pgModels = Array.isArray(d.models) ? d.models : (Array.isArray(d) ? d : []);
    sel.innerHTML = pgModelOptions();
  } catch (e) {
    sel.innerHTML = '<option value="">模型加载失败，可重开重试</option>';
  }
}
function openPlayground() {
  $('pgVeil').classList.add('on');
  pgLoadModels();
  setTimeout(() => { const p = $('pgPrompt'); if (p) p.focus(); }, 60);
}
function pgStop() {
  if (pgAbort) { pgAbort.abort(); pgAbort = null; }
}
function pgSetBusy(busy) {
  pgBusy = busy;
  $('btnPgSend').disabled = busy;
  $('btnPgStop').hidden = !busy;
  $('btnPgSend').textContent = busy ? '生成中…' : '发送';
}
function pgAppend(text) {
  const out = $('pgOut');
  out.textContent += text;
  out.scrollTop = out.scrollHeight;
}
function pgSetOut(text, cls) {
  const out = $('pgOut');
  out.textContent = text;
  out.className = 'pg-out' + (cls ? ' ' + cls : '');
}
// SSE 流式解析：fetch ReadableStream → 按行读 "data: {...}" → 逐 delta 追加。
// data 行可能被 chunk 拆成两半，用行缓冲 buf 拼接后再逐行消费。
async function pgStreamRead(resp, startedAt) {
  const reader = resp.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  let usage = null;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line.startsWith('data:')) continue;
      const payload = line.slice(5).trim();
      if (payload === '[DONE]') continue;
      try {
        const obj = JSON.parse(payload);
        const delta = obj.choices && obj.choices[0] && obj.choices[0].delta;
        if (delta && typeof delta.content === 'string') pgAppend(delta.content);
        if (obj.usage) usage = obj.usage;
      } catch { /* 忽略半行 / 非 JSON 的控制行 */ }
    }
  }
  const secs = ((Date.now() - startedAt) / 1000).toFixed(1);
  $('pgMeta').textContent = '流式完成 · ' + secs + 's' +
    (usage ? ' · prompt ' + (usage.prompt_tokens ?? '—') + ' / completion ' + (usage.completion_tokens ?? '—') : '');
}
async function sendPlayground() {
  if (pgBusy) return;
  const model = $('pgModel').value;
  if (!model) { toast('请先选择模型', 'err'); $('pgModel').focus(); return; }
  const prompt = $('pgPrompt').value.trim();
  if (!prompt) { toast('用户消息不能为空', 'err'); $('pgPrompt').focus(); return; }
  const messages = [];
  const sys = $('pgSystem').value.trim();
  if (sys) messages.push({ role: 'system', content: sys });
  messages.push({ role: 'user', content: prompt });
  const body = { model, messages, stream: $('pgStream').value === '1' };
  const temp = Number($('pgTemp').value);
  if (Number.isFinite(temp)) body.temperature = temp;
  const mt = Number($('pgMaxTok').value);
  if (Number.isFinite(mt) && mt > 0) body.max_tokens = mt;

  pgStop();
  pgAbort = new AbortController();
  pgSetBusy(true);
  pgSetOut('', '');
  $('pgMeta').textContent = '等待响应…';
  const startedAt = Date.now();
  const headers = { 'Content-Type': 'application/json' };
  const key = localStorage.getItem(LS_KEY);
  if (key) headers['Authorization'] = 'Bearer ' + key;
  try {
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST', headers, body: JSON.stringify(body), signal: pgAbort.signal,
    });
    if (!resp.ok) {
      const d = await resp.json().catch(() => ({}));
      const msg = d.error && d.error.message ? ' · ' + d.error.message : '';
      pgSetOut('HTTP ' + resp.status + msg, 'err');
      $('pgMeta').textContent = '失败 · ' + ((Date.now() - startedAt) / 1000).toFixed(1) + 's';
      toast('网关返回 ' + resp.status, 'err');
      return;
    }
    if (body.stream) {
      $('pgMeta').textContent = '流式生成中…';
      pgSetOut('', 'streaming');
      await pgStreamRead(resp, startedAt);
    } else {
      const d = await resp.json();
      const content = d.choices && d.choices[0] && d.choices[0].message && d.choices[0].message.content;
      pgSetOut(content || '(无内容返回)', '');
      const secs = ((Date.now() - startedAt) / 1000).toFixed(1);
      const u = d.usage || {};
      $('pgMeta').textContent = '完成 · ' + secs + 's' +
        (u.total_tokens ? ' · prompt ' + (u.prompt_tokens ?? '—') + ' → completion ' + (u.completion_tokens ?? '—') + '（共 ' + u.total_tokens + '）' : '');
    }
  } catch (e) {
    if (isAbort(e)) { pgSetOut('（已停止）', 'err'); $('pgMeta').textContent = '已停止'; }
    else { pgSetOut(e.message, 'err'); $('pgMeta').textContent = '请求失败'; }
  } finally {
    pgAbort = null;
    pgSetBusy(false);
  }
}
if ($('btnPlayground')) $('btnPlayground').onclick = openPlayground;
if ($('btnPgSend')) $('btnPgSend').onclick = sendPlayground;
if ($('btnPgStop')) $('btnPgStop').onclick = pgStop;
if ($('btnPgClose')) $('btnPgClose').onclick = () => { pgStop(); $('pgVeil').classList.remove('on'); };
if ($('btnPgClear')) $('btnPgClear').onclick = () => { pgSetOut('—', ''); $('pgMeta').textContent = '未发送'; };
