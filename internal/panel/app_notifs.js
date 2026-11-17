/* app_notifs.js — 通知中心（localStorage 持久化，跨刷新保留）
   从 app.js 拆出的第十二个组件（2026-11-17）。

   加载顺序：panelScripts 中排在 app.js 之后。所有函数只在事件处理器、
   toast() 内部与本文件末尾的自初始化里调用，app.js 顶层不读取本文件符号。

   初始化由本文件末尾自己完成（initNotifCenter()），不依赖 app.js 的延迟调用：
   setTimeout(0) 会被 <script src> 的网络往返抢先、DOMContentLoaded 在动态插入时
   不触发——两种"外部等时机"的办法都不成立（CI e2e 抓到过 setTimeout 版本抛
   `initNotifCenter is not defined`）。自己初始化自己，时序上不可能失配。

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

// 自初始化：本文件所有依赖（$、DOM 节点）此时都已就绪——脚本在 </body> 前加载。
initNotifCenter();
