import { describe, expect, it } from "vitest";
import { createSSEParser, type SSEMessage } from "./sse";

const stream = 'id: 7\nevent: log\ndata: {"msg":"a"}\n\n: keep-alive\n\nevent: end\ndata: {"status":"RUNNING"}\n\n';

function parseAll(chunks: string[]): SSEMessage[] {
  const out: SSEMessage[] = [];
  const feed = createSSEParser((m) => out.push(m));
  chunks.forEach(feed);
  return out;
}

describe("createSSEParser", () => {
  it("parses events, ids and ignores comments", () => {
    expect(parseAll([stream])).toEqual([
      { event: "log", id: "7", data: '{"msg":"a"}' },
      { event: "end", id: undefined, data: '{"status":"RUNNING"}' },
    ]);
  });

  it("is independent of how the network splits the bytes", () => {
    const whole = parseAll([stream]);
    // Every possible split point, and one character at a time.
    for (let i = 1; i < stream.length; i++) {
      expect(parseAll([stream.slice(0, i), stream.slice(i)])).toEqual(whole);
    }
    expect(parseAll(stream.split(""))).toEqual(whole);
  });

  it("handles CRLF line endings and multi-line data", () => {
    expect(parseAll(["event: log\r\ndata: line1\r\ndata: line2\r\n\r\n"])).toEqual([
      { event: "log", id: undefined, data: "line1\nline2" },
    ]);
  });

  it("does not emit an incomplete event", () => {
    expect(parseAll(["event: log\ndata: partial"])).toEqual([]);
  });
});
