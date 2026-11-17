/* app_cmdp.js — 命令面板（Ctrl+K）
   从 app.js 拆出的第十三个组件（2026-11-17）。

   加载顺序：panelScripts 中排在 app.js 之后。CMD_ITEMS 的动作全是箭头函数，
   运行时才求值 go/openAdd/openPlayground/$ 等符号，顶层不立即读取。

   注意：app.js 里用 setTimeout(..., 0) 延迟调用 initCmdPalette()。

   依赖（运行时解析）：app.js 的 $ / go，其余动作函数由各自组件提供。 */

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
