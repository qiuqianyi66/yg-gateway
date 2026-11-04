/* ── 游戏化微动效层（P0，2026-11-03）──────────────────────────────
   纯 DOM + CSS 动画，零依赖（不引入外部库，符合单二进制 go:embed 红线）。
   三块能力：
   ① setCount —— KPI 数字滚动（只滚整数前缀，其余文本原样保留）
   ② celebrate —— 成功动作的轻量彩屑（签到 / 领取 / 添加账号）
   ③ noteStreak / renderStreak —— 本机连续操作天数（localStorage 派生，
      明确标注「本机记录」，不冒充上游数据；四态纪律：derivation）
   全部尊重 prefers-reduced-motion；在 node 测试 harness 里缺 matchMedia /
   requestAnimationFrame 时静默降级为直接落值。 */

/* ── ① 数字滚动 ─────────────────────────────────────────────────── */
/* 用法：setCount('sHealthy', 12)。文本形如「123 / 456」「12」时只滚动开头的
   整数，后缀（" / 456"）不动。负数、非数字（如「—」）直接落值不滚。
   动画时长 600ms、easeOutCubic；reduced-motion 或环境无 rAF 时直接落值。 */
function setCount(id, text) {
  const el = document.getElementById(id);
  if (!el) return;
  const value = String(text);
  const reduce = typeof matchMedia === 'function' &&
    matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (reduce || typeof requestAnimationFrame !== 'function') {
    el.textContent = value;
    return;
  }
  const m = value.match(/^(-?\d[\d,]*)/);
  if (!m) { el.textContent = value; return; }   // 非数字开头（— / 未知）不滚
  const target = parseInt(m[1].replace(/[^\d-]/g, ''), 10);
  if (!isFinite(target)) { el.textContent = value; return; }
  const cur = String(el.textContent).match(/^(-?\d[\d,]*)/);
  const from = cur ? parseInt(cur[1].replace(/[^\d-]/g, ''), 10) : NaN;
  if (!isFinite(from) || from === target) { el.textContent = value; return; }
  const suffix = value.slice(m[0].length);
  const dur = 600, t0 = Date.now();
  function step() {
    const p = Math.min(1, (Date.now() - t0) / dur);
    const e = 1 - Math.pow(1 - p, 3);            // easeOutCubic
    el.textContent = Math.round(from + (target - from) * e) + suffix;
    if (p < 1) requestAnimationFrame(step);
    else el.textContent = value;                 // 末帧用原文，避免取整残留
  }
  requestAnimationFrame(step);
}

/* ── ② 庆祝彩屑 ─────────────────────────────────────────────────── */
/* 轻量版：26 片绝对定位的色块，CSS 关键帧飞散后自动清理。
   reduced-motion 下不产生任何 DOM。调用点：签到完成 / 领取奖励 / 添加账号。 */
function celebrate() {
  if (typeof matchMedia === 'function' &&
      matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  let host = document.getElementById('confetti');
  if (!host) {
    host = document.createElement('div');
    host.id = 'confetti';
    host.setAttribute('aria-hidden', 'true');
    document.body.appendChild(host);
  }
  const COLORS = ['var(--accent)', 'var(--warn)', 'var(--bad)', 'var(--ink-3)'];
  for (let i = 0; i < 26; i++) {
    const p = document.createElement('i');
    p.style.left = (28 + Math.random() * 44) + '%';
    p.style.setProperty('--dx', (Math.random() * 180 - 90).toFixed(0) + 'px');
    p.style.setProperty('--dy', (-(70 + Math.random() * 150)).toFixed(0) + 'px');
    p.style.background = COLORS[i % COLORS.length];
    p.style.animationDelay = (Math.random() * 0.14).toFixed(2) + 's';
    host.appendChild(p);
    p.addEventListener('animationend', () => p.remove(), { once: true });
  }
}

/* ── ③ 本机连续操作天数（派生展示）──────────────────────────────── */
/* 只记「当天是否操作过」：每次签到 / 领取 / 添加账号成功时记一笔本地日期。
   跨零点自然延续；连续天数按「今天或昨天起往前数」标准口径计算。
   数据仅存本机 localStorage，**不来自上游**——UI 文案必须带「本机记录」标注。 */
const STREAK_KEY = 'yg_streak_v1';
function streakDayKey(d) {
  return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') +
    '-' + String(d.getDate()).padStart(2, '0');
}
function streakLoad() {
  try {
    const a = JSON.parse(localStorage.getItem(STREAK_KEY));
    return Array.isArray(a) ? a : [];
  } catch { return []; }
}
function streakSave(a) {
  try { localStorage.setItem(STREAK_KEY, JSON.stringify(a.slice(-90))); } catch { /* 隐私模式等：忽略 */ }
}
function noteStreak() {
  const today = streakDayKey(new Date());
  const a = streakLoad();
  if (a[a.length - 1] !== today) a.push(today);
  streakSave(a);
  noteOp();          // 成就带操作计数（app_views_achievements.js）
  renderStreak();
  renderAchievements();
}
function streakDays() {
  const set = new Set(streakLoad());
  let n = 0;
  const d = new Date();
  if (!set.has(streakDayKey(d))) d.setDate(d.getDate() - 1);   // 今天未记则从昨天起算
  while (set.has(streakDayKey(d))) { n++; d.setDate(d.getDate() - 1); }
  return n;
}
function renderStreak() {
  let el = document.getElementById('streakStrip');
  if (!el) {
    // 容器由 JS 创建（HTML 骨架保持最小，配合深度棘轮对 index.html 只许收紧）
    const host = document.getElementById('view-taskscenter');
    const qprog = host && document.getElementById('qProg');
    if (!host) return;
    el = document.createElement('div');
    el.id = 'streakStrip';
    el.className = 'streak';
    el.setAttribute('aria-label', '本机连续操作天数');
    // #qProg 的父节点是 .box（不是 host 的直接子节点）：insertBefore 的参照节点
    // 必须是目标容器的直接子节点，否则抛 NotFoundError（此前整条 start() 被中断）。
    if (qprog && qprog.parentNode) qprog.parentNode.insertBefore(el, qprog);
    else host.appendChild(el);
  }
  const set = new Set(streakLoad());
  const days = streakDays();
  const today = new Date();
  let dots = '';
  for (let i = 6; i >= 0; i--) {
    const d = new Date(today.getTime());
    d.setDate(d.getDate() - i);
    const key = streakDayKey(d);
    dots += '<i class="sd' + (set.has(key) ? ' on' : '') + '" title="' + key +
      (set.has(key) ? ' · 有操作' : ' · 无操作') + '"></i>';
  }
  el.innerHTML = '<span class="sl-n">连续 ' + days + ' 天</span>' +
    '<span class="sl-dots">' + dots + '</span>' +
    '<span class="sl-note">本机记录 · 每次签到 / 领取 / 添加成功 +1 天</span>';
}
/* 初始渲染由 app.js start() 调用（视图组件只许放声明，见 TestViewsComponentsAreFunctionOnly）。 */
