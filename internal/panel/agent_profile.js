/* agent_profile.js — 流量画像视图（REQ-agent-profile-ui-2026-10-29，R5）。
 *
 * 自包含：不依赖 app.js（与 trends.js / opsagent.js 同模式，走独立嵌入；app.js
 * 冻结在 maxFrontendLines 棘轮上限，新功能一律独立文件）。数据源 /panel/api/agent_profile。
 *
 * 挂载点：用量视图 #agentProfileBox / #agProfileHost；进入 usage 视图或点击
 * 「刷新」时按需拉取。不做自动轮询（数据小，现算最干净）。
 *
 * 四态纪律：
 *   - unknown 桶（UA 未采集）与 other 桶（UA 未识别）分开展示，禁止合并；
 *   - present:false = 无信号 → 整个区块隐藏（与 /status 整块省略同源，
 *     不显示空态/假数据）；
 *   - cache_miss_unknown 不累加（miss 未知 ≠ miss=0）——数字直接透传，不做兜底。
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

  // 桶名 → 展示标签。unknown 与 other 语义分开：unknown=UA 未采集，other=UA 未识别。
  var AGENT_LABELS = {
    claude_code: 'Claude Code', codex: 'Codex', openai_sdk: 'OpenAI SDK',
    anthropic_sdk: 'Anthropic SDK', browser: '浏览器', http_client: 'HTTP 客户端',
    other: '未识别 UA', unknown: 'UA 未知'
  };
  function agentLabel(k) { return AGENT_LABELS[k] || k; }

  function loadingHTML() {
    return '<div class="us-empty" style="color:var(--muted,#888)">正在读取流量画像…</div>';
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

  async function loadProfile() {
    const box = $('agentProfileBox');
    const host = $('agProfileHost');
    if (!box || !host) return;
    host.innerHTML = loadingHTML();
    const note = $('agProfileNote'); if (note) note.textContent = '—';
    try {
      const d = await api('agent_profile');
      if (!d.present) { box.style.display = 'none'; return; } // 无信号 → 隐藏区块
      box.style.display = '';
      renderProfile(d.profile || {});
      const n = $('agProfileNote');
      if (n) {
        const w = d.profile && d.profile.window ? d.profile.window : null;
        n.textContent = w ? (w.events + ' 条事件 · 最近 1000 条窗口') : '';
      }
    } catch (e) {
      box.style.display = '';
      host.innerHTML = loadErrorHTML('读取流量画像失败', e.message, '重试');
      const b = host.querySelector('[data-retry]');
      if (b) b.onclick = loadProfile;
    }
  }

  // by_agent 汇总：桶名 + 请求数/会话数 + prompt+completion + 缓存命中/未命中。
  function renderByAgent(byAgent) {
    const names = Object.keys(byAgent || {}).sort();
    if (!names.length) {
      return '<div class="us-empty">无 agent 桶数据。</div>';
    }
    return '<table class="acc zebra"><thead><tr><th>Agent</th><th>请求</th>' +
      '<th>会话</th><th>Prompt</th><th>补全</th><th>缓存命中</th><th>缓存未命中</th>' +
      '</tr></thead><tbody>' + names.map(function (k) {
        const b = byAgent[k] || {};
        const label = agentLabel(k) + (k === 'unknown' ? ' <span class="c-mute">（未采集 UA）</span>'
          : k === 'other' ? ' <span class="c-mute">（未识别 UA）</span>' : '');
        return '<tr><td>' + label + '</td>' +
          '<td class="num">' + (b.requests || 0) + '</td>' +
          '<td class="num">' + (b.sessions || 0) + '</td>' +
          '<td class="num">' + (b.prompt_tokens || 0) + '</td>' +
          '<td class="num">' + (b.completion_tokens || 0) + '</td>' +
          '<td class="num c-ok">' + (b.cache_hit_tokens || 0) + '</td>' +
          '<td class="num">' + (b.cache_miss_tokens || 0) + '</td></tr>';
      }).join('') + '</tbody></table>';
  }

  // top_sessions 榜：turns 降序（后端已截断 10）。fp 展示前 8 位。
  function renderTopSessions(top) {
    const rows = top || [];
    if (!rows.length) {
      return '<div class="us-empty">暂无会话级画像（事件无 session_fp）。</div>';
    }
    return '<table class="acc zebra"><thead><tr><th>会话指纹</th><th>Agent</th>' +
      '<th>轮次</th><th>Prompt</th><th>补全</th><th>缓存命中</th><th>缓存未命中</th>' +
      '<th>首见</th></tr></thead><tbody>' + rows.map(function (s) {
        return '<tr><td class="mono">' + esc(String(s.fp || '—').slice(0, 8)) + '</td>' +
          '<td>' + esc(agentLabel(s.agent_kind || 'unknown')) + '</td>' +
          '<td class="num">' + (s.turns || 0) + '</td>' +
          '<td class="num">' + (s.prompt_tokens || 0) + '</td>' +
          '<td class="num">' + (s.completion_tokens || 0) + '</td>' +
          '<td class="num c-ok">' + (s.cache_hit_tokens || 0) + '</td>' +
          '<td class="num">' + (s.cache_miss_tokens || 0) + '</td>' +
          '<td class="mono">' + esc(String(s.first_seen || '').slice(0, 19).replace('T', ' ')) + '</td></tr>';
      }).join('') + '</tbody></table>';
  }

  function renderProfile(prof) {
    const host = $('agProfileHost');
    if (!host) return;
    const byAgent = prof.by_agent || {};
    const top = prof.top_sessions || [];
    const hasAgent = Object.keys(byAgent).length > 0;
    let html = '';
    if (hasAgent) {
      html += '<div class="uschart-hd"><span class="lb">按 Agent 汇总（最近 1000 条 chat 事件）</span></div>' +
        renderByAgent(byAgent);
    }
    if (top.length) {
      html += '<div class="uschart-hd" style="margin-top:14px"><span class="lb">会话榜 Top ' +
        top.length + '（按轮次）</span></div>' + renderTopSessions(top);
    }
    if (!hasAgent && !top.length) {
      html = '<div class="us-empty">暂无流量画像信号。</div>';
    }
    host.innerHTML = html;
  }

  document.addEventListener('DOMContentLoaded', function () {
    const btn = $('btnAgentProfile');
    if (btn) btn.addEventListener('click', loadProfile);
    function maybeLoad() {
      const h = (location.hash || '#accounts').slice(1);
      if (h === 'usage' && $('agProfileHost')) loadProfile();
    }
    addEventListener('hashchange', maybeLoad);
    maybeLoad();
  });
})();
