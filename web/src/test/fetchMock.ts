import { vi } from "vitest";

export interface Call { method: string; path: string; headers: Headers; body: unknown }
type Responder = (c: Call) => Response | Promise<Response> | undefined;

export const json = (status: number, body?: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

/** Routes fetch calls to responders by "METHOD /path" (path without /api/v1, query ignored). */
export function mockFetch(routes: Record<string, Responder>) {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), "http://localhost");
    const path = url.pathname.replace(/^\/api\/v1/, "");
    const method = (init.method ?? "GET").toUpperCase();
    const call: Call = { method, path, headers: new Headers(init.headers), body: init.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    const r = routes[`${method} ${path}`]?.(call);
    if (!r) throw new Error(`unmocked request: ${method} ${path}`);
    return r;
  });
  vi.stubGlobal("fetch", fn);
  return { calls, fn };
}
