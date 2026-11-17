/* app_playground.js — API 调试台（Playground）
   从 app.js 拆出的第十一个组件（2026-11-17）。

   加载顺序：panelScripts 中排在 app.js **之后**（与 app_qr.js 同模式）——
   pg* 函数只在事件处理器与异步回调里被调用（btnPlayground / btnPgSend 的 onclick），
   首次点击时 app.js 早已求值完毕，不存在先定义后使用的问题。let pgModels/pgAbort/pgBusy
   同理：app.js 的顶层代码不读它们，只有 pg* 函数读。

   留在 app.js：五处顶层 onclick 绑定与首个 start() 调用（引用 $，提前执行会白屏）。

   **这五处绑定必须写成箭头函数延迟求值**（`= () => openPlayground()`，不能写
   `= openPlayground`）。2026-11-17 拆分时实测踩到：直接引用会在 app.js 求值时立即
   读取本文件尚未定义的符号 → `Uncaught ReferenceError: openPlayground is not defined`
   → app.js 后续语句（含末尾 start()）全部不执行 → 面板动态数据全空白，而静态骨架
   仍渲染，用户只看到"数据没加载"。既有三道牙齿测试只校验文件名顺序与组件纯度，
   抓不到这类"跨文件顶层立即求值"的错。

   依赖（运行时解析）：app.js 的 $ / api / toast / LS_KEY / isAbort，app_core.js 的 esc。 */

/* ── API 调试台（Playground） ────────────────────────────────────────
   对标 new-api / ARP：不离开面板即可验证模型可用 / 鉴权 / 流式（同源 fetch）。 */

let pgModels = null;   // 模型缓存（首次打开拉一次 /panel/api/models）
let pgAbort = null;    // 当前请求的 AbortController（停止按钮用）
let pgBusy = false;    // 发送中标记（防连点）

function pgModelOptions() {
  if (!Array.isArray(pgModels) || !pgModels.length) return '<option value="">模型列表为空</option>';
  // 默认推荐低成本主力（积分优化，对应 .harness/rules/COST-CONTROL.md）：
  // deepseek-v4.1-flash 实测 ~1 积分/M，而目录首个模型未必便宜（hy4 系实测
  // 110~133 积分/M）。找不到时保持第一个（不破坏上游目录顺序语义）。
  const pref = pgModels.find(m => /deepseek-v4\.1-flash$/i.test(m.id)) || pgModels[0];
  const prefId = pref && pref.id;
  // 同名区分：跨域/同域变体共用展示名时追加完整 id。
  return pgModels.map(m => {
    const tip = m.promo_note || m.description || '';
    const dup = pgModels.some(o => o !== m && String(o.name || o.id || '') === String(m.name || m.id || '')) ? m.id : '';
    return '<option value="' + esc(m.id) + '"' + (m.id === prefId ? ' selected' : '') +
      (tip ? ' title="' + esc(tip) + '"' : '') + '>' +
      esc(pgModelLabel(m) + (dup ? ' (' + dup + ')' : '')) + '</option>';
  }).join('');
}

/* pgModelLabel：模型名 · 倍率 · 标签（倍率缺失标「未知」）。 */
function pgModelLabel(m) {
  const parts = [String(m.name || m.id || '')];
  const eff = (m.promo_credits != null && m.promo_credits !== '') ? m.promo_credits : m.credits;
  const rate = String(eff || '').trim();
  parts.push(rate ? rate : '倍率未知');
  const factor = m.promo_factor == null ? null : Number(m.promo_factor);
  const creditsNum = parseFloat(String(m.credits || '').replace(/[^\d.]/g, ''));
  const isFree = factor === 0 || (Number.isFinite(creditsNum) && creditsNum === 0);
  if (isFree) parts.push(m.promo_label || '免费');
  else if (m.promo_label) parts.push(m.promo_label);
  else if (m.supports_images) parts.push('视觉');
  return parts.join(' · ');
}
async function pgLoadModels() {
  const sel = $('pgModel');
  if (!sel) return;
  if (pgModels) { sel.innerHTML = pgModelOptions(); return; }
  try {
    const d = await api('models');
    // /panel/api/models 返回 { models: [...] }；个别版本直接返回数组，兼容两种。
    pgModels = Array.isArray(d.models) ? d.models : (Array.isArray(d) ? d : []);
    sel.innerHTML = pgModelOptions();
  } catch (e) {
    sel.innerHTML = '<option value="">模型加载失败，可重开重试</option>';
  }
}
function openPlayground() {
  $('pgVeil').classList.add('on');
  pgLoadModels();
  setTimeout(() => { const p = $('pgPrompt'); if (p) p.focus(); }, 60);
}
function pgStop() {
  if (pgAbort) { pgAbort.abort(); pgAbort = null; }
}
function pgSetBusy(busy) {
  pgBusy = busy;
  $('btnPgSend').disabled = busy;
  $('btnPgStop').hidden = !busy;
  $('btnPgSend').textContent = busy ? '生成中…' : '发送';
}
function pgAppend(text) {
  const out = $('pgOut');
  out.textContent += text;
  out.scrollTop = out.scrollHeight;
}
function pgSetOut(text, cls) {
  const out = $('pgOut');
  out.textContent = text;
  out.className = 'pg-out' + (cls ? ' ' + cls : '');
}
// SSE 流式解析：fetch ReadableStream → 按行读 "data: {...}" → 逐 delta 追加。
// data 行可能被 chunk 拆成两半，用行缓冲 buf 拼接后再逐行消费。
async function pgStreamRead(resp, startedAt) {
  const reader = resp.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  let usage = null;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line.startsWith('data:')) continue;
      const payload = line.slice(5).trim();
      if (payload === '[DONE]') continue;
      try {
        const obj = JSON.parse(payload);
        const delta = obj.choices && obj.choices[0] && obj.choices[0].delta;
        if (delta && typeof delta.content === 'string') pgAppend(delta.content);
        if (obj.usage) usage = obj.usage;
      } catch { /* 忽略半行 / 非 JSON 的控制行 */ }
    }
  }
  const secs = ((Date.now() - startedAt) / 1000).toFixed(1);
  $('pgMeta').textContent = '流式完成 · ' + secs + 's' +
    (usage ? ' · prompt ' + (usage.prompt_tokens ?? '—') + ' / completion ' + (usage.completion_tokens ?? '—') : '');
}
async function sendPlayground() {
  if (pgBusy) return;
  const model = $('pgModel').value;
  if (!model) { toast('请先选择模型', 'err'); $('pgModel').focus(); return; }
  const prompt = $('pgPrompt').value.trim();
  if (!prompt) { toast('用户消息不能为空', 'err'); $('pgPrompt').focus(); return; }
  const messages = [];
  const sys = $('pgSystem').value.trim();
  if (sys) messages.push({ role: 'system', content: sys });
  messages.push({ role: 'user', content: prompt });
  const body = { model, messages, stream: $('pgStream').value === '1' };
  const temp = Number($('pgTemp').value);
  if (Number.isFinite(temp)) body.temperature = temp;
  const mt = Number($('pgMaxTok').value);
  if (Number.isFinite(mt) && mt > 0) body.max_tokens = mt;

  pgStop();
  pgAbort = new AbortController();
  pgSetBusy(true);
  pgSetOut('', '');
  $('pgMeta').textContent = '等待响应…';
  const startedAt = Date.now();
  const headers = { 'Content-Type': 'application/json' };
  const key = localStorage.getItem(LS_KEY);
  if (key) headers['Authorization'] = 'Bearer ' + key;
  try {
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST', headers, body: JSON.stringify(body), signal: pgAbort.signal,
    });
    if (!resp.ok) {
      const d = await resp.json().catch(() => ({}));
      const msg = d.error && d.error.message ? ' · ' + d.error.message : '';
      pgSetOut('HTTP ' + resp.status + msg, 'err');
      $('pgMeta').textContent = '失败 · ' + ((Date.now() - startedAt) / 1000).toFixed(1) + 's';
      toast('网关返回 ' + resp.status, 'err');
      return;
    }
    if (body.stream) {
      $('pgMeta').textContent = '流式生成中…';
      pgSetOut('', 'streaming');
      await pgStreamRead(resp, startedAt);
    } else {
      const d = await resp.json();
      const content = d.choices && d.choices[0] && d.choices[0].message && d.choices[0].message.content;
      pgSetOut(content || '(无内容返回)', '');
      const secs = ((Date.now() - startedAt) / 1000).toFixed(1);
      const u = d.usage || {};
      $('pgMeta').textContent = '完成 · ' + secs + 's' +
        (u.total_tokens ? ' · prompt ' + (u.prompt_tokens ?? '—') + ' → completion ' + (u.completion_tokens ?? '—') + '（共 ' + u.total_tokens + '）' : '');
    }
  } catch (e) {
    if (isAbort(e)) { pgSetOut('（已停止）', 'err'); $('pgMeta').textContent = '已停止'; }
    else { pgSetOut(e.message, 'err'); $('pgMeta').textContent = '请求失败'; }
  } finally {
    pgAbort = null;
    pgSetBusy(false);
  }
}
