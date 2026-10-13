import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AuthProvider } from "../auth/AuthContext";
import { App } from "../App";
import { auth } from "../api/client";
import { json, mockFetch } from "../test/fetchMock";

afterEach(() => { vi.unstubAllGlobals(); auth.setToken(null); });

const me = {
  user: { id: "u1", email: "dev@example.com", name: "Dev" },
  memberships: [{ organization_id: "o1", organization_name: "Acme", organization_slug: "acme", role: "developer" }],
};
const project = { id: "p1", organization_id: "o1", name: "web", repository_url: "https://github.com/acme/web", default_branch: "main", created_at: "", updated_at: "" };
const envs = { data: [{ id: "e1", project_id: "p1", name: "development", namespace: "web-dev", replicas: 1, port: 8080,
  health_check_path: null, auto_rollback: true, resources: { cpu_request: "1", cpu_limit: "1", memory_request: "1", memory_limit: "1" }, created_at: "", updated_at: "" }] };

function renderApp(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <AuthProvider><Routes><Route path="/*" element={<App />} /></Routes></AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("login", () => {
  it("shows the API's error for wrong credentials, then signs in", async () => {
    let attempt = 0;
    mockFetch({
      "POST /auth/refresh": () => json(401, {}), // no session yet
      "POST /auth/login": (c) => {
        expect(c.headers.get("X-Auth-Mode")).toBe("cookie");
        return ++attempt === 1
          ? json(401, { error: { code: "INVALID_CREDENTIALS", message: "invalid email or password" } })
          : json(200, { access_token: "t" });
      },
      "GET /auth/me": () => json(200, me),
      "GET /dashboard": () => json(200, { projects: 0, environments: 0, active_deployments: 0, success_rate_30d: null, recent_deployments: [],
        last_30_days: { QUEUED: 0, BUILDING: 0, DEPLOYING: 0, RUNNING: 0, FAILED: 0, CANCELLED: 0 } }),
    });
    renderApp("/dashboard");
    await screen.findByRole("form", { name: "Sign in" });
    await userEvent.type(screen.getByLabelText("Email"), "dev@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "wrong-password");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("invalid email or password");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
  });
});

describe("deploy form", () => {
  it("reuses one Idempotency-Key for retries of the same deploy, and a new key for a new intent", async () => {
    const keys: string[] = [];
    let n = 0;
    mockFetch({
      "POST /auth/refresh": () => json(200, { access_token: "t" }),
      "GET /auth/me": () => json(200, me),
      "GET /projects/p1": () => json(200, project),
      "GET /projects/p1/environments": () => json(200, envs),
      "GET /projects/p1/environments/development/health": () => json(503, { error: { code: "MONITORING_UNAVAILABLE", message: "no cluster" } }),
      "GET /deployments": () => json(200, { data: [] }),
      "POST /deployments": (c) => {
        keys.push(c.headers.get("Idempotency-Key") ?? "");
        // First attempt: a transient failure. Second: in progress.
        return ++n === 1
          ? json(500, { error: { code: "INTERNAL_ERROR", message: "internal server error" } })
          : json(409, { error: { code: "DEPLOYMENT_IN_PROGRESS", message: "busy", details: { active_deployment_id: "d9" } } });
      },
    });
    renderApp("/projects/p1");
    const sha = await screen.findByLabelText("Commit SHA");
    await userEvent.type(sha, "abc1234");
    const button = screen.getByRole("button", { name: "Deploy" });
    await userEvent.click(button);
    await screen.findByText("internal server error");
    await userEvent.click(button); // retry of the SAME intent
    expect(await screen.findByRole("link", { name: "Watch it" })).toHaveAttribute("href", "/deployments/d9");
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);

    await userEvent.clear(sha);
    await userEvent.type(sha, "def5678"); // a different deploy: new key
    await userEvent.click(button);
    await waitFor(() => expect(keys).toHaveLength(3));
    expect(keys[2]).not.toBe(keys[0]);
  });
});
