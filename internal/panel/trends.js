/* trends.js — 时间序列证据视图（REQ-trends-2026-10-29）。
 *
 * 自包含：不依赖 app.js（与 opsagent.js 同模式，走独立嵌入；app.js 冻结在
 * maxFrontendLines 棘轮上限，新功能一律独立文件）。数据源 /panel/api/trends。
 *
 * 挂载点：用量视图顶部 #trChart / #trAcctBody；进入 usage 视图或点击「刷新」时
 * 按需拉取。不做自动轮询（数据小，现算最干净）。
 *
 * 四态纪律：requests==0 的天不画成功率点（无观测 ≠ 成功率 0%），显示「—」；
 * last_success_known=false = 从未成功（unknown），显示「从未成功」而非「0 次」。
 *
 * 安全：所有动态文本经 esc()；无内联事件（CSP script-src 'self'）；fetch 仅同源。
 */
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function $(id) { return document.getElementById(id); }

  function loadingMsg(text) {
    return '<div class="us-empty" style="color:var(--muted,#888)">' + esc(text) + '</div>';
  }

  function loadErrorHTML(title, msg, retryLabel) {
    return '<div class="us-empty" style="color:var(--err,#e11d48)">' + esc(title) +
      '<span style="opacity:.75">（' + esc(msg) + '）</span>' +
      '<button class="xs" data-retry>' + esc(retryLabel) + '</button></div>';
  }

  async function api(path) {
    const resp = await fetch('/panel/api/' + path, { headers: { 'Accept': 'application/json' } });
    if (resp.status === 401) throw new Error('未授权');
    const text = await resp.text();
    let data = null;
    try { data = JSON.parse(text); } catch (e) { throw new Error('响应不是 JSON（' + resp.status + '）'); }
    if (!resp.ok) throw new Error(data && data.error ? data.error : 'HTTP ' + resp.status);
    return data;
  }

  let trendsData = null;

  async function loadTrends() {
    const host = $('trChart');
    if (!host) return;
    if (!trendsData) {
      host.innerHTML = loadingMsg('正在读取趋势…');
      const n = $('trChartNote'); if (n) n.textContent = '—';
    }
    try {
      const d = await api('trends');
      trendsData = d;
      renderTrends(d);
    } catch (e) {
      trendsData = null;
      host.innerHTML = loadErrorHTML('读取趋势失败', e.message, '重试');
      const n = $('trChartNote'); if (n) n.textContent = '—';
      const t = $('trNote'); if (t) t.textContent = '—';
      const b = host.querySelector('[data-retry]');
      if (b) b.onclick = loadTrends;
    }
  }

  function renderTrends(d) {
    const host = $('trChart');
    const days = d.days || [];
    if (!days.length) {
      host.innerHTML = '<div class="us-empty">暂无归档数据。归档开启并产生请求后再刷新。</div>';
      const n = $('trChartNote'); if (n) n.textContent = '—';
      const t = $('trNote'); if (t) t.textContent = '0 天 · ' + (d.accounts || []).length + ' 个账号';
      renderTrendsAccounts(d.accounts || []);
      return;
    }

    const pts = [];
    for (const p of days) {
      const t = new Date(p.date + 'T00:00:00');
      if (isNaN(t.getTime())) continue;
      pts.push({ t, raw: p.date, req: p.requests || 0, ok: p.succeeded || 0,
                 rate: p.requests ? (p.success_rate || 0) * 100 : null,
                 acct: p.active_accounts || 0 });
    }
    if (!pts.length) return;

    const W = 1200, H = 200, PL = 58, PR = 46, PT = 18, PB = 30;
    const iw = W - PL - PR, ih = H - PT - PB;
    const t0 = pts[0].t, t1 = pts[pts.length - 1].t;
    const span = Math.max(1, t1 - t0);
    const maxReq = Math.max(1, ...pts.map(p => p.req));
    const maxAcct = Math.max(1, ...pts.map(p => p.acct));
    const yMax = Math.max(maxReq, maxAcct);
    const total = pts.reduce((s, p) => s + p.req, 0);
    const avgRate = pts.filter(p => p.rate !== null)
                       .reduce((s, p) => s + p.rate, 0);
    const rateDays = pts.filter(p => p.rate !== null).length;
    const cn = $('trChartNote');
    if (cn) cn.textContent = days.length + ' 天 · 总请求 ' + total + ' · 平均成功率 ' +
      (rateDays ? (avgRate / rateDays).toFixed(1) + '%' : '—') + '（无观测天不参与）';
    const t2 = $('trNote');
    if (t2) t2.textContent = '归档 ' + (d.window || 0) + ' 条 · ' +
      (d.accounts || []).length + ' 个账号';

    const xOf = t => PL + (t - t0) / span * iw;
    const yReq = v => PT + ih - ih * (v / yMax);
    const yRate = v => PT + ih - ih * (v / 100);

    let out = '<svg viewBox="0 0 ' + W + ' ' + H + '" role="img" preserveAspectRatio="xMidYMid meet">';

    const ticksReq = Math.min(4, Math.max(1, Math.round(yMax)));
    for (let k = 0; k <= ticksReq; k++) {
      const v = Math.round(yMax * k / ticksReq);
      const y = yReq(v).toFixed(1);
      out += '<line class="gl" x1="' + PL + '" y1="' + y + '" x2="' + (W - PR) + '" y2="' + y + '" style="opacity:.18"/>';
      out += '<text class="tk" x="' + (PL - 6) + '" y="' + (y + 2) + '" text-anchor="end">' + v + '</text>';
    }
    for (const v of [0, 50, 100]) {
      const y = yRate(v).toFixed(1);
      out += '<text class="tk" x="' + (W - PR + 6) + '" y="' + (y + 4) + '" text-anchor="start" style="opacity:.65">' + v + '%</text>';
    }

    let minGap = Infinity;
    for (let i = 1; i < pts.length; i++) minGap = Math.min(minGap, pts[i].t - pts[i - 1].t);
    if (!isFinite(minGap) || minGap <= 0) minGap = span;
    const slot = iw * (minGap / span);
    const bw = Math.max(2, Math.min(30, slot * 0.7));
    for (const p of pts) {
      const x = xOf(p.t);
      const h = ih * (p.req / yMax);
      const y = PT + ih - h;
      out += '<rect x="' + (x - bw / 2).toFixed(1) + '" y="' + y.toFixed(1) + '" width="' + bw.toFixed(1) +
             '" height="' + h.toFixed(1) + '" rx="2" fill="var(--accent)" opacity=".78"/>';
      const ya = yReq(p.acct);
      out += '<circle cx="' + x.toFixed(1) + '" cy="' + ya.toFixed(1) + '" r="2.6" fill="var(--ok,#22c55e)"/>';
      if (p.rate !== null) {
        const yr = yRate(p.rate).toFixed(1);
        out += '<circle cx="' + x.toFixed(1) + '" cy="' + yr + '" r="3" fill="var(--warn,#f59e0b)"/>';
      }
    }
    let prev = null;
    for (const p of pts) {
      if (p.rate === null) { prev = null; continue; }
      const x = xOf(p.t), y = yRate(p.rate);
      if (prev) out += '<line class="tk" x1="' + prev.x.toFixed(1) + '" y1="' + prev.y.toFixed(1) +
                       '" x2="' + x.toFixed(1) + '" y2="' + y.toFixed(1) + '" stroke="var(--warn,#f59e0b)" stroke-width="1.6" opacity=".85"/>';
      prev = { x, y };
    }

    const TICKS = Math.min(6, pts.length);
    const used = new Set();
    for (let k = 0; k < TICKS; k++) {
      const target = t0 + span * (TICKS === 1 ? 0.5 : k / (TICKS - 1));
      let bi = 0, best = Infinity;
      for (let i = 0; i < pts.length; i++) {
        const dd = Math.abs(pts[i].t - target);
        if (dd < best) { best = dd; bi = i; }
      }
      if (used.has(bi)) continue;
      used.add(bi);
      const p = pts[bi];
      const cx = xOf(p.t);
      const anchor = cx < PL + 16 ? 'start' : (cx > W - PR - 16 ? 'end' : 'middle');
      out += '<text class="tk" x="' + Math.max(PL, Math.min(W - PR, cx)).toFixed(1) +
             '" y="' + (PT + ih + 16) + '" text-anchor="' + anchor + '">' + esc(p.raw.slice(5)) + '</text>';
    }
    out += '</svg>';
    host.innerHTML = out;
    renderTrendsAccounts(d.accounts || []);
  }

  function renderTrendsAccounts(accounts) {
    const head = $('trAcctHead'), body = $('trAcctBody');
    if (!head || !body) return;
    head.innerHTML = '<tr><th>账号</th><th>积分</th><th>将到期</th><th>最早到期日</th>' +
                     '<th>到期余量</th><th>状态</th><th>成功次数</th><th>最近成功</th></tr>';
    if (!accounts.length) {
      body.innerHTML = '<tr><td colspan="8" class="empty">无账号</td></tr>';
      return;
    }
    body.innerHTML = accounts.map(a => {
      const exp = (a.credits_expiring || 0) > 0
        ? '<span class="c-warn">' + (a.credits_expiring || 0) + '</span>'
        : '<span class="c-mute">0</span>';
      const expiry = a.earliest_expiry && a.earliest_expiry !== '0001-01-01T00:00:00Z'
        ? String(a.earliest_expiry).slice(0, 10) : '—';
      const remain = a.earliest_remaining || 0;
      const st = a.disabled ? '<span class="c-err">禁用</span>'
        : a.paused ? '<span class="c-mute">暂停</span>'
        : a.cooling ? '<span class="c-warn">冷却</span>'
        : '<span class="c-ok">健康</span>';
      const ls = a.last_success_known
        ? String(a.last_success).slice(0, 19).replace('T', ' ')
        : '<span class="c-mute">从未成功</span>';
      return '<tr><td class="mono">' + esc(String(a.nickname || a.uid).slice(0, 12)) + '</td>' +
        '<td>' + (a.credits || 0) + '</td><td>' + exp + '</td><td>' + expiry + '</td>' +
        '<td>' + remain + '</td><td>' + st + '</td>' +
        '<td>' + (a.success_count || 0) + '</td><td>' + ls + '</td></tr>';
    }).join('');
  }

  document.addEventListener('DOMContentLoaded', function () {
    const btn = $('btnTrends');
    if (btn) btn.addEventListener('click', loadTrends);
    // 进入用量视图时按需加载：监听 hashchange + 初始 hash。
    function maybeLoad() {
      const h = (location.hash || '#accounts').slice(1);
      if (h === 'usage' && $('trChart')) loadTrends();
    }
    addEventListener('hashchange', maybeLoad);
    maybeLoad();
  });
})();
