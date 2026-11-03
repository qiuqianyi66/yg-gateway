/* app_views_achievements.js — 成就带（P1，2026-11-03）
   ────────────────────────────────────────────────────────────────
   本机成就展示：任务中心顶部横向徽章条。
   数据源**全部是本机记录**（四态纪律：derivation，标「本机记录」）：
   - 操作次数 ops（localStorage yg_ach_v1，签到/领取/添加账号成功时由
     gamify.js 的 noteStreak() 顺带递增）
   - 连续天数 streak（localStorage yg_streak_v1，P0 已有）
   零后端接口、零跨视图 DOM 依赖（不读账号池统计卡）。

   纪律：**只许声明**——本文件排在 app.js 之前加载；初始渲染由 app.js
   start() 调用（见 TestViewsComponentsAreFunctionOnly）。 */

/* 成就定义：c 为徽章内展示字（反 AI slop：不用 emoji，用数字/汉字）。 */
const ACHIEVEMENTS = [
  { id: 'first', n: '初次上手', d: '本机完成 1 次操作', c: '1', t: function (s) { return s.ops >= 1; } },
  { id: 'warm', n: '渐入佳境', d: '本机完成 5 次操作', c: '5', t: function (s) { return s.ops >= 5; } },
  { id: 'pro', n: '老练玩家', d: '本机完成 10 次操作', c: '10', t: function (s) { return s.ops >= 10; } },
  { id: 's3', n: '连登三日', d: '本机连续操作 3 天', c: '3', t: function (s) { return s.streak >= 3; } },
  { id: 's7', n: '七日同行', d: '本机连续操作 7 天', c: '7', t: function (s) { return s.streak >= 7; } },
  ];

/* 操作次数读取（yg_ach_v1，结构 { ops: N }；异常/缺失按 0，不臆造）。 */
function achOps() {
  try {
    const o = JSON.parse(localStorage.getItem('yg_ach_v1'));
    if (o && typeof o.ops === 'number' && o.ops >= 0) return o.ops;
  } catch { /* 隐私模式等：忽略 */ }
  return 0;
}

/* 操作计数 +1：由 gamify.js 的 noteStreak() 在签到/领取/添加账号成功时顺带调用。 */
function noteOp() {
  const ops = achOps() + 1;
  try { localStorage.setItem('yg_ach_v1', JSON.stringify({ ops: ops })); } catch { /* 忽略 */ }
}

/* 成就带渲染：容器 JS 动态创建（与 streakStrip 同模式，配合深度棘轮对
   index.html 只许收紧）。插到 streakStrip 之前、#qProg 之前。 */
function renderAchievements() {
  const host = document.getElementById('view-taskscenter');
  if (!host) return;
  let el = document.getElementById('achStrip');
  if (!el) {
    el = document.createElement('div');
    el.id = 'achStrip';
    el.className = 'ach';
    el.setAttribute('aria-label', '本机成就');
    const ref = document.getElementById('streakStrip') || document.getElementById('qProg');
    if (ref) host.insertBefore(el, ref);
    else host.appendChild(el);
  }
  const s = { ops: achOps(), streak: streakDays() };
  el.innerHTML = '<span class="ach-head">成就</span>' + ACHIEVEMENTS.map(function (a) {
    const on = a.t(s);
    return '<span class="ach-item' + (on ? ' on' : '') + '" title="' + a.n + '：' + a.d +
      (on ? ' · 已达成' : ' · 未达成') + '（本机记录）"><i>' + a.c + '</i><b>' + a.n + '</b></span>';
  }).join('');
}
/* 初始渲染由 app.js start() 调用（本文件只放声明）。 */
