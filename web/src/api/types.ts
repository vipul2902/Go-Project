// Types mirroring the CloudDeploy API (docs/api.md).

export type Role = "admin" | "developer" | "viewer";
export type DeploymentStatus = "QUEUED" | "BUILDING" | "DEPLOYING" | "RUNNING" | "FAILED" | "CANCELLED";
export type EnvironmentName = "development" | "staging" | "production";

export interface User { id: string; email: string; name: string }
export interface Membership { organization_id: string; organization_name: string; organization_slug: string; role: Role }
export interface Me { user: User; memberships: Membership[] }

export interface List<T> { data: T[]; next_cursor?: string | null }

export interface Project {
  id: string; organization_id: string; name: string; repository_url: string;
  default_branch: string; created_at: string; updated_at: string;
}

export interface Resources { cpu_request: string; cpu_limit: string; memory_request: string; memory_limit: string }

export interface Environment {
  id: string; project_id: string; name: EnvironmentName; namespace: string; replicas: number;
  resources: Resources; port: number; health_check_path: string | null; auto_rollback: boolean;
  created_at: string; updated_at: string;
}

export interface Variable { key: string; value: string | null; secret: boolean; secret_ref: string | null; updated_at: string }

export interface Deployment {
  id: string; project_id: string; project_name: string; environment_id: string; environment: EnvironmentName;
  commit_sha: string; image: string | null; status: DeploymentStatus;
  triggered_by: { id: string; name: string } | null; rollback_of: string | null; error_message: string | null;
  config: { namespace: string; replicas: number; resources: Resources; port: number; variables: Variable[] };
  created_at: string; started_at: string | null; completed_at: string | null; duration_seconds: number | null;
  warnings?: string[];
}

export interface LogLine { id: number; level: "debug" | "info" | "warn" | "error"; message: string; created_at: string }
export interface AppLogLine { pod: string; time: string; line: string }

export type HealthStatus = "healthy" | "progressing" | "failing" | "degraded" | "down" | "scaled_to_zero" | "not_deployed";
export interface PodStatus {
  name: string; phase: string; ready: boolean; restarts: number; deployment_id: string;
  reason?: string; message?: string; last_exit_code?: number; age_seconds: number; terminating: boolean;
}
export interface Health {
  environment: string; namespace: string; status: HealthStatus;
  replicas: { desired: number; updated: number; ready: number; available: number; total: number };
  live_deployment_id?: string; pods: PodStatus[];
  latest_succeeded_deployment: Deployment | null; drift: boolean; drift_reasons: string[]; checked_at: string;
}

export interface Summary {
  projects: number; environments: number; active_deployments: number;
  last_30_days: Record<DeploymentStatus, number>; success_rate_30d: number | null; recent_deployments: Deployment[];
}

export interface AuditEntry {
  id: number; actor_id: string | null; actor_email: string | null; action: string; resource_type: string;
  resource_id: string | null; metadata: Record<string, unknown>; ip_address: string | null; created_at: string;
}
