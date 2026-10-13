import { useCallback, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { keys, newIdempotencyKey, useCancelDeployment, useDeployment, useRollback } from "../api/hooks";
import type { LogLine } from "../api/types";
import { allows, roleToChange, useAuth } from "../auth/AuthContext";
import { useProject } from "../api/hooks";
import { EnvTag, StatusBadge } from "../components/Badges";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { ErrorState, InlineError, Loading } from "../components/Feedback";
import { LogViewer, type ViewLine } from "../components/LogViewer";
import { Timeline } from "../components/Timeline";
import { duration, relativeTime, shortId, shortSha } from "../components/format";

const parsePipelineLine = (data: unknown, id?: string): ViewLine => {
  const l = data as LogLine;
  return { key: id ?? String(l.id), level: l.level, prefix: new Date(l.created_at).toLocaleTimeString(), text: l.message };
};

export function DeploymentPage() {
  const { id = "" } = useParams();
  const q = useDeployment(id);
  const project = useProject(q.data?.project_id ?? "");
  const { roleIn } = useAuth();
  const qc = useQueryClient();
  const cancel = useCancelDeployment();
  const rollback = useRollback();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState<null | "cancel" | "rollback">(null);
  const [rollbackKey] = useState(newIdempotencyKey);

  // When the stream ends, refresh the deployment (final status, duration).
  const onEnd = useCallback(() => void qc.invalidateQueries({ queryKey: keys.deployment(id) }), [qc, id]);

  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorState error={q.error} retry={() => void q.refetch()} />;
  const d = q.data;
  const role = project.data ? roleIn(project.data.organization_id) : undefined;
  const canChange = allows(role, roleToChange(d.environment));
  const active = d.status === "QUEUED" || d.status === "BUILDING" || d.status === "DEPLOYING";

  return (
    <>
      <header className="page-head">
        <div>
          <p className="muted"><Link to={`/projects/${d.project_id}`}>{d.project_name}</Link> / deployment <span className="mono">{shortId(d.id)}</span></p>
          <h1 className="row"><StatusBadge status={d.status} /> <EnvTag name={d.environment} /> <code>{shortSha(d.commit_sha)}</code></h1>
        </div>
        {canChange && (
          <div className="row">
            {active && <button className="btn btn-ghost" onClick={() => setConfirm("cancel")}>Cancel</button>}
            {d.status === "RUNNING" && <button className="btn btn-ghost" onClick={() => setConfirm("rollback")}>Roll back to this</button>}
          </div>
        )}
      </header>

      <section className="card"><Timeline d={d} /></section>

      <section className="grid grid-2">
        <div className="card">
          <h2>Details</h2>
          <dl className="kv">
            <dt>Triggered by</dt><dd>{d.triggered_by?.name ?? "system (auto-rollback)"}</dd>
            <dt>Created</dt><dd>{relativeTime(d.created_at)}</dd>
            <dt>Duration</dt><dd>{duration(d.duration_seconds)}</dd>
            <dt>Image</dt><dd className="mono">{d.image ?? "—"}</dd>
            {d.rollback_of && <><dt>Rolls back to</dt><dd><Link to={`/deployments/${d.rollback_of}`} className="mono">{shortId(d.rollback_of)}</Link></dd></>}
          </dl>
          {d.error_message && <p className="inline-error" role="alert">{d.error_message}</p>}
        </div>
        <div className="card">
          <h2>Configuration snapshot</h2>
          <dl className="kv">
            <dt>Namespace</dt><dd className="mono">{d.config.namespace}</dd>
            <dt>Replicas</dt><dd>{d.config.replicas}</dd>
            <dt>Port</dt><dd>{d.config.port}</dd>
            <dt>CPU</dt><dd>{d.config.resources.cpu_request} / {d.config.resources.cpu_limit}</dd>
            <dt>Memory</dt><dd>{d.config.resources.memory_request} / {d.config.resources.memory_limit}</dd>
            <dt>Variables</dt><dd>{d.config.variables.length === 0 ? "—" : d.config.variables.map((v) => <span key={v.key} className="tag">{v.key}{v.secret ? " 🔒" : ""}</span>)}</dd>
          </dl>
        </div>
      </section>

      <section className="card">
        <h2>Pipeline log</h2>
        <LogViewer key={d.id} path={`/deployments/${d.id}/logs?follow=true`} parse={parsePipelineLine} onEnd={onEnd} />
      </section>

      <ConfirmDialog open={confirm === "cancel"} title="Cancel this deployment?" confirmLabel="Cancel deployment" danger
        busy={cancel.isPending} onCancel={() => setConfirm(null)}
        onConfirm={() => cancel.mutate(d.id, { onSettled: () => setConfirm(null) })}>
        <p>The running step is stopped. Pods already rolled out keep running until the next deployment.</p>
        <InlineError error={cancel.error} />
      </ConfirmDialog>

      <ConfirmDialog open={confirm === "rollback"} title={`Roll ${d.environment} back to ${shortSha(d.commit_sha)}?`}
        confirmLabel="Roll back" danger={d.environment === "production"} busy={rollback.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={() => rollback.mutate({ targetId: d.id, idempotencyKey: rollbackKey }, {
          onSuccess: (rb) => { setConfirm(null); navigate(`/deployments/${rb.id}`); },
        })}>
        <p>A new deployment restores this release exactly: commit <code>{shortSha(d.commit_sha)}</code> and its configuration snapshot. The environment’s settings are not changed.</p>
        <InlineError error={rollback.error} />
      </ConfirmDialog>
    </>
  );
}
