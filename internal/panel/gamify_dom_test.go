package panel

// gamify_dom_test.go 游戏化层（连续天数条 / 成就带）的**真实 DOM 语义**测试。
//
// 为什么单列（2026-11-04 生产事故）：这两个条由 JS 动态插入 #view-taskscenter，
// 但 #qProg 是 .box 的子节点（孙层），而插入用的是 host.insertBefore(el, qProg)。
// 浏览器里这抛 NotFoundError，整条 start() 被中断（成就带不渲染、用量页 TDZ）。
// 已有的 TestAppJSTopLevelSmoke 用 inert Proxy 桩 DOM——所有调用都被吞掉、永不抛错，
// 所以它对这类"参数语义错"完全盲目。这里给一个**有真实父子约束**的桩：
// insertBefore 的参照节点不是直接子节点就抛错，与浏览器同判。

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const gamifyDOMHarness = `const fs = require('fs');
const vm = require('vm');

class FakeNode {
  constructor(id) {
    this.id = id || '';
    this.children = [];
    this.parentNode = null;
    this.className = '';
    this.attrs = {};
    this._html = '';
  }
  set innerHTML(v) { this._html = String(v); }
  get innerHTML() { return this._html; }
  get textContent() { return this._html; }
  appendChild(c) { c.parentNode = this; this.children.push(c); return c; }
  insertBefore(node, ref) {
    if (ref && ref.parentNode !== this) {
      // 浏览器原文：Failed to execute 'insertBefore' on 'Node'
      throw new Error('NotFoundError: ref is not a child of this node');
    }
    node.parentNode = this;
    const i = this.children.indexOf(ref);
    if (i < 0) this.children.push(node); else this.children.splice(i, 0, node);
    return node;
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  querySelector() { return null; }
}

const host = new FakeNode('view-taskscenter');
const box = new FakeNode('');
const qProg = new FakeNode('qProg');
host.appendChild(box);
box.appendChild(qProg);

const byId = { 'view-taskscenter': host, qProg: qProg };
const document = {
  getElementById: (id) => byId[id] || null,
  createElement: () => new FakeNode(''),
  querySelectorAll: () => [],
  addEventListener() {},
};
const ctx = {
  document: document,
  window: {},
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  console, JSON, Math, Date, Number, String, Object, Array, Set, Map, Promise, Error,
};
ctx.window = ctx;
ctx.globalThis = ctx;
vm.createContext(ctx);

const files = ['app_views_gamify.js', 'app_views_achievements.js'];
try {
  for (const f of files) vm.runInContext(fs.readFileSync(f, 'utf8'), ctx, { filename: f });
  vm.runInContext('renderStreak(); renderAchievements();', ctx);
  const ids = box.children.map((c) => c.id || '(anon)');
  const topIds = host.children.map((c) => c.id || '(anon)');
  console.log(JSON.stringify({ ok: true, box: ids, host: topIds }));
} catch (e) {
  console.log(JSON.stringify({ ok: false, err: (e && e.stack ? e.stack : String(e)).split('\n').slice(0, 3).join(' | ') }));
  process.exit(1);
}
`

// TestGamifyStripsInsertIntoNestedContainer 用真实父子约束验证两条带插得进去。
//
// 牙齿：把 app_views_gamify.js 的插入改回 host.insertBefore(el, qprog)，
// 或把 app_views_achievements.js 改回 host.insertBefore(el, ref) → 本用例立刻红
// （harness 抛 NotFoundError，进程 exit 1）。
func TestGamifyStripsInsertIntoNestedContainer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; gamify DOM test skipped")
	}
	hf, err := os.CreateTemp(t.TempDir(), "gamify-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.WriteString(gamifyDOMHarness); err != nil {
		t.Fatal(err)
	}
	hf.Close()

	out, err := exec.Command(node, hf.Name()).Output()
	if err != nil {
		t.Fatalf("游戏化层 DOM 渲染失败（insertBefore 参照节点不是直接子节点？）: %v\n%s", err, nodeDiag(err, out))
	}
	var got struct {
		OK   bool     `json:"ok"`
		Box  []string `json:"box"`
		Host []string `json:"host"`
		Err  string   `json:"err"`
	}
	if e := json.Unmarshal(bytes.TrimSpace(out), &got); e != nil {
		t.Fatalf("harness 输出不是 JSON: %v\n%s", e, out)
	}
	if !got.OK {
		t.Fatalf("渲染抛错: %s", got.Err)
	}
	joined := strings.Join(got.Box, ",")
	for _, want := range []string{"streakStrip", "achStrip"} {
		if !strings.Contains(joined, want) {
			t.Errorf("容器内缺 %s（实际子节点：%v）", want, got.Box)
		}
	}
	// 两条带必须排在进度条之前：用户扫视时先看到连续天数/成就，再看到队列进度。
	if i, j := strings.Index(joined, "qProg"), strings.Index(joined, "streakStrip"); i >= 0 && j > i {
		t.Errorf("streakStrip 排到了 qProg 之后：%v", got.Box)
	}
}

// TestAppJSBootOrderGuards 顶层启动顺序的两条静态闸门（2026-11-04 两个 TDZ 事故）。
//
// 为什么会静默失效：经典脚本里顶层 let/const 在求值到那一行之前处于 TDZ，
// 而 `typeof x` 对 TDZ 变量**会抛 ReferenceError**（只对未声明变量安全）。
// 于是"用 typeof 守卫"的写法看着安全、实际一触发就崩；同理 start() 若早于
// 后半段的 const 求值，loadOverview → loadUsage 会踩 TDZ。
func TestAppJSBootOrderGuards(t *testing.T) {
	raw, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	if strings.Contains(body, "typeof queueTimer") {
		t.Error("app.js 里出现 typeof queueTimer：对 TDZ 中的 let 无效（会抛 ReferenceError），" +
			"应把 queueTimer 声明上提到可见性监听之前")
	}
	decl := strings.Index(body, "let queueTimer")
	vis := strings.Index(body, "addEventListener('visibilitychange'")
	if decl < 0 {
		t.Fatal("app.js 找不到 queueTimer 声明")
	}
	if vis >= 0 && decl > vis {
		t.Error("queueTimer 声明在 visibilitychange 监听之后：首次触发即 TDZ，声明必须更早")
	}
	// start() 必须是整份脚本的最后一条顶层语句（后面只允许注释/空行）。
	tail := strings.TrimSpace(body)
	if !strings.HasSuffix(tail, "start();") {
		t.Errorf("app.js 末尾不是 start();（实际结尾：%q）——启动点前移会让 "+
			"start() 读到尚未求值的模块级 const（TDZ）", lastLine(tail))
	}
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
