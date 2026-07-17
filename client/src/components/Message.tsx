import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Usage } from "../types";
import { Metrics } from "./Metrics";

type MessageProps = {
  text: string;
  streaming: boolean;
  usage?: Usage;
};

/**
 * Message renders the assistant reply: an avatar, the streamed text with a
 * blinking cursor while streaming, and the metrics strip once usage arrives.
 */
export function Message({ text, streaming, usage }: MessageProps) {
  return (
    <div className="row">
      <div className="avatar" aria-hidden="true">
        &#9889;
      </div>
      <div className="reply">
        {/* react-markdown is safe by default: it escapes raw HTML and neutralizes
            javascript: URLs. No rehype-raw, so no sanitizer is needed. remark-gfm
            adds tables and strikethrough. The tree re-parses each token as the
            text grows, which is cheap at this size and is how streaming UIs work. */}
        {/* Only the streaming reply is a live region; completed history messages
            are static, so marking them live would create competing announcers. */}
        <div className="reply__text markdown" aria-live={streaming ? "polite" : undefined}>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
          {streaming && <span className="cursor" aria-hidden="true" />}
        </div>
        {usage && <Metrics usage={usage} />}
      </div>
    </div>
  );
}
