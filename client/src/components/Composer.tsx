type ComposerProps = {
  prompt: string;
  onPromptChange: (value: string) => void;
  onSubmit: () => void;
  onStop: () => void;
  canSend: boolean;
};

/**
 * Composer is the prompt input plus the Send/Stop controls. It is presentational:
 * the parent owns the prompt value and the send/stop actions. The Send and Stop
 * buttons both render; app.css swaps them based on the shell's data-status.
 */
export function Composer({
  prompt,
  onPromptChange,
  onSubmit,
  onStop,
  canSend,
}: ComposerProps) {
  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    onSubmit();
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    // Enter sends; Shift+Enter inserts a newline.
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      onSubmit();
    }
  }

  return (
    <form className="composer" onSubmit={handleSubmit}>
      <div className="inputwrap">
        <textarea
          rows={1}
          value={prompt}
          onChange={(e) => onPromptChange(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Message the gateway..."
          aria-label="Message the gateway"
        />
        <button
          type="button"
          className="round round--stop"
          onClick={onStop}
          aria-label="Stop"
        >
          &#9632;
        </button>
        <button
          type="submit"
          className="round round--send"
          disabled={!canSend}
          aria-label="Send"
        >
          &#8593;
        </button>
      </div>
    </form>
  );
}
