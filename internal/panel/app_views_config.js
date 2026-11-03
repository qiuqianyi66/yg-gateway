/* app_views_config.js — 配置页视图（从 app.js 拆出的第八个组件，2026-11-02）
   ────────────────────────────────────────────────────────────────────────────────
   内容：配置页表单渲染与回填（含配置脏检查、热改字段）。

   纪律：**只许声明**（函数/const/let/var）——本文件排在 app.js 之前加载。
   顶层事件绑定与状态留在 app.js（引用 `$`，提前执行会白屏）。

   依赖（运行时解析）：app_core.js 的 esc；app.js 的 $/api/toast/配置状态。 */
/* ── 配置 ─────────────────────────────────────────────────────────── */
/* 覆盖型字段：空串 = 回落到内置默认（issue #102-2：user_agent 清空后值没变）。
 * 不含 api_key：清空 = 关闭鉴权，误触代价太大，单独处理。 */

function dig(obj, path) { return path.reduce((o, k) => (o == null ? undefined : o[k]), obj); }
function put(obj, path, val) {
  let o = obj;
  for (let i = 0; i < path.length - 1; i++) { if (typeof o[path[i]] !== 'object' || o[path[i]] === null) o[path[i]] = {}; o = o[path[i]]; }
  o[path[path.length - 1]] = val;
}

async function loadConfig() {
  try {
    const d = await api('config');
    cfgLoaded = d.config;
    $('cfgPath').textContent = d.path || '';
    const f = $('cfgForm');
    for (const [name, path] of Object.entries(CFG_MAP)) {
      const el = f.elements[name];
      if (!el) continue;
      const v = dig(cfgLoaded, path);
      if (el.type === 'checkbox') el.checked = el.name === 'session_sticky_derive' ? v !== false : !!v;
      else if (Array.isArray(v)) el.value = v.join(', ');
      else el.value = v == null ? '' : v;
    }
    // model_aliases 是 map[string]string：序列化成 "from=to" 每行一条填到 textarea。
    const aliases = dig(cfgLoaded, ['model_aliases']);
    const aliasesEl = f.elements['model_aliases'];
    if (aliasesEl) {
      if (aliases && typeof aliases === 'object') {
        aliasesEl.value = Object.entries(aliases).map(([k, v]) => `${k}=${v}`).join('\n');
      } else {
        aliasesEl.value = '';
      }
    }
    markDurationFields(); // 回填后重置校验态（清掉残留红框；现值来自后端必然合法）
    // 回填即"与后端一致"→ 清掉脏标记（避免刚加载就被判为有未保存改动）。
    clearCfgDirty();
  } catch (e) { toast('读取配置失败：' + e.message, 'err'); }
}
function collectConfig() {
  const f = $('cfgForm'), out = {};
  for (const [name, path] of Object.entries(CFG_MAP)) {
    const el = f.elements[name];
    if (!el) continue;
    let v;
    if (el.type === 'checkbox') v = el.checked;
    else if (el.type === 'number') { v = el.value.trim() === '' ? undefined : Number(el.value); }
    else {
      const raw = el.value.trim();
      // 覆盖型字段空串照发（见 CLEARABLE_CFG）；其余空 = 不下发。
      if (raw === '') v = CLEARABLE_CFG.has(name) ? '' : undefined;
      else if (name.endsWith('_hours')) v = raw.split(/[,，\s]+/).filter(Boolean).map(Number);
      else v = raw;
    }
    if (v !== undefined) put(out, path, v);
  }
  // model_aliases：解析 "from=to" 每行一条。空 = 显式清空 map（照发空 map）。
  const aliasesEl = f.elements['model_aliases'];
  if (aliasesEl) {
    const map = {};
    aliasesEl.value.split('\n').forEach(line => {
      const t = line.trim();
      if (!t) return;
      const eq = t.indexOf('=');
      if (eq <= 0) return;
      const from = t.slice(0, eq).trim();
      const to = t.slice(eq + 1).trim();
      if (from && to) map[from] = to;
    });
    out.model_aliases = map;
  }
  return out;
}
/* Go 时长字段即时校验：空 = 沿用现值（collectConfig 跳过发送）；非空必须是
   ParseDuration 语法（30m / 2h / 600s / 1h30m，可组合可带小数）。与后端
   config.go normalize() 的 time.ParseDuration 同口径，脏值在前端就地标红，
   不再等到保存被拒。 */
function durationBad(name) {
  const el = $('cfgForm').elements[name];
  if (!el) return false;
  const v = el.value.trim();
  return v !== '' && !DURATION_RE.test(v);
}
function markDurationFields() {
  for (const name of DURATION_FIELDS) {
    const el = $('cfgForm').elements[name];
    if (!el) continue;
    const bad = durationBad(name);
    el.classList.toggle('invalid', bad);
    el.title = bad ? DURATION_TIP : '';
  }
}
// checkbox / select 改动同样算脏（input 事件在部分浏览器对 checkbox 不触发）

