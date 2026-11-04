/* app_views_accounts.js — 账号池视图（从 app.js 拆出的第七个组件，2026-11-02）
   ────────────────────────────────────────────────────────────────────────────────
   内容：账号表渲染与签名去抖、模型锁池表、账号操作（签到/余额/解冻/禁用/暂停/移除）。

   纪律：**只放函数声明**——本文件排在 app.js 之前加载（app.js 顶层路由会调用 renderAccounts）。
   顶层事件绑定与状态（accRenderSig / mlRenderSig / mdQTimer 及各 $('x').onclick）
   仍留在 app.js：它们引用 `$`，提前加载会在 `$` 初始化前执行 → 白屏。

   依赖（运行时解析）：app_core.js 的 esc/ago/dur/fmtTok；app.js 的 $/api/toast/状态。 */
/* ── 账号池 ───────────────────────────────────────────────────────── */
/* renderAccounts 签名短路：内容没变就不重建 DOM。
 *
 * 为什么：面板每 5s 轮询一次 overview（refreshVisible），此前无条件走
 * tb.innerHTML = ... 全量重绘。后果——
 *   ① .acc tbody tr:hover 的悬停高亮每 5 秒被抹掉一次；
 *   ② 用户正在表格里选中 uid 想复制时，选区随重绘消失；
 *   ③ 行 title 气泡（成本台账 tip）悬停到一半被重建，提示闪断；
 *   ④ 账号多时每 5 秒重建几十行 DOM，肉眼可见抖动。
 * 签名比对让"数据没变"的绝大多数 tick 直接短路（零 DOM 操作）。
 * 另加一条：用户正在表内交互（有焦点）时本轮直接跳过，避免打断输入/选择。
 * 注意：签名必须覆盖**所有会影响渲染的字段**，否则该更新的不更新（比抖动更糟）。 */
function accountsSig(list) {
  return (list || []).map(s => [
    s.uid, s.nickname, s.realm, s.enterprise ? 1 : 0,
    s.disabled ? 1 : 0, s.paused ? 1 : 0, s.checkin_done ? 1 : 0,
    s.credits, s.credits_total, s.success_count, s.err_total, s.in_flight,
    Math.round(s.cool_remaining_sec || 0),
    s.breaker_until || '', s.degrade_until || '', s.cool_kind || '',
    s.reason || '', s.last_success || '',
    (s.rate_limited_models || []).map(r => r.model + ':' + (r.kind || '') + ':' + (r.reset_at || '')).join(','),
    JSON.stringify(s.token_usage || {}),
    (s.model_costs || []).map(c => c.model + ':' + c.cost_per_1k).join(','),
  ].join('\u0001')).join('\u0002');
}
/* unkBadge 未知成本徽章的纯函数形态（AGENTS.md「Unknown 消解纪律」）。
 * 空 / 字段缺失 → ''（不占位，也不显示"0 未知"）；有值 → 一枚可悬停的 warn 徽章，
 * title 里逐条给 question + requires（需要什么才能消解）。
 * 为什么单独抽出来：这段拼接此前内联在 ~80 行的 renderAccountsUnsafe 里，只能靠人眼看；
 * 抽成纯函数后由 frontend_test.go TestAppJSUnknownBadgeAndNote 直接断言（含转义）。
 * 禁止用 `!!unk` 之类的真假判断替代 length —— 未知不是"没有"（四态纪律）。 */
function unkBadge(unknowns) {
  const unk = unknowns || [];
  if (!unk.length) return '';
  const tip = esc(unk.map(u => u.question + '\n需要：' + u.requires).join('\n\n'));
  return '<span class="tag warn" title="' + tip + '" style="cursor:help">' + unk.length + ' 未知成本</span> ';
}

/* unkNoteText 账号池提示行里的未知成本一句话（纯函数）。
 * 0 条 → ''（不显示）；>0 → 「N 个 (账号,模型) 成本未知，悬停账号卡片查看明细」。
 * 明细在账号卡片悬停里，这里只给入口，不复述。 */
function unkNoteText(accounts) {
  // s 可能是 null（接口数组里混进 null 时 reduce 会读到 undefined.unknowns）。
  // 这里恒返回字符串，绝不抛错：它是每轮刷新都会跑的旁支路径，抛错会连带整页不刷新。
  const n = (accounts || []).reduce((a, s) => a + (((s && s.unknowns) || []).length), 0);
  return n ? n + ' 个 (账号,模型) 成本未知，悬停账号卡片查看明细' : '';
}

function renderAccounts(list) {
  const tb = $('accBody');
  // 用户正在表内交互（聚焦/选中）时跳过本轮：重建会打断选择与输入。
  if (document.activeElement && tb.contains(document.activeElement)) return;
  const sig = accountsSig(list);
  if (sig === accRenderSig) return; // 数据无变化：不碰 DOM
  accRenderSig = sig;
  renderAccountsUnsafe(list);
}
function renderAccountsUnsafe(list) {
  const tb = $('accBody');
  if (!tb) return;
  if (!list.length) {
    tb.innerHTML = '<div class="empty" style="grid-column:1/-1"><div class="big">账号池是空的</div>点击右上角「添加账号」，用浏览器登录一个 WorkBuddy 账号</div>';
    return;
  }
  const maxCred = Math.max(1, ...list.map(s => s.credits || 0));
  tb.innerHTML = list.map(s => {
    const bl = (new Date(s.breaker_until || 0) - Date.now()) / 1000;
    const dg = (new Date(s.degrade_until || 0) - Date.now()) / 1000;
    const cool = Math.max(s.cool_remaining_sec || 0, bl > 0 ? bl : 0, dg > 0 ? dg : 0);
    let cls = '', tag;
    if (s.disabled) { cls = 'off'; tag = '<span class="tag bad">已禁用</span>'; }
    else if (s.paused) { cls = 'off'; tag = '<span class="tag warn">已暂停</span>'; }
    else if (cool > 0) {
      cls = 'off';
      const kind = bl > Math.max(s.cool_remaining_sec || 0, dg > 0 ? dg : 0) ? '熔断'
        : (dg > (s.cool_remaining_sec || 0) ? '连败降权' : (s.cool_kind === 'hard_credit' ? '积分冷却' : '限流冷却'));
      tag = '<span class="tag warn">' + kind + ' ' + dur(cool) + '</span>';
    } else tag = '<span class="tag ok">可用</span>';
    const short = s.uid.length > 16 ? s.uid.slice(0, 16) + '…' : s.uid;
    const unlimited = s.credits_total === -1;
    const cred = unlimited ? '不限'
      : (s.credits == null ? '—' : (s.credits_total > 0 ? s.credits + '<span class="of">/' + s.credits_total + '</span>' : String(s.credits)));
    const pct = unlimited ? 100
      : (s.credits_total > 0
        ? Math.min(100, Math.round((s.credits || 0) / s.credits_total * 100))
        : Math.round((s.credits || 0) / maxCred * 100));
    const frozen = s.disabled || cool > 0;
    const tu = s.token_usage || {};
    const req = tu.request_count || 0;
    const totalTok = formatTokenCount(tu.total_tokens);
    const latency = formatLatency(tu.last_latency_ms);
    const rate = formatRate(tu.last_tokens_per_second);
    const avatar = (s.nickname || '?').charAt(0).toUpperCase();
    const realmTag = s.realm === 'global' ? '<span class="realm-tag">国际</span>' : (s.enterprise ? '<span class="realm-tag">企业</span>' : '');
    const groupTag = s.group ? '<span class="realm-tag" style="background:var(--accent,#4a9)">组:' + esc(s.group) + '</span>' : '';
    // unknown 成本徽章（AGENTS.md「Unknown 消解纪律」）：该账号还有哪些 (账号,模型)
    // 的实际成本没摸清。正常运营下多为 0——有值即提示"这条未知还欠一次真实观测"，
    // 悬停看 question + requires（需要什么才能消解）。数据来自 overview→accounts→unknowns。
    // 拼接逻辑抽在 unkBadge()（纯函数，可被 JS 级测试直接调用）。
    const unkTag = unkBadge(s.unknowns);
    return '<div class="acc-card ' + cls + '" title="uid: ' + esc(s.uid) + '">' +
      '<div class="acc-card-head">' +
        '<div class="acc-avatar">' + esc(avatar) + '</div>' +
        '<div><div class="acc-name">' + (s.nickname ? esc(s.nickname) : '<span style="color:var(--ink-3)">未命名</span>') + ' ' + realmTag + ' ' + groupTag + '</div>' +
        '<div class="acc-uid">' + esc(short) + '</div></div>' +
        '<div class="acc-status">' + unkTag + tag + '</div>' +
      '</div>' +
      '<div class="acc-cred"><div class="n">' + cred + '</div><div class="bar"><i style="width:' + pct + '%"></i></div></div>' +
      '<div class="acc-stats">' +
        '<div class="acc-stat"><div class="v">' + (function(){var o=s.success_count||0,e=s.err_total||0,t=o+e;return t>0?Math.round(o/t*100)+'%':'—';})() + '</div><div class="k">成功率</div></div>' +
        '<div class="acc-stat"><div class="v">' + (s.in_flight || 0) + '</div><div class="k">在途</div></div>' +
        '<div class="acc-stat"><div class="v">' + req + '</div><div class="k">次数</div></div>' +
        '<div class="acc-stat"><div class="v">' + totalTok + '</div><div class="k">Token</div></div>' +
      '</div>' +
      '<div class="acc-acts">' +
        (s.enterprise ? '' : '<button class="xs ghost" data-a="checkin" data-u="' + esc(s.uid) + '">' + (s.checkin_done ? '已签' : '签到') + '</button>') +
        '<button class="xs ghost" data-a="balance" data-u="' + esc(s.uid) + '">' + (s.enterprise ? '额度' : '余额') + '</button>' +
        (s.enterprise ? '' : '<button class="xs ghost" data-a="tasks" data-u="' + esc(s.uid) + '">任务</button>') +
        (frozen ? '<button class="xs primary" data-a="revive" data-u="' + esc(s.uid) + '">解冻</button>'
                : (s.paused ? '<button class="xs primary" data-a="resume" data-u="' + esc(s.uid) + '">恢复</button>'
                            : '<button class="xs ghost" data-a="pause" data-u="' + esc(s.uid) + '">暂停</button>')) +
        (s.disabled ? '' : '<button class="xs ghost" data-a="disable" data-u="' + esc(s.uid) + '">禁用</button>') +
        '<button class="xs ghost danger" data-a="remove" data-u="' + esc(s.uid) + '">移除</button>' +
      '</div>' +
    '</div>';
  }).join('');
}

// renderModelLocks 模型锁池：哪些模型不能用、锁了几个号、还要锁多久。
// 后端 model_locks 已按「整池不可用 → 没号可用 → 部分限流」排好序，这里只做展示。
// 同样加签名短路：该表多数时候为空或不变，无需每 5 秒重建（与 renderAccounts 同理）。
function renderModelLocks(rows) {
  const tb = $('mlBody');
  if (!tb) return;
  const sig = JSON.stringify(rows || []);
  if (sig === mlRenderSig) return; // 无变化：不碰 DOM
  mlRenderSig = sig;
  renderModelLocksUnsafe(rows);
}
function renderModelLocksUnsafe(rows) {
  const tb = $('mlBody');
  if (!tb) return;
  const note = $('mlNote');
  if (!rows || !rows.length) {
    tb.innerHTML = '<tr><td colspan="8"><div class="empty">当前没有模型级限流 —— 所有模型均可选</div></td></tr>';
    if (note) note.textContent = '';
    return;
  }
  const STATE = { locked: ['bad', '整池不可用'], starved: ['warn', '没号可用'], partial: ['warn', '部分限流'] };
  const left = iso => {
    const ms = parseAPITime(iso);
    return ms ? dur(Math.max(0, Math.round((ms - Date.now()) / 1000))) : '—';
  };
  tb.innerHTML = rows.map(r => {
    const st = STATE[r.state] || ['mute', r.state || '—'];
    const realm = r.realm === 'global' ? '国际版' : '国内版';
    return '<tr>' +
      '<td>' + esc(r.model) + '</td>' +
      '<td><span class="realm-tag">' + realm + '</span></td>' +
      '<td><span class="tag ' + st[0] + '">' + st[1] + '</span></td>' +
      '<td class="num">' + (r.servable || 0) + ' / ' + (r.total || 0) + '</td>' +
      '<td class="num">' + (r.locked || 0) + '</td>' +
      '<td class="num">' + left(r.unlock_at || r.fully_unlock_at) + '</td>' +
      '<td class="num">' + left(r.fully_unlock_at) + '</td>' +
      '<td>' + (r.reason ? '<div class="note">' + esc(r.reason) + '</div>' : '—') + '</td>' +
      '</tr>';
  }).join('');
  if (note) {
    const bad = rows.filter(r => r.state === 'locked' || r.state === 'starved').length;
    note.textContent = bad ? bad + ' 个模型整池不可用' : rows.length + ' 个模型部分限流';
  }
}

async function loadOverview(quiet) {
  // 首屏加载骨架：账号表给行骨架，统计卡保持 HTML 里的「-」但补一条加载提示。
  if (!overviewData) {
    const tb = $('accBody');
    if (tb && !tb.children.length) tb.innerHTML = '<tr><td colspan="9">' + skelRows(5, 7) + '</td></tr>';
  }
  // 防慢响应覆盖：新一轮开始时取消上一轮未完成的 overview 请求。
  const ctl = abortable('overview');
  try {
    const d = await api('overview', { signal: ctl.signal });
    overviewData = d;
    // F6：key RPM 实况槽就地刷新（每 5s 轮询路径，不整表重渲）。
    refreshRpmLive(d.key_rpm);
    // 统计卡数字滚动（游戏化层 setCount）：整数前缀滚上去，其余文本原样保留；
    // reduced-motion / 无 rAF 环境自动降级为直接落值。
    setCount('sTotal', d.total);
    setCount('sHealthy', d.healthy);
    setCount('sCooling', d.cooling);
    setCount('sDisabled', d.disabled);
    // 暂停选号单列（issue #125）：只关选号、照常签到保活，与禁用不同。
    if ($('sPaused')) setCount('sPaused', d.paused == null ? '-' : d.paused);
    const remSum = (d.accounts || []).reduce((a, s) => a + (s.credits || 0), 0);
    const totSum = (d.accounts || []).reduce((a, s) => a + (s.credits_total || 0), 0);
    setCount('sCredits', totSum > 0 ? remSum + ' / ' + totSum : remSum);
    // 能量环：积分占比一眼可见。总额缺失（含不限量账号）时占比未知，环留空不臆造。
    const ringEl = $('statCredits');
    if (ringEl) {
      const pct = totSum > 0 ? Math.min(100, Math.round(remSum / totSum * 100)) : 0;
      ringEl.style.setProperty('--ring', pct + '%');
    }
    setCount('sSticky', d.sticky_sessions);
    // 粘性会话详情列表：join accounts 数据显示昵称 + 成本层级。
    const sl = d.sticky_list || [];
    const stickyBox = $('stickyBox'), stickyList = $('stickyList');
    if (stickyBox && stickyList) {
      if (!sl.length) { stickyBox.hidden = true; }
      else {
        stickyBox.hidden = false;
        const accMap = {};
        (d.accounts || []).forEach(a => { accMap[a.uid || a.UID] = a; });
        stickyList.innerHTML = sl.map(b => {
          const a = accMap[b.uid] || {};
          const nick = a.nickname || a.Nickname || b.uid;
          // cost_tier: 0=免费(绿), 1=未知(灰), 2=收费(红)。accounts 里可能没有 cost_tier 字段，默认灰。
          const tier = a.cost_tier != null ? a.cost_tier : 1;
          const tierColor = tier === 0 ? 'var(--ok)' : tier === 2 ? 'var(--bad)' : 'var(--ink-3)';
          const tierLabel = tier === 0 ? '免费' : tier === 2 ? '收费' : '未知';
          const when = b.last_active ? new Date(b.last_active).toLocaleString('zh-CN', { hour12: false }) : '—';
          return `<div style="display:flex;align-items:center;gap:10px;padding:8px 0;border-bottom:1px solid var(--line-soft)">
            <i style="width:8px;height:8px;border-radius:50%;background:${tierColor};flex:none"></i>
            <span style="font-weight:550">${esc(nick)}</span>
            <span style="color:var(--ink-3);font-size:12px">(${esc(b.uid)})</span>
            <span style="background:${tierColor}22;color:${tierColor};padding:2px 8px;border-radius:4px;font-size:11px">${tierLabel}</span>
            <span style="flex:1"></span>
            <span style="color:var(--ink-3);font-size:11px;font-family:var(--mono)">${when}</span>
          </div>`;
        }).join('');
      }
    }
    $('navSub').textContent = 'v' + d.version;
    $('navVer').textContent = 'v' + d.version;
    $('navRedis').textContent = d.redis_mode === 'upstash' ? 'Redis 镜像' : '本地内存';
    $('navState').textContent = d.healthy > 0 ? '服务正常' : (d.total ? '无可用账号' : '待添加账号');
    const p = $('navPulse');
    p.className = 'pulse' + (d.healthy > 0 ? '' : (d.total ? ' warn' : ' bad'));
    // 账号池提示行：在途占满 + 未知成本总数（明细在账号卡片悬停里）。
    // 用 innerHTML（不再 textContent）：本行现在包含代理状态高亮（failed 红），
    // 其余提示文本一律经 esc() 转义，不引入注入面。
    const accNotes = [];
    if (d.in_flight_full) accNotes.push(esc(d.in_flight_full + ' 个账号在途占满'));
    const unkNote = unkNoteText(d.accounts);
    if (unkNote) accNotes.push(esc(unkNote));
    // 代理状态观测（P0-2 三态：direct/active/failed；failed=配置错误，出站必失败）。
    // overview 接线后恒输出 proxy_status（未接线省略键）；{} = 无代理配置，不打扰。
    // failed 高亮 + 悬停出明细：运维开面板即见，不必翻 /status。
    const ps = d.proxy_status;
    if (ps) {
      const pEntries = Object.entries(ps);
      if (pEntries.length) {
        const pFailed = pEntries.filter(([, s]) => s === 'failed');
        const pActive = pEntries.length - pFailed.length;
        let pLabel = '';
        if (pFailed.length) {
          const detail = pFailed.map(([u]) => u).join(' · ');
          pLabel = '<span style="color:var(--bad)" title="' + esc(detail) + '">' + pFailed.length
            + ' 个代理 failed</span>（配置错误，出站必失败）';
        } else if (pActive) {
          pLabel = pActive + ' 个代理 active';
        }
        if (pLabel) accNotes.push('代理：' + pLabel);
      }
    }
    $('accNote').innerHTML = accNotes.join(' · ');
    const up = Math.floor(d.uptime_sec);
    $('subMeta').textContent = '运行 ' + (up >= 86400 ? Math.floor(up / 86400) + ' 天 ' : '') + Math.floor(up % 86400 / 3600) + ' 时 ' + Math.floor(up % 3600 / 60) + ' 分';
    renderAccounts(d.accounts || []);
    renderModelLocks(d.model_locks);
    // 今日用量 KPI：并行拉 24h 窗口，失败静默（旁支指标不打扰）。
    (async () => {
      try {
        const u = await api('usage?hours=24');
        if ($('sTodayReq')) setCount('sTodayReq', Number(u.totals && u.totals.requests || 0));
        if ($('sTodayTok')) $('sTodayTok').textContent = fmtTok(u.totals && u.totals.total_tokens || 0);
      } catch (_) { /* 旁支指标失败不打扰 */ }
    })();
    // 到期卡片跟随首页统计同步刷新（同一份响应快照，不额外发请求）。
    if ($('expBox') && !lastPackages) renderExpirySnapshot(d);
  } catch (e) {
    // 本轮被下一轮 abort（慢响应作废）：静默返回，不报错也不改 UI。
    if (isAbort(e)) return;
    // 首屏失败此前完全静默（quiet=true 时一个字都不提示），7 个统计卡永远停在
    // HTML 里的「-」、账号表空白，用户只能猜。这里区分首次与轮询：
    //   首次（还没拿到过数据）→ 表内插持久化错误条 + 就地重试；
    //   轮询失败 → 保持安静（避免每 5 秒弹一次 toast 刷屏）。
    if (!overviewData) {
      const tb = $('accBody');
      if (tb) {
        tb.innerHTML = '<tr><td colspan="9">' +
          loadErrorHTML('读取账号池失败', e.message, '重试') + '</td></tr>';
        bindRetry(tb, () => loadOverview(false));
      }
    }
    if (!quiet) toast(e.message, 'err');
  } finally {
    ctl.done();
  }
}



/* ── 模型视图已拆到 app_views_models.js（2026-11-02 组件库拆分第 4 步）────────────
   只放函数声明、先于 app.js 加载；修改请去 app_views_models.js。 */

/* ── 模型筛选（按条件查询）───────────────────────────────────────────
   模型目录一次拉全（几十条），筛选与排序全部在前端完成：改条件零延迟，且不会
   因为调一次筛选就打一次上游——/panel/api/models 是直连上游的实时查询，很贵。
   条件之间是 AND；每个条件为空即不参与判定。 */
// mdRateValue 当前生效的积分倍率数值：优先促销价（限时免费 = 0），无倍率记为
// Infinity 排到最后（排序时"没有价格"不该冒充最便宜）。
function mdRateValue(m) {
  const raw = (m.promo_credits != null && m.promo_credits !== '') ? m.promo_credits : m.credits;
  const n = parseFloat(String(raw == null ? '' : raw).replace(/[^\d.]/g, ''));
  return Number.isFinite(n) ? n : Infinity;
}

// mdSearchText 参与关键字搜索的字段（ID / 展示名 / 厂商 / 描述 / 标签）。
function mdSearchText(m) {
  return [m.id, m.name, m.vendor, m.description, (m.tags || []).join(' ')]
    .filter(Boolean).join(' ').toLowerCase();
}

// mdMatch 单个模型是否满足全部筛选条件。
function mdMatch(m, f) {
  f = f || mdFilter;
  if (f.q) {
    const text = mdSearchText(m);
    // 空格分词后逐个匹配：多关键词是 AND，便于"cn 视觉"这类组合查询。
    for (const kw of f.q.toLowerCase().split(/\s+/).filter(Boolean)) {
      if (!text.includes(kw)) return false;
    }
  }
  if (f.realm && !String(m.id || '').startsWith(f.realm + ':')) return false;
  // 能力筛选按**三值**处理：只有显式 false 才排除。上游精简目录会省略字段，
  // 缺失=未知（AGENTS.md 四态纪律），把未知当否会把实际可用的模型藏掉且不报错。
  if (f.cap === 'tool' && m.supports_tool_call === false) return false;
  if (f.cap === 'vision' && m.supports_images === false) return false;
  if (f.cap === 'reasoning' && m.supports_reasoning === false) return false;
  if (f.cap === 'default' && !m.is_default) return false;
  if (f.effort === 'off') {
    if (m.can_disable_thinking === false) return false;
  } else if (f.effort && !(m.supported_efforts || []).includes(f.effort)) {
    return false;
  }
  const factor = m.promo_factor == null ? null : Number(m.promo_factor);
  if (f.promo === 'promo' && factor == null && !m.promo_label) return false;
  if (f.promo === 'free' && !(factor === 0)) return false;
  if (f.promo === 'discount' && !(factor != null && factor > 0)) return false;
  return true;
}

// mdSortList 按当前排序条件返回新数组（不改动入参，保持上游原始顺序可回溯）。
function mdSortList(list, f) {
  f = f || mdFilter;
  const out = list.slice();
  const num = v => { const n = Number(v || 0); return Number.isFinite(n) ? n : 0; };
  if (f.sort === 'rate') out.sort((a, b) => mdRateValue(a) - mdRateValue(b));
  else if (f.sort === 'context') out.sort((a, b) => num(b.context_length) - num(a.context_length));
  else if (f.sort === 'output') out.sort((a, b) => num(b.max_output_tokens) - num(a.max_output_tokens));
  else if (f.sort === 'name') out.sort((a, b) => String(a.id || '').localeCompare(String(b.id || '')));
  return out;
}

// mdRowHtml 单个模型行（纯渲染，便于独立测试）。
function mdRowHtml(m, pr) {
  const eff = (m.supported_efforts || []).slice();
  if (m.can_disable_thinking && eff.length && !eff.includes('off')) eff.push('off（可关）');
  const effs = eff.length ? eff.map(e => '<span class="tag warn">' + esc(e) + '</span>').join(' ')
    // 三值：true=固定档｜false=不支持｜缺失=未知（不宣称"不支持"）
    : '<span style="color:var(--ink-3);font-size:12.5px">' + (m.supports_reasoning
      ? '固定档 · 默认 ' + esc(m.default_effort || '?')
      : (m.supports_reasoning === false ? '不支持思考' : '档位未知')) + '</span>';
  // 能力徽标：默认模型 / 工具调用 / 视觉 / 纯推理（上游目录全字段透出，缺失不显示）
  const caps = [];
  if (m.is_default) caps.push('<span class="tag ok">默认</span>');
  if (m.supports_tool_call) caps.push('<span class="tag warn">工具</span>');
  if (m.supports_images) caps.push('<span class="tag warn">视觉</span>');
  if (m.supports_reasoning && !m.can_disable_thinking) caps.push('<span class="tag warn">思考常开</span>');
  const capHtml = caps.length ? '<div class="id" style="margin-top:2px">' + caps.join(' ') + '</div>' : '';
  const tip = m.description ? ' title="' + esc(m.description) + '"' : '';
  return '<tr><td class="mark" aria-hidden="true"><i></i></td><td class="who"' + tip + '><div class="nm">' + esc(m.id) + '</div><div class="id">' + esc(m.name || '') + '</div>' + capHtml + '</td>' +
    '<td class="num">' + rateCell(m) + '</td>' +
    '<td>' + (m.default_effort ? '<span class="tag ok">' + esc(m.default_effort) + '</span>' : '<span style="color:var(--ink-3)">—</span>') + '</td>' +
    '<td class="efs" style="white-space:normal">' + effs + '</td>' +
    '<td class="num">' + (m.context_length ? Math.round(m.context_length / 1000) + 'K' : '—') + '</td>' +
    outCell(m, pr) + '</tr>';
}

function renderModels() {
  const tb = $('mdBody');
  const list = mdSortList(mdAll.filter(m => mdMatch(m)));
  if (!list.length) {
    tb.innerHTML = '<tr><td colspan="7"><div class="empty">没有符合当前筛选条件的模型</div></td></tr>';
  } else {
    tb.innerHTML = list.map(m => mdRowHtml(m, mdProbeOf(m.id))).join('');
  }
  const filtered = list.length !== mdAll.length;
  $('mdCount').textContent = !mdAll.length ? ''
    : filtered ? '命中 ' + list.length + ' / ' + mdAll.length + ' 个模型'
    : mdAll.length + ' 个模型';
  $('mdCount').className = filtered ? 'note src-off' : 'note';
}

function resetModelFilter() {
  mdFilter = { q: '', realm: '', cap: '', effort: '', promo: '', sort: 'default' };
  $('mdQ').value = ''; $('mdRealm').value = ''; $('mdCap').value = '';
  $('mdEffort').value = ''; $('mdPromo').value = ''; $('mdSort').value = 'default';
  renderModels();
}


