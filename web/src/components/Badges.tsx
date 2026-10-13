import type { DeploymentStatus, HealthStatus } from "../api/types";

const deployTone: Record<DeploymentStatus, string> = {
  QUEUED: "neutral", BUILDING: "info", DEPLOYING: "info", RUNNING: "ok", FAILED: "bad", CANCELLED: "muted",
};

export function StatusBadge({ status }: { status: DeploymentStatus }) {
  const active = status === "QUEUED" || status === "BUILDING" || status === "DEPLOYING";
  return (
    <span className={`badge tone-${deployTone[status]}`} data-status={status}>
      {active && <span className="pulse" aria-hidden />}
      {status.toLowerCase()}
    </span>
  );
}

const healthTone: Record<HealthStatus, string> = {
  healthy: "ok", progressing: "info", failing: "bad", degraded: "warn", down: "bad", scaled_to_zero: "muted", not_deployed: "muted",
};

export function HealthBadge({ status }: { status: HealthStatus }) {
  return <span className={`badge tone-${healthTone[status]}`}>{status.replaceAll("_", " ")}</span>;
}

export function EnvTag({ name }: { name: string }) {
  return <span className={`env env-${name}`}>{name}</span>;
}
