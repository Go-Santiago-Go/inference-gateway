type SidebarProps = {
  apiKey: string;
  onApiKeyChange: (value: string) => void;
  // The current conversation's title (its first user message), or null before
  // anything has been sent.
  title: string | null;
  onNewChat: () => void;
};

/**
 * Sidebar is the rail: brand, the current conversation, the New chat control, and
 * the live API key field. The conversation list reflects the one active chat, so
 * the rail tracks real state rather than showing placeholder history.
 */
export function Sidebar({
  apiKey,
  onApiKeyChange,
  title,
  onNewChat,
}: SidebarProps) {
  return (
    <aside className="rail">
      <div className="brand">
        <div className="brand__logo" aria-hidden="true">
          &#9889;
        </div>
        <div>
          <p className="brand__name">inference-gateway</p>
          <p className="brand__sub">Bedrock console</p>
        </div>
      </div>

      <button type="button" className="newchat" onClick={onNewChat}>
        <span aria-hidden="true">+</span> New chat
      </button>

      <ul className="convs" role="list">
        {title && (
          <li className="conv" aria-current="true">
            {title}
          </li>
        )}
      </ul>

      <div className="rail__foot">
        <label className="keyfield">
          API key
          <input
            type="text"
            value={apiKey}
            onChange={(e) => onApiKeyChange(e.target.value)}
            spellCheck={false}
            autoComplete="off"
          />
        </label>
      </div>
    </aside>
  );
}
