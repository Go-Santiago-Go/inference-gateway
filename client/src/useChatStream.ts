import { useMemo, useRef, useState } from "react";
import type { ChatState, ConversationTotal, Turn, Usage } from "./types";
import { parseFrame, readFrames } from "./sse";

// Vite exposes VITE_API_BASE at build time; the fallback keeps local dev working
// without a .env file.
const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

// A stopped turn never received a usage frame, but a completed assistant turn
// needs one. Zeros are honest: the turn did not finish, so nothing was billed.
const ZERO_USAGE: Usage = { tokensIn: 0, tokensOut: 0, costUsd: 0, latencyMs: 0 };

// total sums the metered usage across completed assistant turns. Cost and tokens
// accumulate; latency does not, because each request is independent.
function total(history: Turn[]): ConversationTotal {
  return history.reduce<ConversationTotal>(
    (acc, turn) => {
      if (turn.role !== "assistant") return acc;
      return {
        turns: acc.turns + 1,
        tokensIn: acc.tokensIn + turn.usage.tokensIn,
        tokensOut: acc.tokensOut + turn.usage.tokensOut,
        costUsd: acc.costUsd + turn.usage.costUsd,
      };
    },
    { turns: 0, tokensIn: 0, tokensOut: 0, costUsd: 0 },
  );
}

/**
 * useChatStream owns the conversation: the completed turn history, the in-flight
 * turn's ChatState, and the running cost total. It exposes send/stop so the
 * component stays presentational. Each send resends the full history, because the
 * gateway is stateless and Bedrock holds no session.
 */
export function useChatStream() {
  const [history, setHistory] = useState<Turn[]>([]);
  const [chatState, setChatState] = useState<ChatState>({ status: "idle" });

  // A ref, not state: the controller is plumbing the UI never renders, and it
  // must persist across renders so stop() can reach the controller send() made.
  const controllerRef = useRef<AbortController | null>(null);

  async function send(prompt: string, apiKey: string) {
    // Append the user turn first, then send the whole history so the model sees
    // the full conversation. The new history ends with this user turn.
    const nextHistory: Turn[] = [...history, { role: "user", content: prompt }];
    setHistory(nextHistory);

    // A fresh controller per request, so a new send never inherits a signal an
    // earlier stop() already aborted.
    const controller = new AbortController();
    controllerRef.current = controller;
    setChatState({ status: "streaming", text: "" });

    // Hoisted above the try so the catch can read whatever streamed before an
    // abort, without nesting state updates.
    let text = "";
    try {
      const res = await fetch(`${API}/v1/chat`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-API-Key": apiKey },
        // Map turns to the wire shape, dropping per-turn usage the model does not need.
        body: JSON.stringify({
          messages: nextHistory.map((t) => ({ role: t.role, content: t.content })),
        }),
        signal: controller.signal,
      });

      // Branch on status before touching the body: a 401 or 429 is a plain error
      // response, not a stream, so there are no SSE frames to read.
      if (res.status === 401) {
        setChatState({ status: "error", kind: "unauthorized" });
        return;
      }
      if (res.status === 429) {
        // Retry-After is the seconds set by the Phase 5 limiter; fall back to 1 so
        // the union's required retryAfter is always a real number.
        const retryAfter = Number(res.headers.get("Retry-After")) || 1;
        setChatState({ status: "error", kind: "rate_limited", retryAfter });
        return;
      }
      if (!res.ok || !res.body) {
        setChatState({ status: "error", kind: "network" });
        return;
      }

      // The running text lives in a local, not chatState: chatState here is the
      // stale value captured when send() started and never updates mid loop, so
      // appending to it would drop tokens.
      let usage: Usage | null = null;
      for await (const frame of readFrames(res.body)) {
        const parsed = parseFrame(frame);
        if (!parsed) continue;
        if (parsed.type === "token") {
          text += parsed.text;
          setChatState({ status: "streaming", text });
        } else {
          usage = parsed.usage;
        }
      }

      // A stream that ended with a usage frame is a completed turn: move it into
      // history and return the in-flight state to idle. One that stopped without
      // usage ended abnormally, so surface a network fault instead.
      if (usage) {
        setHistory((h) => [...h, { role: "assistant", content: text, usage }]);
        setChatState({ status: "idle" });
      } else {
        setChatState({ status: "error", kind: "network" });
      }
    } catch (err) {
      // abort() is the Stop button, a clean stop, not a failure: keep whatever
      // text streamed as a finished (zero-cost) turn instead of an error banner.
      // The updater form reads the latest state, so it captures the text that had
      // streamed by the time Stop was pressed, not the stale closure value.
      if (err instanceof DOMException && err.name === "AbortError") {
        if (text !== "") {
          setHistory((h) => [
            ...h,
            { role: "assistant", content: text, usage: ZERO_USAGE },
          ]);
        }
        setChatState({ status: "idle" });
        return;
      }
      setChatState({ status: "error", kind: "network" });
    }
  }

  function stop() {
    // No-op before the first send, when current is still null.
    controllerRef.current?.abort();
  }

  // reset starts a fresh conversation: abort any in-flight turn, clear the
  // history, and return the in-flight state to idle.
  function reset() {
    controllerRef.current?.abort();
    setHistory([]);
    setChatState({ status: "idle" });
  }

  // clearError dismisses an error state without touching history, so the app can
  // return to idle when a rate-limit countdown ends.
  function clearError() {
    setChatState((prev) => (prev.status === "error" ? { status: "idle" } : prev));
  }

  const conversationTotal = useMemo(() => total(history), [history]);

  return {
    history,
    chatState,
    total: conversationTotal,
    send,
    stop,
    reset,
    clearError,
  };
}
