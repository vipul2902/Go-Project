import { useEffect, useRef, useState } from "react";
import { streamSSE } from "../api/sse";

export interface ViewLine { key: string; level?: string; prefix?: string; text: string }

/**
 * Follows an SSE log endpoint. Give it a `key` that changes with `path` so
 * switching streams remounts it with empty state.
 * Lines arrive as "log" events; parse() maps
 * each payload to a display line. Reconnects with Last-Event-ID so nothing
 * repeats after a dropped connection, and stops at the "end" event.
 */
export function LogViewer(props: {
  path: string;
  parse: (data: unknown, id?: string) => ViewLine;
  onEnd?: (data: unknown) => void;
  emptyText?: string;
  height?: number;
}) {
  const { path, parse, onEnd } = props;
  const [lines, setLines] = useState<ViewLine[]>([]);
  const [state, setState] = useState<"connecting" | "live" | "ended" | "error">("connecting");
  const [follow, setFollow] = useState(true);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    let lastId: string | undefined;
    let ended = false;
    (async () => {
      for (let attempt = 0; !ctrl.signal.aborted && !ended && attempt < 20; attempt++) {
        try {
          setState("connecting");
          await streamSSE(path, {
            signal: ctrl.signal,
            lastEventId: lastId,
            onMessage: (m) => {
              setState("live");
              const data = m.data ? JSON.parse(m.data) : null;
              if (m.event === "end") { ended = true; setState("ended"); onEnd?.(data); return; }
              if (m.event === "log") {
                if (m.id) lastId = m.id;
                setLines((prev) => [...prev.slice(-1999), parse(data, m.id)]);
              }
            },
          });
          if (!ended) await new Promise((r) => setTimeout(r, 1000)); // server closed: reconnect
        } catch {
          if (ctrl.signal.aborted) return;
          setState("error");
          await new Promise((r) => setTimeout(r, 2000));
        }
      }
    })();
    return () => ctrl.abort();
  }, [path, parse, onEnd]);

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);

  return (
    <div className="logs">
      <div className="logs-bar">
        <span className={`live live-${state}`}>{{ connecting: "connecting…", live: "live", ended: "finished", error: "reconnecting…" }[state]}</span>
        <label><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> auto-scroll</label>
      </div>
      <div className="logs-body" ref={box} style={{ height: props.height ?? 420 }} role="log" aria-live="polite">
        {lines.length === 0 && <div className="logs-empty">{props.emptyText ?? "Waiting for output…"}</div>}
        {lines.map((l) => (
          <div key={l.key} className={`logline lvl-${l.level ?? "info"}`}>
            {l.prefix && <span className="logprefix">{l.prefix}</span>}
            <span>{l.text}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
