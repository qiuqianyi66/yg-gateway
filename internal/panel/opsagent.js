/* opsagent.js — 运维 agent 可观测视图（REQ-opsagent 阶段 2）。
 *
 * 自包含：不依赖 app.js（该文件冻结在 maxFrontendLines 棘轮上限 4378 行，
 * 本文件走 acl_notice.js/savings_note.js 的独立嵌入模式）。
 * 挂载点：账号池视图底部 #opsagentBox；点击「加载/刷新」按需拉取
 * /panel/api/opsagent/status（未知台账 + 最近审计轮），不做自动轮询。
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
      return '<p style="margin:4px 0;color:var(--muted,#888)">无未消解 unknown。</p>';
    }
    var rows = items.map(function (it) {
      return '<tr><td><code>' + esc(String(it.id).slice(0, 12)) + '</code></td>' +
        '<td>' + esc(it.question) + '</td>' +
        '<td>' + esc(it.requires) + '</td>' +
        '<td>' + esc(it.revision) + '</td></tr>';
    }).join('');
    return '<table style="width:100%;border-collapse:collapse;font-size:12px">' +
      '<thead><tr><th style="text-align:left">ID</th><th style="text-align:left">未知</th>' +
      '<th style="text-align:left">消解需要</th><th>rev</th></tr></thead><tbody>' +
      rows + '</tbody></table>';
  }

  function renderRounds(rounds) {
    if (!rounds || !rounds.length) {
      return '<p style="margin:4px 0;color:var(--muted,#888)">暂无审计轮（agent 尚未跑满一轮，或刚启用）。</p>';
    }
    return rounds.slice().reverse().map(function (rd) {
      var sug = (rd.Suggestions || []).map(function (s) {
        return '<li><b>' + esc(s.Rule) + '</b>：' + esc(s.Title) +
          (s.UID ? '（' + esc(String(s.UID).slice(0, 8)) + '）' : '') + '</li>';
      }).join('');
      var rej = (rd.Rejected || []).length
        ? '<p style="margin:4px 0;color:#b58900">被白名单拒绝 ' + esc(rd.Rejected.length) + ' 项（已审计）</p>'
        : '';
      return '<div style="margin:8px 0;padding:6px 8px;border:1px solid var(--border,#444);border-radius:6px">' +
        '<div style="font-size:12px;color:var(--muted,#888)">' + esc(fmtTime(rd.At)) +
        ' · 建议 ' + esc((rd.Suggestions || []).length) + ' · unknown ' + esc((rd.Unknowns || []).length) + '</div>' +
        (sug ? '<ul style="margin:4px 0;padding-left:18px">' + sug + '</ul>' : '') + rej +
        '</div>';
    }).join('');
  }

  function load() {
    var body = document.getElementById('opsagentBody');
    if (!body) return;
    body.textContent = '加载中…';
    fetch('/panel/api/opsagent/status').then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (d) {
      if (!d.enabled) {
        body.innerHTML = '<p style="margin:4px 0;color:var(--muted,#888)">未启用：config.json 加 ' +
          '<code>"opsagent": {"enabled": true}</code> 后重启生效。</p>';
        return;
      }
      body.innerHTML =
        '<p style="margin:4px 0"><b>未消解 unknown（全量，含成本口径与 agent 登记）：</b></p>' +
        renderUnknowns(d.unknowns_open) +
        '<p style="margin:10px 0 4px"><b>最近审计轮（新→旧，观察-建议模式：不自动执行动作）：</b></p>' +
        renderRounds(d.rounds);
    }).catch(function (e) {
      body.textContent = '加载失败：' + esc(e && e.message ? e.message : e);
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('opsagentReload');
    if (btn) btn.addEventListener('click', load);
  });
})();
