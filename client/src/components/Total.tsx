import type { ConversationTotal } from "../types";

/**
 * Total renders the running cost of the conversation. It exists to make the
 * resend-cost story visible: because the stateless gateway resends the full
 * history each turn, tokens and cost climb faster than turn count alone suggests.
 */
export function Total({ total }: { total: ConversationTotal }) {
  return (
    <div className="total" role="status" aria-label="Conversation total">
      <span className="total__label">conversation</span>
      <span className="total__chip">{total.turns} turns</span>
      <span className="total__chip">{total.tokensIn} tokens in</span>
      <span className="total__chip">{total.tokensOut} tokens out</span>
      <span className="total__chip total__chip--cost">
        ${total.costUsd.toFixed(4)}
      </span>
    </div>
  );
}
