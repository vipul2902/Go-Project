import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, auth, login as apiLogin, logout as apiLogout, refreshAccessToken } from "../api/client";
import type { Me, Role } from "../api/types";

type State = { status: "loading" } | { status: "anonymous" } | { status: "authenticated"; me: Me };

interface AuthValue {
  state: State;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  /** The user's role in an organization (undefined if not a member). */
  roleIn: (orgId: string) => Role | undefined;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<State>({ status: "loading" });
  const qc = useQueryClient();

  const loadMe = useCallback(async () => {
    const me = await api<Me>("/auth/me");
    setState({ status: "authenticated", me });
  }, []);

  // On page load the access token (memory only) is gone; the HttpOnly
  // refresh cookie silently restores the session if it is still valid.
  useEffect(() => {
    auth.onLost(() => { auth.setToken(null); qc.clear(); setState({ status: "anonymous" }); });
    refreshAccessToken()
      .then((ok) => (ok ? loadMe() : setState({ status: "anonymous" })))
      .catch(() => setState({ status: "anonymous" }));
  }, [loadMe, qc]);

  const value = useMemo<AuthValue>(() => ({
    state,
    login: async (email, password) => { await apiLogin(email, password); await loadMe(); },
    logout: async () => { await apiLogout(); qc.clear(); setState({ status: "anonymous" }); },
    roleIn: (orgId) => (state.status === "authenticated"
      ? state.me.memberships.find((m) => m.organization_id === orgId)?.role : undefined),
  }), [state, loadMe, qc]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const v = useContext(AuthContext);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}

const rank: Record<Role, number> = { viewer: 1, developer: 2, admin: 3 };
/** Mirrors the API's hierarchical RBAC; the API remains the authority. */
export const allows = (have: Role | undefined, need: Role) => !!have && rank[have] >= rank[need];
/** Production changes need admin (matches the API). */
export const roleToChange = (env: string): Role => (env === "production" ? "admin" : "developer");
