/* app_views_keys.js — API Key 明文展示弹窗（从 app.js 拆出的第五个组件，2026-11-02）
   ────────────────────────────────────────────────────────────────────────────────
   内容：Key 明文/复制弹窗的渲染与交互（零顶层语句，全部是函数声明）。

   纪律：只放函数声明，本文件排在 app.js 之前加载（app.js 顶层路由会调用这些渲染函数）。

   依赖（运行时解析）：app_core.js 的 esc；app.js 的 $/api/状态变量。 */
/* ── 明文展示弹窗 ────────────────────────────────────────────────── */

// 生成可直接导入客户端的两段接入配置（OpenAI / Anthropic 协议）。
// baseURL 用面板实际访问地址（location.protocol + host）动态拼，不写死；
// 网关 /v1/models 会自动暴露模型，客户端自行发现，配置里不写死模型名。
function buildKeyImportConfigs(plaintext, baseURL) {
  const b = String(baseURL || '').replace(/\/+$/, '');
  const models = '可用模型：GET ' + b + '/v1/models 查看';
  return {
    openai: '# ' + models + '\nOPENAI_BASE_URL=' + b + '/v1\nOPENAI_API_KEY=' + plaintext,
    anthropic: '# ' + models + '\nANTHROPIC_BASE_URL=' + b + '\nANTHROPIC_AUTH_TOKEN=' + plaintext,
  };
}

function showKeyPlaintext(plaintext, kid) {
  $('keyPlaintext').textContent = plaintext;
  const cfgs = buildKeyImportConfigs(plaintext, location.protocol + '//' + location.host);
  $('cfgOpenAI').textContent = cfgs.openai;
  $('cfgAnthropic').textContent = cfgs.anthropic;
  // 缓存明文：创建/重置后本会话内，Key 卡片「接入配置」可直接回看，不依赖弹窗。
  lastKeyPlaintext = plaintext;
  lastKeyPlaintextKid = kid || null;
  showVeil('keyShowVeil');
}

// clipboard API 只在 secure context（https/localhost）可用，远程 http 面板
// 拿不到 navigator.clipboard → 降级 execCommand（textarea 选中复制）。
async function writeClipboardText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
  } else {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
  }
}

async function copyKeyPlaintext() {
  const text = $('keyPlaintext').textContent;
  try {
    await writeClipboardText(text);
    toast('已复制到剪贴板', 'ok');
  } catch (e) { toast('复制失败：' + e.message, 'err'); }
}

async function copyCfgText(elId) {
  const text = $(elId).textContent;
  try {
    await writeClipboardText(text);
    toast('已复制到剪贴板', 'ok');
  } catch (e) { toast('复制失败：' + e.message, 'err'); }
}

/* ── 接入配置弹窗（Key 卡片入口）────────────────────────────────── */

// 打开某把 Key 的「接入配置」弹窗。
// 状态 A：本会话缓存了该 Key 的明文（刚创建/刚重置）→ 直接展示完整配置；
// 状态 B：明文已不可恢复（服务端只存哈希）→ 提示 + 提供「重置密钥」找回。
function openKeyCfg(kid) {
  const k = keyData.find(x => x.id === kid);
  if (!k) return;
  keyCfgKid = kid;
  const label = (k.name || k.id);
  $('keyCfgHint').textContent = 'Key「' + label + '」# ' + kid;
  const plain = (lastKeyPlaintextKid === kid && lastKeyPlaintext) ? lastKeyPlaintext : null;
  if (plain) {
    $('keyCfgLost').hidden = true;
    $('keyCfgHas').hidden = false;
    $('keyCfgPlaintext').textContent = plain;
    const cfgs = buildKeyImportConfigs(plain, location.protocol + '//' + location.host);
    $('keyCfgOpenAI').textContent = cfgs.openai;
    $('keyCfgAnthropic').textContent = cfgs.anthropic;
  } else {
    $('keyCfgHas').hidden = true;
    $('keyCfgLost').hidden = false;
  }
  showVeil('keyCfgVeil');
}

// 重置密钥：生成新密钥并调后端专用接口（服务端只改目标条目，其他 Key 不动）。
// 成功后缓存明文，弹窗立即切到状态 A 展示新配置；旧密钥已失效。
async function resetKeySecret() {
  const kid = keyCfgKid;
  const k = keyData.find(x => x.id === kid);
  const label = (k && (k.name || k.id)) || kid;
  if (!confirm(`重置 Key「${label}」的密钥？\n\n将生成新密钥并立即生效，旧密钥作废——已配置到其他 agent / 客户端里的旧 Key 将无法再用。`)) return;
  const newKey = genApiKey();
  try {
    await api('key-reset', { method: 'POST', body: JSON.stringify({ id: kid, key: newKey }) });
    lastKeyPlaintext = newKey;
    lastKeyPlaintextKid = kid;
    $('keyCfgLost').hidden = true;
    $('keyCfgHas').hidden = false;
    $('keyCfgPlaintext').textContent = newKey;
    const cfgs = buildKeyImportConfigs(newKey, location.protocol + '//' + location.host);
    $('keyCfgOpenAI').textContent = cfgs.openai;
    $('keyCfgAnthropic').textContent = cfgs.anthropic;
    toast('密钥已重置，旧 Key 已失效', 'ok');
    loadKeys();
  } catch (e) {
    toast('重置失败：' + e.message, 'err');
  }
}

// 生成 .env 配置文件内容（OpenAI + Anthropic 两段合并），供「下载配置文件」落盘。
function buildEnvFile(plaintext, baseURL) {
  const cfgs = buildKeyImportConfigs(plaintext, baseURL);
  return '# yg-gateway 接入配置（OpenAI + Anthropic 协议）\n' + cfgs.openai + '\n\n' + cfgs.anthropic + '\n';
}

// 创建弹窗「下载 .env」：把一次性明文落成可随时导入的配置文件。
function downloadEnvFile() {
  const plain = $('keyPlaintext').textContent;
  if (!plain) return;
  const env = buildEnvFile(plain, location.protocol + '//' + location.host);
  const blob = new Blob([env], { type: 'text/plain;charset=utf-8' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'yg-gateway-import.env';
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(a.href);
}

/* ── 弹窗辅助（veil 切换）────────────────────────────────────────── */

function showVeil(id) { $(id).classList.add('on'); }
function hideVeil(id) { $(id).classList.remove('on'); }

