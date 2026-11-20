/* 顶栏加载指示（2026-11-20）
 *
 * 为什么独立成文件：app.js 已顶 1368 行棘轮，任何一行都要等量替换。这里是横切
 * 能力——包一层 fetch，任何视图的 /panel/api 请求自动点亮顶栏，不必逐个改 loader。
 *
 * 语义边界：
 *   - 只追踪 /panel/api/*（面板自身接口）。/v1/chat/completions 等代理流量不追踪：
 *     流式响应在响应头到达时就 resolve，亮一下就灭会变成噪音。
 *   - 停止后延迟 260ms 再熄灭：毫秒级请求（如 overview 命中缓存）不会闪一下。
 *   - 不改变 fetch 返回值/错误：只挂 then(off, off)，调用方的 catch 语义不变。
 */
(function () {
  var orig = window.fetch;
  if (!orig) return;
  var n = 0;
  var hideTimer = 0;

  function topbar() { return document.querySelector('.topbar'); }

  function on() {
    n++;
    if (hideTimer) { clearTimeout(hideTimer); hideTimer = 0; }
    var t = topbar();
    if (t) t.classList.add('is-loading');
  }

  function off() {
    n = Math.max(0, n - 1);
    if (n) return;
    hideTimer = setTimeout(function () {
      hideTimer = 0;
      if (n) return;
      var t = topbar();
      if (t) t.classList.remove('is-loading');
    }, 260);
  }

  function isPanelApi(input) {
    var raw = typeof input === 'string' ? input : (input && input.url) || '';
    if (!raw) return false;
    try {
      return new URL(raw, window.location.href).pathname.indexOf('/panel/api/') === 0;
    } catch (e) {
      return raw.indexOf('/panel/api/') === 0;
    }
  }

  window.fetch = function (input, init) {
    if (!isPanelApi(input)) return orig.apply(this, arguments);
    on();
    var p = orig.apply(this, arguments);
    p.then(off, off);
    return p;
  };
})();
