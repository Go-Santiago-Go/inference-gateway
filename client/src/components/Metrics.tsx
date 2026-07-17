import type { Usage } from "../types";

/** Metrics renders the per-request usage strip beneath a completed reply. */
export function Metrics({ usage }: { usage: Usage }) {
  return (
    <div className="metrics">
      <span className="metrics__chip">{usage.tokensIn} tokens in</span>
      <span className="metrics__chip">{usage.tokensOut} tokens out</span>
      <span className="metrics__chip">${usage.costUsd.toFixed(4)}</span>
      <span className="metrics__chip">{usage.latencyMs} ms</span>
    </div>
  );
}
