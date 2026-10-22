/* acl_notice.js — F-101 面板显式提示（R-101）。
 *
 * 背景：后端写回（auth.SaveAtomic → LockdownFile）与启动兜底（auth.LoadDirReport）
 * 已落地凭证 ACL 自动收紧（internal/auth/lockdown.go：icacls /inheritance:r 断开继承，
 * 只 grant 服务账户 + SYSTEM）。失败仅 log.WARN 不阻断写回（auth.go:375-377）。
 * 此前面板零提示：用户既不知道凭证已被自动收紧，也不知道收紧失败时的人工兜底
 * 路径（lock-auths.ps1）。本文件把这两件事显式说出来。
 *
 * 行为边界：本文件不做任何写/收紧操作，只在 app.js 渲染的「添加账号成功」
 * （#addDone，OAuth 登录）与「导入 JSON 成功」（#importDone）提示出现后，向其
 * 追加一行说明文案。文案纪律：收紧结果不落盘、面板不可观测，因此只描述机制与
 * 失败兜底路径，不承诺「保证已收紧」。
 *
 * 与 app.js 的配合：app.js 在重新打开弹窗时会重置 #addDone 的 hidden 并以
 * textContent 重写内容（textContent 会抹掉本文件追加的子节点）。故 observer 在
 * hidden=true 时清除已挂载标记，下次成功再补挂。
 */
(function () {
  'use strict';

  const NOTICE =
    '凭证已落盘：写回后服务会自动收紧 ACL（仅当前用户 + SYSTEM）。' +
    '收紧失败仅记日志（WARN），可手动运行 lock-auths.ps1 复核。';

  // attach 向成功提示节点追加说明行；幂等（已挂过则跳过）。
  function attach(anchor) {
    if (!anchor || anchor.dataset.aclNotice === '1') return;
    const line = document.createElement('div');
    line.className = 'hint';
    line.style.marginTop = '6px';
    line.textContent = NOTICE;
    anchor.appendChild(line);
    anchor.dataset.aclNotice = '1';
  }

  // watch 监听成功提示节点的 hidden 属性：
  // 变为可见时补挂说明行；变回隐藏（重开弹窗被 app.js 重置）时清除标记，
  // 否则 textContent 重写抹掉子节点后，标记还在会漏挂下一次。
  function watch(id) {
    const el = document.getElementById(id);
    if (!el) return;
    const mo = new MutationObserver(function () {
      if (el.hidden) {
        el.dataset.aclNotice = '0';
        return;
      }
      attach(el);
    });
    mo.observe(el, { attributes: true, attributeFilter: ['hidden'] });
    if (!el.hidden) attach(el);
  }

  watch('addDone');
  watch('importDone');
})();
