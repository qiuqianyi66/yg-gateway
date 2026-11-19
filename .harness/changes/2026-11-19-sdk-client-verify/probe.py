"""真实 Anthropic SDK 消费 yg-gateway 的流式错误帧。

目的：验证 SDK 2.x 在收到 `event: error` 时是**抛错**还是**静默结束**。
后者会让调用方以为模型答完了 —— 那正是 d03b52e 修掉的网关侧问题，
如果客户端又把错误吞掉，等于白修。

不消耗任何上游额度：实例是空池（total:0），所有请求在到达上游之前被拒。
"""

import sys
import anthropic

BASE = "http://127.0.0.1:17865"
KEY = "verify-key"


def show(label, fn):
    try:
        result = fn()
        print(f"[{label}] 没有抛异常，返回: {result!r}")
        return "no-raise"
    except Exception as e:
        print(f"[{label}] 抛出 {type(e).__name__}: {e}")
        return type(e).__name__


def main():
    client = anthropic.Anthropic(base_url=BASE, api_key=KEY)

    # 场景 1：缺 model（网关本地校验，应 400，不打上游）
    def missing_model():
        return client.messages.create(
            max_tokens=64, messages=[{"role": "user", "content": "hi"}]
        )

    r1 = show("缺model", missing_model)

    # 场景 2：非流式 + 空池（应 503 no_healthy_account）
    def non_stream_empty_pool():
        return client.messages.create(
            model="glm-5.2",
            max_tokens=64,
            messages=[{"role": "user", "content": "hi"}],
        )

    r2 = show("非流式+空池", non_stream_empty_pool)

    # 场景 3：流式 + 空池 —— 这条是核心
    # 网关会先发 message_start / content_block_start，再发 event: error。
    # 问的是：SDK 在读流时会把 error 事件变成异常，还是当正常流结束？
    def stream_empty_pool():
        with client.messages.stream(
            model="glm-5.2",
            max_tokens=64,
            messages=[{"role": "user", "content": "hi"}],
        ) as stream:
            texts = []
            for t in stream.text_stream:
                texts.append(t)
            final = stream.get_final_message()
            return {"texts": texts, "stop_reason": final.stop_reason}

    r3 = show("流式+空池", stream_empty_pool)

    print()
    print("===== 结论 =====")
    print(f"缺model            -> {r1}")
    print(f"非流式+空池         -> {r2}")
    print(f"流式+空池（核心）    -> {r3}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
