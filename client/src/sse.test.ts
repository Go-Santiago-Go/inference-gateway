import { describe, it, expect } from "vitest";
import { parseFrame } from "./sse";

describe("parseFrame", () => {
  it("reads a single-line token frame", () => {
    expect(parseFrame("data: Hello")).toEqual({ type: "token", text: "Hello" });
  });

  it("rejoins a multi-line token frame with newlines", () => {
    // The gateway encodes "Hello\n\nWorld" as three data lines; the parser must
    // put the newlines back. This is the round trip the "data" vs "data:" bug broke.
    const frame = "data: Hello\ndata: \ndata: World";
    expect(parseFrame(frame)).toEqual({ type: "token", text: "Hello\n\nWorld" });
  });

  it("parses the usage frame and converts to camelCase", () => {
    const frame =
      'event: usage\ndata: {"tokens_in":12,"tokens_out":8,"cost_usd":0.0001,"latency_ms":340}';
    expect(parseFrame(frame)).toEqual({
      type: "usage",
      usage: { tokensIn: 12, tokensOut: 8, costUsd: 0.0001, latencyMs: 340 },
    });
  });

  it("returns null for a frame with no data lines", () => {
    expect(parseFrame(": comment")).toBeNull();
  });
});
