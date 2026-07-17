import { useLayoutEffect, useRef, useState } from "react";
import { useChatStream } from "./useChatStream";
import { useTheme } from "./useTheme";
import type { ChatState } from "./types";
import { Sidebar } from "./components/Sidebar";
import { Composer } from "./components/Composer";
import { Message } from "./components/Message";
import { Total } from "./components/Total";

// Seeded for demo convenience; matches the API_KEYS=testkey docker example.
const DEFAULT_KEY = import.meta.env.VITE_API_KEY ?? "testkey";

// The data-status attribute mirrors the ChatState tag, except an error surfaces
// its kind so the CSS can color per error, not just "error".
function statusAttr(state: ChatState): string {
  return state.status === "error" ? state.kind : state.status;
}

function pillLabel(state: ChatState): string {
  switch (state.status) {
    case "streaming":
      return "streaming";
    case "error":
      return state.kind === "unauthorized"
        ? "unauthorized"
        : state.kind === "rate_limited"
          ? "rate limited"
          : "disconnected";
    default:
      return "connected";
  }
}

function bannerText(state: Extract<ChatState, { status: "error" }>): string {
  switch (state.kind) {
    case "unauthorized":
      return "Unauthorized. Check your API key and try again.";
    case "rate_limited":
      return `Rate limited. Try again in ${state.retryAfter}s.`;
    case "network":
      return "Couldn't reach the gateway. Retry.";
  }
}

export default function App() {
  const { history, chatState, total, send, stop, reset } = useChatStream();
  const { theme, toggle } = useTheme();
  const [prompt, setPrompt] = useState("");
  const [apiKey, setApiKey] = useState(DEFAULT_KEY);

  // The conversation's title is its first user message; null before anything sent.
  const title = history.find((t) => t.role === "user")?.content ?? null;

  // Stick-to-bottom autoscroll. threadRef is the scroll container; stick tracks
  // whether the user is near the bottom. We only pin to the bottom while they
  // are, so scrolling up to read history is not fought by incoming tokens.
  const threadRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  function handleThreadScroll() {
    const el = threadRef.current;
    if (!el) return;
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }

  // useLayoutEffect, not useEffect: pin before the browser paints so the view
  // never flashes the pre-scroll position. Runs on every token and turn.
  useLayoutEffect(() => {
    const el = threadRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [history, chatState]);

  function handleSend() {
    // Guard against a second request while one streams: the Send button is hidden
    // then, but the Enter key would otherwise still start an overlapping turn.
    if (chatState.status === "streaming") return;
    const trimmed = prompt.trim();
    if (!trimmed) return;
    send(trimmed, apiKey);
    setPrompt("");
  }

  return (
    <div className="shell" data-status={statusAttr(chatState)}>
      <Sidebar
        apiKey={apiKey}
        onApiKeyChange={setApiKey}
        title={title}
        onNewChat={reset}
      />

      <main className="main">
        <header className="top">
          <div>
            <p className="top__title">inference-gateway</p>
            <p className="top__sub">claude-haiku · streaming</p>
          </div>
          <div className="top__right">
            <button
              type="button"
              className="themetoggle"
              onClick={toggle}
              aria-label={
                theme === "dark" ? "Switch to light mode" : "Switch to dark mode"
              }
            >
              {theme === "dark" ? "☀" : "☾"}
            </button>
            <span className="pill" role="status">
              <span className="pill__dot" aria-hidden="true" />
              {pillLabel(chatState)}
            </span>
          </div>
        </header>

        <div className="thread" ref={threadRef} onScroll={handleThreadScroll}>
          {/* Index keys are safe here: history only ever appends, so a turn's
              position never changes and React never has to reconcile a reorder. */}
          {history.map((turn, i) =>
            turn.role === "user" ? (
              <div key={i} className="row row--user">
                <div className="bubble">{turn.content}</div>
              </div>
            ) : (
              <Message
                key={i}
                text={turn.content}
                streaming={false}
                usage={turn.usage}
              />
            ),
          )}

          {chatState.status === "error" && (
            <div className={`banner banner--${chatState.kind}`} role="alert">
              {bannerText(chatState)}
            </div>
          )}

          {chatState.status === "streaming" && (
            <Message text={chatState.text} streaming />
          )}
        </div>

        {total.turns > 0 && <Total total={total} />}

        <Composer
          prompt={prompt}
          onPromptChange={setPrompt}
          onSubmit={handleSend}
          onStop={stop}
          canSend={prompt.trim().length > 0}
        />
      </main>
    </div>
  );
}
