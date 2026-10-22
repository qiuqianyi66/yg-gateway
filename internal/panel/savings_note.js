/* savings_note.js — R-106 OpenRouter 费率估算面板展示。
 *
 * 后端已实现折算（internal/usage Agg.USDValue：按 OpenRouter 公开参考价折算的
 * 美元值，纯展示层 inference）。wire 口径：`json:"usd_value,omitempty"`——
 * 有定价且窗口内有 token 才出现该键；无定价部署响应体不含此键。
 *
 * 四态纪律（缺失 = unknown，禁止按 0 计）：
 *   - totals 里存在 usd_value 键 → 渲染折算金额 + inference 标注；
 *   - 键缺失 → 显示「未估算」，不渲染 0。
 *
 * app.js 已在用量明细表与「扣除积分」KPI 副标题展示折算值；本文件补一块带
 * 明确「估算（inference）」标注的独立说明行（#savingsLine），不改动后端 wire、
 * 不改动 app.js（其行数已顶 check-depth 基线 maxFrontendLines）。
 */
(function () {
  'use strict';

  const line = document.getElementById('savingsLine');
  if (!line) return;

  // 与 app.js 同一 localStorage 键（只读复用，避免再存一份密钥）。
  const LS_KEY = 'yg-gateway.key';

  // fmtUSD 紧凑格式化：≥1000 一位小数 + k；≥1 两位小数；否则三位。
  function fmtUSD(v) {
    if (v >= 1000) return '$' + (v / 1000).toFixed(1) + 'k';
    if (v >= 1) return '$' + v.toFixed(2);
    return '$' + v.toFixed(3);
  }

  function render(totals) {
    if (totals && Object.prototype.hasOwnProperty.call(totals, 'usd_value') &&
        Number(totals.usd_value) > 0) {
      line.textContent =
        '本窗口折算 ≈ ' + fmtUSD(Number(totals.usd_value)) +
        '（按 OpenRouter 公开参考价估算 · inference，非上游账单实测）';
    } else {
      line.textContent =
        '本窗口未估算：无模型定价或窗口内无计费用量' +
        '（usd_value 字段缺失 = unknown，不按 0 计）';
    }
  }

  async function refresh() {
    if ((location.hash || '#accounts').slice(1) !== 'usage') return;
    try {
      const h = {};
      const k = localStorage.getItem(LS_KEY);
      if (k) h['Authorization'] = 'Bearer ' + k;
      // hours=72 与后端默认窗口一致（app.js 同口径）。
      const r = await fetch('/panel/api/usage?hours=72', { headers: h });
      if (!r.ok) return; // 401/5xx：保留静态说明文案，不弹窗不报错
      const d = await r.json();
      render(d.totals || null);
    } catch (e) { /* 网络抖动时保留静态文案 */ }
  }

  window.addEventListener('hashchange', refresh);
  refresh();
})();
