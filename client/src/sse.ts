import type { Usage, UsageFrame } from "./types";

export type ParsedFrame = 
| { type: "token"; text: string }
| { type: "usage"; usage: Usage}

const SEP = "\n\n"
/**
 * readFrames yields complete SSE frames from a fetch response body
 * 
 * Network reads split anywhere, so a single read can carry half a frame or
 * several; the buffer holds the unterminated remainder until the next read
 * completes it. TextDecoder's streaming mode does the same for a multi-byte
 * character split across reads.
 */
export async function* readFrames(
    body: ReadableStream<Uint8Array>,
): AsyncGenerator<string> {
    const reader = body.getReader()
    const decoder = new TextDecoder();
    let buffer = "";

    for (;;) {
        const { value, done } = await reader.read()
        if (done) break;
        // stream:true holds back a multi-byte character split across reads
        // instead of decoding half of it to a replacement character.
        buffer += decoder.decode(value, { stream: true});

        // Drain every complete frame this read delivered, not just the first:
        // one read can carry both a token frame and the trailing usage frame.
        let i: number;
        while ((i = buffer.indexOf(SEP)) !== -1) {
            const frame = buffer.slice(0, i);
            buffer = buffer.slice(i + SEP.length);
            if (frame !== "") yield frame
        }
    }
}

/**
 * parseFrame turns one SSE frame into a token or the usage summary, or null if
 * it carries no data. Multiple data lines are rejoined with "\n", reversing the
 * gateway's per-line encoding of text that contained newlines.
 */
export function parseFrame(frame: string): ParsedFrame | null {
    let event = "message";
    const data: string[] = [];

    for (const line of frame.split("\n")) {
        if (line.startsWith("event:")) {
            event = line.slice("event:".length).trim();
        } else if (line.startsWith("data:")) {
            // Strip only one leading space after the colon: that is the space
            // the spec adds, and eating more would drop real leading whitespace.
            data.push(line.slice("data:".length).replace(/^ /, ""));
        }
    }

    if (data.length === 0) return null;
    const payload = data.join("\n");

    if (event === "usage") {
        // The one unavoidable unsafe cast: JSON.parse returns any, so this names
        // the wire contract rather than verifying it. snake_case stops here.
        const w = JSON.parse(payload) as UsageFrame;
        return {
            type: "usage",
            usage: {
                tokensIn: w.tokens_in,
                tokensOut: w.tokens_out,
                costUsd: w.cost_usd,
                latencyMs: w.latency_ms,
            },
        }
    }
    return { type: "token", text: payload }
}