/* opsagent.js — 运维 agent 可观测视图（REQ-opsagent 阶段 2）。
 *
 * 自包含：不依赖 app.js（该文件冻结在 maxFrontendLines 棘轮上限 4378 行，
 * 本文件走 acl_notice.js/savings_note.js 的独立嵌入模式）。
 * 挂载点：**独立视图** `#view-opsagent`（index.html 第 8 个 nav tab）里的
 * `#opsagentBody`；点击 `#opsagentReload` 按需拉取 /panel/api/opsagent/status
 * （未知台账 + 最近审计轮），不做自动轮询。
 * 注：早期版本曾计划挂在账号池视图底部的 #opsagentBox，该元素从未落地——
 * e2e 的 smoke.spec.ts 一度按旧设计断言它，2026-11-17 已改为断言独立视图。
 * 安全：所有动态文本经 esc()（工程结构 §三：渲染前必须转义）；无内联事件
 * （CSP script-src 'self'）；fetch 仅同源（connect-src 'self'）。
 */
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function fmtTime(iso) {
    var d = new Date(iso);
    return isNaN(d.getTime()) ? esc(iso) : d.toLocaleTimeString();
  }

  function renderUnknowns(items) {
    if (!items || !items.length) {
      return '<div class="us-empty">无未消解 unknown。</div>';
    }
    var rows = items.map(function (it) {
      return '<tr><td class="mono">' + esc(String(it.id).slice(0, 12)) + '</td>' +
        '<td>' + esc(it.question) + '</td>' +
        '<td>' + esc(it.requires) + '</td>' +
        '<td class="num">' + esc(it.revision) + '</td></tr>';
    }).join('');
    /* 复用账号池的 .acc 表格样式（此前是内联样式，跟全站表格不一个字体/行高）。 */
    return '<div class="tbl-wrap"><table class="acc"><thead><tr><th>ID</th><th>未知</th>' +
      '<th>消解需要</th><th>rev</th></tr></thead><tbody>' + rows + '</tbody></table></div>';
  }

  function renderRounds(rounds) {
    if (!rounds || !rounds.length) {
      return '<div class="us-empty">暂无审计轮（agent 尚未跑满一轮，或刚启用）。</div>';
    }
    return rounds.slice().reverse().map(function (rd) {
      var sug = (rd.Suggestions || []).map(function (s) {
        return '<li><b>' + esc(s.Rule) + '</b>：' + esc(s.Title) +
          (s.UID ? '（' + esc(String(s.UID).slice(0, 8)) + '）' : '') + '</li>';
      }).join('');
      var rej = (rd.Rejected || []).length
        ? '<div class="note c-warn">被白名单拒绝 ' + esc(rd.Rejected.length) + ' 项（已审计）</div>'
        : '';
      return '<div class="ops-round">' +
        '<div class="note">' + esc(fmtTime(rd.At)) +
        ' · 建议 ' + esc((rd.Suggestions || []).length) + ' · unknown ' + esc((rd.Unknowns || []).length) + '</div>' +
        (sug ? '<ul class="ops-list">' + sug + '</ul>' : '') + rej +
        '</div>';
    }).join('');
  }

  function renderRuntime(rt, sessions) {
    if (!rt) {
      return '<div class="us-empty">runtime 未启用（规则引擎照常跑）。在 config.json 的 ' +
        '<code>opsagent.agent</code> 里置 <code>enabled=true</code> 并配置免费模型后重启。</div>';
    }
    var head =
      '<div class="note">model=' + esc(rt.model || '') +
      ' · session=' + esc(String(rt.session_id || '').slice(0, 24)) +
      ' · 最近 turn=' + esc(rt.last_turn || 0) +
      ' · outcome=' + esc(rt.last_outcome || 'none') +
      ' · steps=' + esc(rt.last_steps || 0) +
      ' · tools=' + esc(rt.last_tool_calls || 0) +
      ' · 会话数=' + esc(rt.sessions || 0) + '</div>';
    if (!sessions || !sessions.length) return head + '<div class="us-empty">暂无 session。</div>';
    var rows = sessions.slice(0, 8).map(function (s) {
      return '<tr><td class="mono">' + esc(String(s.id || '').slice(0, 24)) + '</td>' +
        '<td class="num">' + esc(s.turns || 0) + '</td>' +
        '<td class="num">' + esc(s.events || 0) + '</td>' +
        '<td>' + esc(s.last_outcome || '') + '</td>' +
        '<td class="note">' + esc(fmtTime(s.updated_at)) + '</td></tr>';
    }).join('');
    return head + '<div class="tbl-wrap"><table class="acc"><thead><tr><th>session</th>' +
      '<th>turns</th><th>events</th><th>last</th><th>updated</th></tr></thead><tbody>' +
      rows + '</tbody></table></div>';
  }

  function load() {
    var body = document.getElementById('opsagentBody');
    if (!body) return;
    body.textContent = '加载中…';
    // api_key 鉴权下必须带 Bearer（app.js LS_KEY='yg-gateway.key'，此处硬编码保持同步；
    // 自包含不依赖 app.js，若 app.js 改键名需同步改这里）。
    var k = null;
    try { k = localStorage.getItem('yg-gateway.key'); } catch (e) { k = null; }
    var hdr = k ? { 'Authorization': 'Bearer ' + k } : {};
    fetch('/panel/api/opsagent/status', { headers: hdr }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (d) {
      if (!d.enabled) {
        body.innerHTML = '<div class="us-empty">未启用：config.json 加 ' +
          '<code>"opsagent": {"enabled": true}</code> 后重启生效。</div>';
        return;
      }
      body.innerHTML =
        '<h4 class="ops-h">运维 agent runtime（只读诊断，不自动执行）</h4>' +
        renderRuntime(d.runtime, d.runtime_sessions) +
        '<h4 class="ops-h">未消解 unknown（全量，含成本口径与 agent 登记）</h4>' +
        renderUnknowns(d.unknowns_open) +
        '<h4 class="ops-h">最近审计轮（新→旧，观察-建议模式：不自动执行动作）</h4>' +
        renderRounds(d.rounds);
    }).catch(function (e) {
      body.textContent = '加载失败：' + esc(e && e.message ? e.message : e);
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('opsagentReload');
    if (btn) btn.addEventListener('click', load);
    /* 进视图即加载：本卡原来必须手点「加载 / 刷新」，切过来看到的是一片空白 + 一句提示。
       与 trends/agent_profile/bench 同样的坑——app.js 导航走 pushState 不触发 hashchange，
       所以这里同时挂 hashchange（前进后退）与导航点击（捕获阶段）。 */
    function maybeLoad() {
      var h = (location.hash || '#accounts').slice(1);
      if (h === 'opsagent' && document.getElementById('opsagentBody')) load();
    }
    addEventListener('hashchange', maybeLoad);
    document.addEventListener('click', function (e) {
      var a = e.target && e.target.closest ? e.target.closest('a[data-view]') : null;
      if (a && a.dataset.view === 'opsagent') setTimeout(maybeLoad, 0);
    }, true);
    maybeLoad();
  });
})();
