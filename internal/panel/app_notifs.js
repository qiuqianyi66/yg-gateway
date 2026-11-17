/* app_notifs.js — 通知中心（localStorage 持久化，跨刷新保留）
   从 app.js 拆出的第十二个组件（2026-11-17）。

   加载顺序：panelScripts 中排在 app.js 之后。所有函数只在事件处理器、
   toast() 内部与延迟初始化里调用，app.js 顶层不读取本文件符号。

   注意：app.js 里用 setTimeout(..., 0) 延迟调用 initNotifCenter()。
   顶层同步直接调用会 ReferenceError——本文件尚未加载（同 app_playground.js 的坑）。

   依赖（运行时解析）：app.js 的 $，app_core.js 的 esc（本文件未用）。 */

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
