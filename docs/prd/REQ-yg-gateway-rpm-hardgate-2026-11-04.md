# REQ — yg-gateway 出站 RPM 硬闸（rpmTransport，可配置默认关）

## 必要性

出站侧没有任何按账号的速率闸：上游风控按短窗突发判定，「短时间把一个号打热」是养号
场景最直接的封号路径，池内并发一高就会集体触发限流（429 → 冷却 → 可用号骤减）。
magpie 的 60s 滑动窗 + RoundTripper 卡槽位是可借鉴的成熟做法；把它做成可配置能力
（默认关）才能让「多号并发」变成可控吞吐，而不是赌上游风控的容忍度。

日期：2026-11-04
来源：REA ④可借鉴清单补审（P0 最高优先级真实缺口，重开项）
基线：`769fec2`
方法：doubao-coding-develop-backend-features（先红后绿）

## 背景

REA：magpie `rpm.go` 60s+1s 滑动窗 + RoundTripper 卡槽位，按 providerAccount
限 RPM——"短时间把一个号打热"是养号场景最直接的封号路径，yg 当前无出站前
强制槽位。P1-1 校准结论（8 天观测支撑 0 关）只回答了**默认值**维度，不覆盖
**机制**维度。本项实现机制为可配置能力（默认关，与校准结论不冲突）。

## 概念区分

- `pool.min_request_interval`：**选号侧**——选号时跳过/等待该账号（0 关已校准）。
- `upstream.rpm_per_account`（本项）：**出站侧**——所有出站请求发出前按账号
  卡槽位（RoundTripper 层，覆盖 chat/billing/models/签到全路径）。

## 需求（Given-When-Then）

Given `upstream.rpm_per_account = N`（>0）
When 单账号在 60s 窗口内的出站请求数 < N 且距上次出站 ≥ 1s
Then 请求立即放行

Given 单账号 60s 窗口内已有 N 个出站请求（窗口满）
When 再发出站请求
Then 阻塞等待最早槽位滑出窗口后放行（**不失败、不换号**），ctx 取消时返回
`ctx.Err()`（不悬挂）

Given 同一请求流中两个不同账号（X-Machine-ID 互异）
When 并发出站
Then 各自独立限速，互不占用对方槽位

Given 请求无 X-Machine-ID（理论上全出站已注入；防御兜底）
When 出站
Then 归入全局桶，受同一 N 约束（不绕过硬闸）

Given `upstream.rpm_per_account = 0`（默认）
When 任意出站请求
Then 行为与旧版完全一致（零等待，零回归）

## 非目标

- 不改 `pool.min_request_interval` 的 0 关结论（选号侧，已校准）。
- 不做独立并发信号量：60s 窗口 + 1s 槽位即平滑突发（长 SSE 请求只占 1s 槽，
  不饿死队列）。
- 不做热生效（装配期字段，需重启；与 SanitizeFingerprints 的热改区分）。

## 验收（测试可证伪）

- limiter 单测（注入可控时钟）：窗口满等待最早滑出、槽位 1s 最小间隔、ctx 取消。
- RoundTripper 分桶测试：两账号独立、无头归 global、RpmPerAccount=0 直放。
- 现有 30 包测试全绿（零回归）。
