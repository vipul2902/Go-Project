import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, auth } from "./client";
import { json, mockFetch } from "../test/fetchMock";

afterEach(() => { vi.unstubAllGlobals(); auth.setToken(null); });

describe("api client", () => {
  it("sends the bearer token and parses the error envelope", async () => {
    auth.setToken("t1");
    const { calls } = mockFetch({
      "GET /projects/x": () => json(404, { error: { code: "PROJECT_NOT_FOUND", message: "project not found", request_id: "r1" } }),
    });
    const err = await api("/projects/x").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 404, code: "PROJECT_NOT_FOUND", requestId: "r1" });
    expect(calls[0]?.headers.get("Authorization")).toBe("Bearer t1");
  });

  it("refreshes ONCE for concurrent 401s, then retries each request", async () => {
    // Two refreshes with the same cookie would look like token theft to the
    // API (reuse detection) and revoke the whole session.
    auth.setToken("expired");
    let refreshes = 0;
    const { calls } = mockFetch({
      "POST /auth/refresh": async (c) => {
        refreshes++;
        expect(c.headers.get("X-Auth-Mode")).toBe("cookie");
        await new Promise((r) => setTimeout(r, 20));
        return json(200, { access_token: "fresh" });
      },
      "GET /dashboard": (c) => (c.headers.get("Authorization") === "Bearer fresh" ? json(200, { ok: true }) : json(401, {})),
    });
    const results = await Promise.all(Array.from({ length: 5 }, () => api<{ ok: boolean }>("/dashboard")));
    expect(results.every((r) => r.ok)).toBe(true);
    expect(refreshes).toBe(1);
    expect(calls.filter((c) => c.path === "/dashboard")).toHaveLength(10); // 5 rejected + 5 retried
  });

  it("reports the session lost when refresh fails", async () => {
    const lost = vi.fn();
    auth.onLost(lost);
    mockFetch({ "POST /auth/refresh": () => json(401, {}), "GET /dashboard": () => json(401, {}) });
    await expect(api("/dashboard")).rejects.toBeInstanceOf(ApiError);
    expect(lost).toHaveBeenCalled();
  });

  it("sends JSON bodies and returns undefined for 204", async () => {
    const { calls } = mockFetch({ "DELETE /x": () => new Response(null, { status: 204 }), "POST /y": () => json(201, { id: 1 }) });
    await expect(api("/x", { method: "DELETE" })).resolves.toBeUndefined();
    await api("/y", { method: "POST", body: { a: 1 } });
    expect(calls[1]?.body).toEqual({ a: 1 });
    expect(calls[1]?.headers.get("Content-Type")).toBe("application/json");
  });
});
