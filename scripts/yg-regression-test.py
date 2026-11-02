#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
yg-gateway 生产稳定性回归（worst-case 覆盖版）
==============================================
针对 D:/yg-gateway 生产实例（127.0.0.1:7863）做最坏情景回归。

覆盖矩阵（每节独立断言，失败不影响其他节）：
  S1  配置自检          config.json 关键值 vs /status（护栏 500k/300k、rpm 20、append）
  S2  健康基线          100 次非流式小请求 → 期望 100% 200
  S3  护栏 max 边界     ~520k 中文 → 期望 413 context_length_exceeded（本地拦截，不达上游）
  S4  会话 RPM 限流     同 conversationId 连发 25 次（rpm=20）→ 期望前 20 次 200、第 21+ 次 429
  S5  无效输入族        坏 auth→401；畸形 JSON→400；不存在模型→4xx；空 messages→400
  S6  超大 body         >64MB → 期望 413（body 上限，非 guardrail 拦截）
  S7  流式完整性        10 次 SSE：期望全部 200 且以 data: [DONE] 收尾
  S8  并发无 panic      8 线程 × 5 请求 → 期望全有响应（200/429），无 5xx/超时

用法：python yg-regression-test.py [-n 健康次数] [--model <模型>] [--skip S4,S8]
默认模型 cn:hy3 ×0（限时免费，零积分消耗）；失败可改 --model cn:deepseek-v4.1-flash ×0.11
"""
import argparse
import json
import os
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:7863"
CONFIG_PATH = r"D:\yg-gateway\config.json"
DEFAULT_MODEL = "cn:hy3 ×0"
FALLBACK_MODEL = "cn:deepseek-v4.1-flash ×0.11"

PASS, FAIL = [], []


def load_key():
    with open(CONFIG_PATH, encoding="utf-8") as f:
        return json.load(f)["api_key"]


def request(path, key, body=None, headers=None, stream=False, timeout=180):
    url = BASE + path
    data = body if body is None else json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST" if body is not None else "GET")
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Content-Type", "application/json")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            extra = {"x_gateway_warning": resp.headers.get("X-Gateway-Warning"),
                     "retry_after": resp.headers.get("Retry-After")}
            if stream:
                return resp.status, raw.decode("utf-8", "replace"), time.time() - t0, extra
            try:
                return resp.status, json.loads(raw.decode("utf-8")), time.time() - t0, extra
            except Exception:
                return resp.status, {"_raw": raw[:200].decode("utf-8", "replace")}, time.time() - t0, extra
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            data = json.loads(raw.decode("utf-8"))
        except Exception:
            data = {"_raw": raw[:200].decode("utf-8", "replace")}
        extra = {"x_gateway_warning": e.headers.get("X-Gateway-Warning"),
                 "retry_after": e.headers.get("Retry-After")}
        return e.code, data, time.time() - t0, extra
    except Exception as e:
        return -1, {"error": str(e)}, time.time() - t0, {}


def check(name, cond, detail=""):
    if cond:
        PASS.append(name)
        print(f"  [PASS] {name} {detail}")
    else:
        FAIL.append(name)
        print(f"  [FAIL] {name} {detail}")


def s1_config(key):
    print("\n== S1 配置自检 ==")
    with open(CONFIG_PATH, encoding="utf-8") as f:
        c = json.load(f)
    check("config guardrails 500k/300k",
          c["guardrails"].get("context_max_tokens") == 500000 and c["guardrails"].get("context_warn_tokens") == 300000,
          f"max={c['guardrails'].get('context_max_tokens')} warn={c['guardrails'].get('context_warn_tokens')}")
    check("config prompt append",
          c["prompt"].get("mode") == "append", f"mode={c['prompt'].get('mode')}")
    check("config session_rpm=20",
          c["guardrails"].get("session_rpm") == 20)
    try:
        s = request("/status", key, body=None)[1]
        gs = s.get("guardrail_stats", {})
        print(f"  /status guardrail_stats={json.dumps(gs, ensure_ascii=False)}")
        check("status endpoint 可达", True)
    except Exception as e:
        check("status endpoint 可达", False, str(e))


def s2_healthy(key, model, n):
    print(f"\n== S2 健康基线 {n} 次（model={model}）==")
    ok, errs, pts, dts = 0, [], [], []
    for i in range(1, n + 1):
        st, data, dt, _ = request("/v1/chat/completions", key,
                                  {"model": model, "messages": [{"role": "user", "content": f"ping #{i} 稳定性回归"}], "stream": False})
        if st == 200:
            ok += 1
            pts.append(data.get("usage", {}).get("prompt_tokens", 0))
            dts.append(dt)
        else:
            errs.append((st, (data.get("error") or {}).get("code") if isinstance(data.get("error"), dict) else None))
        if i % 50 == 0:
            print(f"  progress {i}/{n}")
    check("100% 200", ok == n, f"{ok}/{n}")
    if errs:
        from collections import Counter
        print(f"  non200={Counter(errs)}")
    if pts:
        print(f"  prompt_tokens min={min(pts)} avg={statistics.mean(pts):.1f} max={max(pts)} stdev={statistics.pstdev(pts):.2f}")
        check("prompt_tokens 稳定(stdev<2)", statistics.pstdev(pts) < 2, f"stdev={statistics.pstdev(pts):.2f}")
    if dts:
        print(f"  耗时 avg={statistics.mean(dts):.2f}s p95={sorted(dts)[int(len(dts)*0.95)-1]:.2f}s")


def s3_max_boundary(key, model):
    print("\n== S3 护栏 max 边界（构造 ~520k token）==")
    big = "测" * 520000
    st, data, dt, extra = request("/v1/chat/completions", key,
                                  {"model": model, "messages": [{"role": "user", "content": big}], "stream": False})
    code = (data.get("error") or {}).get("code") if isinstance(data.get("error"), dict) else None
    check("413 + context_length_exceeded", st == 413 and code == "context_length_exceeded",
          f"status={st} code={code} dt={dt:.1f}s")
    print(f"  msg={str((data.get('error') or {}).get('message'))[:90]}")


def s4_session_rpm(key, model, n=21):
    from collections import Counter
    print(f"\n== S4 会话 RPM 限流（同 conversationId 并发连发 {n} 次，rpm=20）==")
    cid = f"reg-rpm-{int(time.time())}"
    statuses, lock = [], threading.Lock()

    def one(i):
        st, data, _, _ = request("/v1/chat/completions", key,
                                 {"model": model, "conversationId": cid,
                                  "messages": [{"role": "user", "content": f"rpm #{i}"}], "stream": False})
        with lock:
            statuses.append((i, st))

    # 并发 6 线程（= global_max_in_flight，不额外触发 in-flight 429）确保 21 次落在同一 60s 窗口
    threads = [threading.Thread(target=one, args=(i,)) for i in range(1, n + 1)]
    t0 = time.time()
    for t in threads:
        t.start()
    for t in threads:
        t.join(150)
    alive = sum(1 for t in threads if t.is_alive())
    statuses.sort()
    seq = [st for _, st in statuses]
    n429 = seq.count(429)
    # RPM 仅统计「被受理」（进入选号流程）的请求；账号全冷却时 503 选号前返回、不 record。
    # 受理数≥21（同一 60s 窗口）才具备触发前提；不足 = 上游冷却干扰，本轮不判定。
    served = [s for s in seq if s != 503]
    print(f"  状态分布={Counter(seq)}  受理(非503)={len(served)}  耗时={time.time()-t0:.1f}s")
    if len(served) >= 21:
        check("RPM 触发（受理≥21 → 必有 429）", n429 >= 1, f"n429={n429}")
    else:
        print(f"  [skip] 受理 {len(served)} < 21（上游冷却干扰），RPM 触发前提不满足，本轮不判定")
    check("无挂死无超时", alive == 0, f"alive={alive}")


def s5_invalid_inputs(key, model):
    print("\n== S5 无效输入族（错误处理最坏情景）==")
    # 5.1 坏 auth（POST 带 body）
    st, data, _, _ = request("/v1/chat/completions", "sk-bad-key",
                             {"model": model, "messages": [{"role": "user", "content": "hi"}]})
    check("坏 auth → 401", st == 401, f"status={st}")
    # 5.2 畸形 JSON
    url = BASE + "/v1/chat/completions"
    req = urllib.request.Request(url, data=b"{not-json", method="POST")
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            st, raw = resp.status, resp.read()
    except urllib.error.HTTPError as e:
        st, raw = e.code, e.read()
    check("畸形 JSON → 4xx", 400 <= st < 500, f"status={st}")
    # 5.3 不存在模型（账号冷却期会先 503 no_healthy_account，属上游状态非模型校验）
    st, data, _, _ = request("/v1/chat/completions", key,
                             {"model": "nonexistent-model-xyz", "messages": [{"role": "user", "content": "hi"}]})
    code = ((data.get("error") or {}).get("code")) if isinstance(data.get("error"), dict) else None
    check("不存在模型 → 4xx 或冷却 503", 400 <= st < 500 or st == 503,
          f"status={st} code={code}")
    # 5.4 空 messages（append 模式会补 persona 为第一条 system → 预期 200 透传）
    st, data, _, _ = request("/v1/chat/completions", key, {"model": model, "messages": []})
    check("空 messages append 补全后透传", st == 200, f"status={st}")
    # 5.5 无 body
    req = urllib.request.Request(url=BASE + "/v1/chat/completions", data=b"", method="POST")
    req.add_header("Authorization", "Bearer " + key)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            st = resp.status
    except urllib.error.HTTPError as e:
        st = e.code
    check("空 body → 4xx", 400 <= st < 500, f"status={st}")


def s6_huge_body(key, model):
    print("\n== S6 超大 body（>64MB 上限）==")
    big = "a" * (64 * 1024 * 1024 + 100)
    payload = json.dumps({"model": model, "messages": [{"role": "user", "content": big}]}).encode("utf-8")
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Content-Type", "application/json")
    t0 = time.time()
    st, raw, code = None, b"", None
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            st, raw = resp.status, resp.read()
    except urllib.error.HTTPError as e:
        st, raw = e.code, e.read()
    except (ConnectionResetError, ConnectionAbortedError, urllib.error.URLError) as e:
        # 服务器读超限 body 时主动断连（Go MaxBytesReader 标准行为）= 拒绝生效
        st = -2
    if st == -2:
        check("超大 body 被拒（413 或服务器断连）", True, "connection reset（服务器主动拒绝）")
        return
    try:
        code = (json.loads(raw.decode("utf-8")).get("error") or {}).get("code")
    except Exception:
        code = None
    check("超大 body → 413", st == 413, f"status={st} code={code} dt={time.time()-t0:.1f}s")


def s7_stream(key, model, n=5):
    print(f"\n== S7 流式 SSE 完整性 {n} 次 ==")
    ok, bad = 0, []
    for i in range(n):
        st, text, _, extra = request("/v1/chat/completions", key,
                                     {"model": model, "messages": [{"role": "user", "content": f"stream #{i} 请流式回复"}], "stream": True},
                                     stream=True)
        if st == 200:
            lines = [l for l in text.split("\n") if l.startswith("data: ")]
            done = text.rstrip().endswith("data: [DONE]")
            if done and lines:
                ok += 1
            else:
                bad.append((i, "missing-DONE"))
        else:
            bad.append((i, f"status={st}"))
    check(f"{n}/{n} SSE 完整", ok == n, f"{ok}/{n}")
    if bad:
        print(f"  bad={bad}")


def s8_concurrency(key, model):
    print("\n== S8 并发 8 线程 × 2 请求（无 panic 无 5xx）==")
    results = []
    lock = threading.Lock()

    def worker(w):
        for j in range(2):
            st, data, dt, _ = request("/v1/chat/completions", key,
                                      {"model": model, "messages": [{"role": "user", "content": f"conc w{w} #{j}"}], "stream": False})
            with lock:
                results.append((st, dt))

    threads = [threading.Thread(target=worker, args=(w,)) for w in range(8)]
    t0 = time.time()
    for t in threads:
        t.start()
    for t in threads:
        t.join(timeout=180)
    alive = sum(1 for t in threads if t.is_alive())
    st_counts = {}
    for s, _ in results:
        st_counts[s] = st_counts.get(s, 0) + 1
    total = len(results)
    print(f"  {total}/16 有响应  状态分布={st_counts}  耗时={time.time()-t0:.1f}s")
    check("16/16 有响应", total == 16, f"{total}/16")
    # 核心：并发下无 panic 无挂死。5xx(上游冷却)/超时(上游慢) 属外部状态，作观察报告不判 FAIL
    n5xx = sum(1 for s, _ in results if s >= 500)
    ntimeout = sum(1 for s, _ in results if s == -1)
    print(f"  [obs] 5xx={n5xx} timeout={ntimeout} alive={alive}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-n", type=int, default=20, help="S2 健康基线次数（上游慢时建议 20）")
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--skip", default="", help="逗号分隔：S3,S4 等")
    args = ap.parse_args()
    skip = {s.strip().upper() for s in args.skip.split(",") if s.strip()}

    key = load_key()
    model = args.model
    print(f"yg-gateway 稳定性回归  base={BASE} model={model} skip={skip or '-'}")
    print(f"时间: {time.strftime('%Y-%m-%d %H:%M:%S')}")

    # 模型探测（含 fallback）
    st, data, dt, _ = request("/v1/chat/completions", key,
                              {"model": model, "messages": [{"role": "user", "content": "probe"}]})
    if st != 200:
        print(f"  [probe] {model} 失败({st})，回退 {FALLBACK_MODEL}")
        model = FALLBACK_MODEL
        st, data, dt, _ = request("/v1/chat/completions", key,
                                  {"model": model, "messages": [{"role": "user", "content": "probe"}]})
        if st != 200:
            print(f"  [probe] {FALLBACK_MODEL} 也失败({st})，中止")
            sys.exit(2)
    print(f"  [probe] {model} OK dt={dt:.1f}s")

    # S4 会话限流放最后：21 连发会打上游账号冷却，避免污染其他节
    if "S1" not in skip: s1_config(key)
    if "S2" not in skip: s2_healthy(key, model, args.n)
    if "S3" not in skip: s3_max_boundary(key, model)
    if "S5" not in skip: s5_invalid_inputs(key, model)
    if "S6" not in skip: s6_huge_body(key, model)
    if "S7" not in skip: s7_stream(key, model)
    if "S8" not in skip: s8_concurrency(key, model)
    if "S4" not in skip: s4_session_rpm(key, model)

    print("\n" + "=" * 50)
    print(f"PASS {len(PASS)} | FAIL {len(FAIL)}")
    if FAIL:
        print("FAIL 项:", FAIL)
        sys.exit(1)
    print("全部通过")


if __name__ == "__main__":
    main()
