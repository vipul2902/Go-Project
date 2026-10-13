// Server-Sent Events over fetch. The browser's EventSource cannot send an
// Authorization header, so we read the stream ourselves and parse the SSE
// wire format (https://html.spec.whatwg.org/multipage/server-sent-events.html).

import { authFetch } from "./client";

export interface SSEMessage { event: string; id?: string; data: string }

/**
 * Returns a function that accepts arbitrary text chunks (frames may be split
 * anywhere by the network) and calls onMessage for each complete event.
 */
export function createSSEParser(onMessage: (m: SSEMessage) => void): (chunk: string) => void {
  let buffer = "";
  let event = "message";
  let id: string | undefined;
  let data: string[] = [];

  const dispatch = () => {
    if (data.length > 0) onMessage({ event, id, data: data.join("\n") });
    event = "message";
    id = undefined;
    data = [];
  };

  return (chunk: string) => {
    buffer += chunk;
    let nl: number;
    while ((nl = buffer.search(/\r\n|\r|\n/)) >= 0) {
      const line = buffer.slice(0, nl);
      buffer = buffer.slice(nl + (buffer.startsWith("\r\n", nl) ? 2 : 1));
      if (line === "") { dispatch(); continue; }
      if (line.startsWith(":")) continue; // comment (heartbeat)
      const colon = line.indexOf(":");
      const field = colon < 0 ? line : line.slice(0, colon);
      let value = colon < 0 ? "" : line.slice(colon + 1);
      if (value.startsWith(" ")) value = value.slice(1);
      if (field === "event") event = value;
      else if (field === "data") data.push(value);
      else if (field === "id") id = value;
    }
  };
}

/** Streams events from an authenticated SSE endpoint until it ends or signal aborts. */
export async function streamSSE(
  path: string,
  opts: { signal: AbortSignal; onMessage: (m: SSEMessage) => void; lastEventId?: string },
): Promise<void> {
  const headers: Record<string, string> = { Accept: "text/event-stream" };
  if (opts.lastEventId) headers["Last-Event-ID"] = opts.lastEventId;
  const res = await authFetch(path, { headers, signal: opts.signal });
  if (!res.ok || !res.body) throw new Error(`stream failed: HTTP ${res.status}`);
  const feed = createSSEParser(opts.onMessage);
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      feed(value);
    }
  } finally {
    reader.releaseLock();
  }
}
