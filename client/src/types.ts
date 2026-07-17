/**
 * Usage is the per-request accounting the gateway reports in its final SSE frame.
 *
 * Field names are camelCase while the wire format is snake_case (`tokens_in`,
 * `cost_usd`, `latency_ms`). The rename happens once, in the SSE parser, so the
 * gateway's JSON shape stays out of the components.
 */
export type Usage = {
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
  latencyMs: number;
};

/**
 * UsageFrame is the gateway's `event: usage` payload in its wire naming.
 *
 * The parser converts it to a Usage, which is the only place snake_case exists
 * in the client; nothing downstream sees the gateway's JSON shape.
 */
export type UsageFrame = {
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  latency_ms: number;
};

/**
 * ChatState is the request lifecycle, modeled so illegal states cannot be built.
 *
 * Each member carries only the data that state owns: there is no `usage` before
 * the stream completes, and no `text` on a request that never started. Switching
 * on `status` narrows to one member, so the compiler rejects reads that a boolean
 * flag would have allowed at runtime. The error cases split on `kind` for the same
 * reason, which is what makes `retryAfter` required where it applies and absent
 * everywhere else.
 */
export type ChatState =
  | { status: "idle" }
  | { status: "streaming"; text: string }
  | { status: "done"; text: string; usage: Usage }
  | { status: "error"; kind: "unauthorized" }
  | { status: "error"; kind: "rate_limited"; retryAfter: number }
  | { status: "error"; kind: "network" };

/**
 * Turn is one completed message in the conversation history. A user turn is just
 * text; an assistant turn also carries the usage the gateway metered for it, so
 * each reply can show its own cost and the conversation can sum them.
 */
export type Turn =
  | { role: "user"; content: string }
  | { role: "assistant"; content: string; usage: Usage };

/**
 * ConversationTotal is the running cost of the whole conversation. Tokens and
 * cost accumulate because a stateless gateway resends the full history every
 * turn, so a conversation costs more than the sum of its prompts in isolation.
 * Latency is deliberately absent: each request is independent, so a summed
 * latency would be meaningless.
 */
export type ConversationTotal = {
  turns: number;
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
};
