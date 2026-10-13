import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { LogViewer } from "./LogViewer";

afterEach(() => vi.unstubAllGlobals());

/** A Response whose body streams the given SSE text in small chunks. */
function sseResponse(text: string): Response {
  const bytes = new TextEncoder().encode(text);
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      for (let i = 0; i < bytes.length; i += 7) c.enqueue(bytes.slice(i, i + 7)); // split mid-frame
      c.close();
    },
  });
  return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

const line = (id: number, message: string) =>
  `id: ${id}\nevent: log\ndata: ${JSON.stringify({ id, level: "info", message, created_at: "2026-10-13T10:00:00Z" })}\n\n`;

describe("LogViewer", () => {
  it("resumes after a dropped connection with Last-Event-ID, without duplicates, and stops at end", async () => {
    const lastEventIds: (string | null)[] = [];
    let connection = 0;
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
      lastEventIds.push(new Headers(init.headers).get("Last-Event-ID"));
      connection++;
      // First connection drops after two lines (no "end"); the second
      // resumes from line 2 and finishes.
      return connection === 1
        ? sseResponse(line(1, "claimed") + line(2, "building"))
        : sseResponse(line(3, "deploying") + `event: end\ndata: {"status":"RUNNING"}\n\n`);
    }));
    const onEnd = vi.fn();
    render(<LogViewer path="/deployments/d1/logs?follow=true"
      parse={(d) => { const l = d as { id: number; message: string }; return { key: String(l.id), text: l.message }; }}
      onEnd={onEnd} />);

    await waitFor(() => expect(onEnd).toHaveBeenCalledWith({ status: "RUNNING" }), { timeout: 4000 });
    expect(lastEventIds).toEqual([null, "2"]); // resumed after the last line received
    const texts = screen.getAllByText(/claimed|building|deploying/).map((e) => e.textContent);
    expect(texts).toEqual(["claimed", "building", "deploying"]); // in order, no duplicates
    expect(screen.getByText("finished")).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 1200));
    expect(connection).toBe(2); // no reconnect after "end"
  });
});
