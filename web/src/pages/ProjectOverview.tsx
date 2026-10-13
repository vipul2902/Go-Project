import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { newIdempotencyKey, useDeploy, useDeployments, useEnvironments, useHealth } from "../api/hooks";
import type { Environment } from "../api/types";
import { allows, roleToChange } from "../auth/AuthContext";
import { EnvTag, HealthBadge, StatusBadge } from "../components/Badges";
import { Empty, ErrorState, InlineError, Loading } from "../components/Feedback";
import { duration, relativeTime, shortId, shortSha } from "../components/format";
import { useProjectCtx } from "./ProjectPage";
import { ApiError } from "../api/client";

export function ProjectOverview() {
  const { project, role } = useProjectCtx();
  const envs = useEnvironments(project.id);
  const deployments = useDeployments(project.id, 10);

  return (
    <>
      <section aria-label="Environments" className="grid">
        {envs.isPending ? <Loading /> : envs.isError ? <ErrorState error={envs.error} /> :
          envs.data.data.length === 0 ? <Empty title="No environments yet"><Link to="environments">Create one</Link> to deploy.</Empty> :
          envs.data.data.map((e) => <EnvironmentCard key={e.id} env={e} projectId={project.id} />)}
      </section>
      {envs.data && envs.data.data.length > 0 && allows(role, "developer") && <DeployForm envs={envs.data.data} />}
      <section className="card">
        <h2>Recent deployments</h2>
        {deployments.isPending ? <Loading /> : deployments.isError ? <ErrorState error={deployments.error} /> :
          <DeploymentTable rows={deployments.data.data} />}
      </section>
    </>
  );
}

function EnvironmentCard({ env, projectId }: { env: Environment; projectId: string }) {
  const h = useHealth(projectId, env.name);
  const unavailable = h.error instanceof ApiError && h.error.status === 503;
  return (
    <div className="card env-card">
      <div className="row between"><EnvTag name={env.name} />{h.data && <HealthBadge status={h.data.status} />}</div>
      <p className="muted mono">{env.namespace}</p>
      {h.isPending && <Loading label="Checking health…" />}
      {unavailable && <p className="muted">Live health unavailable (API has no cluster access).</p>}
      {h.isError && !unavailable && <InlineError error={h.error} />}
      {h.data && (
        <>
          <div className="pods" aria-label="Pods">
            {h.data.pods.map((p) => (
              <span key={p.name} title={`${p.name}${p.reason ? ` · ${p.reason}` : ""} · restarts ${p.restarts}`}
                className={`pod ${p.terminating ? "pod-off" : p.ready ? "pod-ok" : p.reason ? "pod-bad" : "pod-wait"}`} />
            ))}
            <span className="muted">{h.data.replicas.available}/{h.data.replicas.desired} available</span>
          </div>
          {h.data.drift && (
            <div className="drift" role="note"><strong>Drift:</strong> {h.data.drift_reasons.join("; ")}</div>
          )}
          {h.data.latest_succeeded_deployment && (
            <p className="muted">Live: <Link to={`/deployments/${h.data.latest_succeeded_deployment.id}`}><code>{shortSha(h.data.latest_succeeded_deployment.commit_sha)}</code></Link> · {relativeTime(h.data.latest_succeeded_deployment.completed_at)}</p>
          )}
        </>
      )}
    </div>
  );
}

/** Deploy form. One Idempotency-Key per submitted intent: retries of the same
 *  click reuse it, so a double click never starts two deployments. */
function DeployForm({ envs }: { envs: Environment[] }) {
  const { project, role } = useProjectCtx();
  const deployable = envs.filter((e) => allows(role, roleToChange(e.name)));
  const [env, setEnv] = useState(deployable[0]?.name ?? "");
  const [sha, setSha] = useState("");
  const keyRef = useRef<string | null>(null);
  const deploy = useDeploy();
  const navigate = useNavigate();

  if (deployable.length === 0) return null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    keyRef.current ??= newIdempotencyKey();
    deploy.mutate(
      { project_id: project.id, environment: env, commit_sha: sha.trim(), idempotencyKey: keyRef.current },
      { onSuccess: (d) => { keyRef.current = null; navigate(`/deployments/${d.id}`); } },
    );
  };
  const inProgress = deploy.error instanceof ApiError && deploy.error.code === "DEPLOYMENT_IN_PROGRESS";
  return (
    <form className="card form deploy" onSubmit={submit} aria-label="Deploy">
      <h2>Deploy</h2>
      <div className="row">
        <label>Environment
          <select value={env} onChange={(e) => { setEnv(e.target.value); keyRef.current = null; }}>
            {deployable.map((e) => <option key={e.id} value={e.name}>{e.name}</option>)}
          </select>
        </label>
        <label className="grow">Commit SHA
          <input value={sha} onChange={(e) => { setSha(e.target.value); keyRef.current = null; }} placeholder="e.g. bbed28c" required pattern="[0-9a-fA-F]{7,40}" title="7-40 hex characters" />
        </label>
        <button className="btn btn-primary" disabled={deploy.isPending}>{deploy.isPending ? "Queuing…" : "Deploy"}</button>
      </div>
      {inProgress ? (
        <p className="inline-error" role="alert">Another deployment is in progress for {env}.{" "}
          <Link to={`/deployments/${(deploy.error as ApiError).details?.active_deployment_id}`}>Watch it</Link></p>
      ) : <InlineError error={deploy.error} />}
    </form>
  );
}

export function DeploymentTable({ rows }: { rows: import("../api/types").Deployment[] }) {
  if (rows.length === 0) return <Empty title="No deployments yet" />;
  return (
    <table className="table">
      <thead><tr><th>ID</th><th>Environment</th><th>Status</th><th>Commit</th><th>By</th><th>Duration</th><th>Created</th></tr></thead>
      <tbody>
        {rows.map((d) => (
          <tr key={d.id}>
            <td><Link to={`/deployments/${d.id}`} className="mono">{shortId(d.id)}</Link></td>
            <td><EnvTag name={d.environment} /></td>
            <td><StatusBadge status={d.status} /></td>
            <td><code>{shortSha(d.commit_sha)}</code>{d.rollback_of && <span className="tag">rollback</span>}</td>
            <td>{d.triggered_by?.name ?? <span className="muted">system</span>}</td>
            <td>{duration(d.duration_seconds)}</td>
            <td className="muted">{relativeTime(d.created_at)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function ProjectDeployments() {
  const { project } = useProjectCtx();
  const q = useDeployments(project.id, 100);
  return (
    <section className="card">
      <h2>Deployment history</h2>
      {q.isPending ? <Loading /> : q.isError ? <ErrorState error={q.error} retry={() => void q.refetch()} /> : <DeploymentTable rows={q.data.data} />}
    </section>
  );
}
