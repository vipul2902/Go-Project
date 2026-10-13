// Server state via TanStack Query: caching, background refetch, polling,
// and invalidation after mutations, instead of hand-rolled useEffect fetches.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type {
  AuditEntry, Deployment, Environment, Health, List, Me, Project, Resources, Summary, Variable,
} from "./types";

export const keys = {
  me: ["me"] as const,
  summary: ["summary"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["project", id] as const,
  environments: (pid: string) => ["environments", pid] as const,
  variables: (pid: string, env: string) => ["variables", pid, env] as const,
  health: (pid: string, env: string) => ["health", pid, env] as const,
  deployments: (pid?: string) => ["deployments", pid ?? "all"] as const,
  deployment: (id: string) => ["deployment", id] as const,
  audit: (org: string, action: string) => ["audit", org, action] as const,
};

const isActive = (s: string) => s === "QUEUED" || s === "BUILDING" || s === "DEPLOYING";

export const useMe = (enabled = true) => useQuery({ queryKey: keys.me, queryFn: () => api<Me>("/auth/me"), enabled });

export const useSummary = () =>
  useQuery({ queryKey: keys.summary, queryFn: () => api<Summary>("/dashboard"), refetchInterval: 10_000 });

export const useProjects = () =>
  useQuery({ queryKey: keys.projects, queryFn: () => api<List<Project>>("/projects?limit=100&sort=name") });

// enabled: callers may not know the id yet (e.g. while the deployment that
// references the project is loading); never request /projects/ with no id.
export const useProject = (id: string) =>
  useQuery({ queryKey: keys.project(id), queryFn: () => api<Project>(`/projects/${id}`), enabled: id !== "" });

export const useEnvironments = (pid: string) =>
  useQuery({ queryKey: keys.environments(pid), queryFn: () => api<List<Environment>>(`/projects/${pid}/environments`) });

export const useVariables = (pid: string, env: string) =>
  useQuery({ queryKey: keys.variables(pid, env), queryFn: () => api<List<Variable>>(`/projects/${pid}/environments/${env}/variables`) });

/** Live environment health, polled while the page is open. 503 (no cluster) is not retried. */
export const useHealth = (pid: string, env: string) =>
  useQuery({
    queryKey: keys.health(pid, env),
    queryFn: () => api<Health>(`/projects/${pid}/environments/${env}/health`),
    refetchInterval: 10_000,
    retry: false,
  });

export const useDeployments = (pid?: string, limit = 50) =>
  useQuery({
    queryKey: keys.deployments(pid),
    queryFn: () => api<List<Deployment>>(`/deployments?limit=${limit}${pid ? `&project_id=${pid}` : ""}`),
    // Poll faster while something is in flight.
    refetchInterval: (q) => (q.state.data?.data.some((d) => isActive(d.status)) ? 3_000 : 15_000),
  });

export const useDeployment = (id: string) =>
  useQuery({
    queryKey: keys.deployment(id),
    queryFn: () => api<Deployment>(`/deployments/${id}`),
    refetchInterval: (q) => (q.state.data && isActive(q.state.data.status) ? 2_000 : false),
  });

export const useAudit = (org: string, action: string) =>
  useQuery({
    queryKey: keys.audit(org, action),
    queryFn: () => api<List<AuditEntry>>(`/organizations/${org}/audit?limit=100${action ? `&action=${encodeURIComponent(action)}` : ""}`),
    enabled: !!org,
  });

function useInvalidate() {
  const qc = useQueryClient();
  return (...queryKeys: readonly unknown[][]) => Promise.all(queryKeys.map((k) => qc.invalidateQueries({ queryKey: k })));
}

export function useCreateProject() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (in_: { organization_id: string; name: string; repository_url: string; default_branch?: string }) =>
      api<Project>("/projects", { method: "POST", body: in_ }),
    onSuccess: () => invalidate([...keys.projects], [...keys.summary]),
  });
}

export function useCreateEnvironment(pid: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (in_: { name: string; port?: number; health_check_path?: string }) =>
      api<Environment>(`/projects/${pid}/environments`, { method: "POST", body: in_ }),
    onSuccess: () => invalidate([...keys.environments(pid)]),
  });
}

export function useUpdateEnvironment(pid: string, env: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (patch: Partial<{ replicas: number; resources: Resources; port: number; health_check_path: string; auto_rollback: boolean }>) =>
      api<Environment>(`/projects/${pid}/environments/${env}`, { method: "PATCH", body: patch }),
    onSuccess: () => invalidate([...keys.environments(pid)]),
  });
}

export function useSetVariable(pid: string, env: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ key, ...body }: { key: string; value?: string; secret_ref?: string }) =>
      api<Variable>(`/projects/${pid}/environments/${env}/variables/${encodeURIComponent(key)}`, { method: "PUT", body }),
    onSuccess: () => invalidate([...keys.variables(pid, env)]),
  });
}

export function useDeleteVariable(pid: string, env: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (key: string) =>
      api<void>(`/projects/${pid}/environments/${env}/variables/${encodeURIComponent(key)}`, { method: "DELETE" }),
    onSuccess: () => invalidate([...keys.variables(pid, env)]),
  });
}

/**
 * Deploy with an Idempotency-Key: the caller generates ONE key per user
 * action and reuses it for retries, so a double click or a retry after a
 * timeout can never start two deployments.
 */
export function useDeploy() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ idempotencyKey, ...body }: { project_id: string; environment: string; commit_sha: string; idempotencyKey: string }) =>
      api<Deployment>("/deployments", { method: "POST", body, headers: { "Idempotency-Key": idempotencyKey } }),
    onSuccess: (d) => invalidate([...keys.deployments(d.project_id)], [...keys.deployments()], [...keys.summary]),
  });
}

export function useCancelDeployment() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (id: string) => api<Deployment>(`/deployments/${id}/cancel`, { method: "POST" }),
    onSuccess: (d) => invalidate([...keys.deployment(d.id)], [...keys.deployments(d.project_id)]),
  });
}

export function useRollback() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ targetId, idempotencyKey }: { targetId: string; idempotencyKey: string }) =>
      api<Deployment>(`/deployments/${targetId}/rollback`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey } }),
    onSuccess: (d) => invalidate([...keys.deployments(d.project_id)], [...keys.deployments()], [...keys.summary]),
  });
}

export const newIdempotencyKey = () => crypto.randomUUID();
