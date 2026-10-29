/* bench.js — 网关级 benchmark 摘要（REQ-benchmark G4）。
 *
 * 自包含：不依赖 app.js（与 trends.js/opsagent.js 同模式，独立嵌入；
 * app.js 冻结在 maxFrontendLines 棘轮上限）。数据源 /panel/api/benchmark。
 *
 * 挂载点：用量视图 #benchHost；进入 usage 视图或点击「刷新」时按需拉取，
 * 不做自动轮询。三项指标：
 *   - M1 选号到期偏置 pick_expiring_bias（>1 = 快过期优先兑现）
 *   - M2 池健康度 health（7 天成功率 + 健康占比）
 *   - M3 路由成功率 route_success（按模型，样本 <5 = unknown）
 *
 * 四态纪律：kind=unknown 或 samples==0 显示「—/无观测」，不做 0 美化；
 * 每项带 kind 徽标（observation=观测/derivation=派生/inference=推断/unknown=未知）。
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

  const KIND_LABEL = {
    observation: '观测', derivation: '派生',
    inference: '推断', unknown: '未知'
  };
  const KIND_CLS = {
    observation: 'c-ok', derivation: 'c-ok',
    inference: 'c-warn', unknown: 'c-mute'
  };

  // kindBadge kind → 徽标 HTML。
  function kindBadge(kind) {
    const k = kind || 'unknown';
    return '<span class="' + (KIND_CLS[k] || 'c-mute') + '" title="四态：观测=直接记录，派生=由观测计算，推断=样本外推，未知=无观测/样本不足">' +
      esc(KIND_LABEL[k] || k) + '</span>';
  }

  // metricCell 指标值 → 单元格 HTML（unknown/0 样本显示「—」，不做 0 美化）。
  function metricCell(m) {
    if (!m) return '<td class="c-mute">—</td>';
    if (m.kind === 'unknown' || m.samples === 0) {
      return '<td class="c-mute">— <span class="note">' + (m.samples || 0) + ' 样本</span></td>';
    }
    return '<td>' + m.value + ' <span class="note">' + (m.samples || 0) + ' 样本</span></td>';
  }

  let benchData = null;

  async function loadBench() {
    const host = $('benchHost');
    if (!host) return;
    if (!benchData) {
      host.innerHTML = loadingMsg('正在计算 benchmark…');
      const n = $('benchNote'); if (n) n.textContent = '—';
    }
    try {
      const d = await api('benchmark');
      benchData = d;
      renderBench(d);
    } catch (e) {
      benchData = null;
      host.innerHTML = loadErrorHTML('读取 benchmark 失败', e.message, '重试');
      const n = $('benchNote'); if (n) n.textContent = '—';
      const b = host.querySelector('[data-retry]');
      if (b) b.onclick = loadBench;
    }
  }

  // routeTable M3 路由成功率表（unknown 模型排序在后，不美化 0）。
  function routeTable(routing) {
    const rows = Object.keys(routing || {}).sort(function (a, b) {
      const ka = routing[a].kind === 'unknown' ? 1 : 0;
      const kb = routing[b].kind === 'unknown' ? 1 : 0;
      if (ka !== kb) return ka - kb;
      return b.localeCompare(a);
    });
    if (!rows.length) {
      return '<div class="us-empty">暂无带模型字段的请求（无观测）。</div>';
    }
    return '<table class="acc zebra"><thead><tr><th>模型</th><th>成功率</th><th>样本</th><th>四态</th></tr></thead><tbody>' +
      rows.map(function (m) {
        const v = routing[m];
        const val = (v.kind === 'unknown' || v.samples === 0)
          ? '<span class="c-mute">—</span>'
          : (v.value * 100).toFixed(0) + '%';
        return '<tr><td class="mono">' + esc(m) + '</td><td>' + val + '</td>' +
          '<td>' + (v.samples || 0) + '</td><td>' + kindBadge(v.kind) + '</td></tr>';
      }).join('') + '</tbody></table>';
  }

  function renderBench(d) {
    const host = $('benchHost');
    const h = d.health || {};
    const sr = h.success_rate_7d || {};
    const hr = h.healthy_ratio || {};
    const pb = d.pick_expiring_bias || {};
    const note = $('benchNote');
    if (note) {
      note.textContent = '窗口 ' + (d.window_days || 0) + ' 天 · ' +
        (d.events || 0) + ' 条请求 · 零出站零付费（纯离线计算）';
    }
    // 健康占比：samples>0 才显示数值；unknown 显示 —。
    const hrVal = (hr.kind === 'unknown' || hr.samples === 0) ? '—' : (hr.value * 100).toFixed(0) + '%';
    const pbVal = (pb.kind === 'unknown' || pb.samples === 0) ? '—' : pb.value;

    host.innerHTML =
      '<table class="acc zebra"><thead><tr><th>指标</th><th>当前值</th><th>说明</th><th>四态</th></tr></thead><tbody>' +
      '<tr><td><b>M1 选号到期偏置</b></td>' +
        '<td>' + (pb.kind === 'unknown' ? '<span class="c-mute">—</span>' : '<b>' + pbVal + '</b>') +
          ' <span class="note">' + (pb.samples || 0) + ' 账号</span></td>' +
        '<td class="note">被选账号平均将到期积分 / 池内平均，&gt;1 = 快过期优先兑现</td>' +
        '<td>' + kindBadge(pb.kind) + '</td></tr>' +
      '<tr><td><b>M2 成功率（7 天）</b></td>' +
        '<td>' + (sr.kind === 'unknown' ? '<span class="c-mute">—</span>' : (sr.value * 100).toFixed(0) + '%') +
          ' <span class="note">' + (sr.samples || 0) + ' 请求</span></td>' +
        '<td class="note">reqlog 观测成功 / 总（7 天窗口）</td>' +
        '<td>' + kindBadge(sr.kind) + '</td></tr>' +
      '<tr><td><b>M2 健康账号占比</b></td>' +
        '<td>' + (hr.kind === 'unknown' ? '<span class="c-mute">—</span>' : '<b>' + hrVal + '</b>') +
          ' <span class="note">' + (hr.samples || 0) + ' 账号</span></td>' +
        '<td class="note">非禁用/暂停/冷却（账号现态观测）</td>' +
        '<td>' + kindBadge(hr.kind) + '</td></tr>' +
      '</tbody></table>' +
      '<div class="uschart-hd" style="margin-top:10px"><span class="lb">M3 路由成功率（按模型）</span>' +
      '<span class="grow"></span><span class="note">样本 &lt;5 = 未知（推断不作数）</span></div>' +
      routeTable(d.route_success || {});
  }

  document.addEventListener('DOMContentLoaded', function () {
    const btn = $('btnBench');
    if (btn) btn.addEventListener('click', loadBench);
    // 进入用量视图时按需加载（与 trends.js 同模式）。
    function maybeLoad() {
      const h = (location.hash || '#accounts').slice(1);
      if (h === 'usage' && $('benchHost')) loadBench();
    }
    addEventListener('hashchange', maybeLoad);
    maybeLoad();
  });
})();
