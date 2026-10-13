import { Link } from "react-router-dom";
import { useSummary } from "../api/hooks";
import { EnvTag, StatusBadge } from "../components/Badges";
import { Empty, ErrorState, Loading } from "../components/Feedback";
import { duration, relativeTime, shortSha } from "../components/format";

export function DashboardPage() {
  const q = useSummary();
  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorState error={q.error} retry={() => void q.refetch()} />;
  const s = q.data;
  const rate = s.success_rate_30d == null ? "—" : `${Math.round(s.success_rate_30d * 100)}%`;

  return (
    <>
      <header className="page-head"><h1>Dashboard</h1></header>
      <section className="stats" aria-label="Summary">
        <Stat label="Projects" value={s.projects} />
        <Stat label="Active deployments" value={s.active_deployments} tone={s.active_deployments ? "info" : undefined} />
        <Stat label="Successful (30d)" value={s.last_30_days.RUNNING} tone="ok" />
        <Stat label="Failed (30d)" value={s.last_30_days.FAILED} tone={s.last_30_days.FAILED ? "bad" : undefined} />
        <Stat label="Success rate (30d)" value={rate} />
      </section>
      <section className="card">
        <h2>Recent activity</h2>
        {s.recent_deployments.length === 0 ? (
          <Empty title="No deployments yet">Open a project and press Deploy.</Empty>
        ) : (
          <table className="table">
            <thead><tr><th>Project</th><th>Environment</th><th>Status</th><th>Commit</th><th>By</th><th>Duration</th><th>When</th></tr></thead>
            <tbody>
              {s.recent_deployments.map((d) => (
                <tr key={d.id}>
                  <td><Link to={`/projects/${d.project_id}`}>{d.project_name}</Link></td>
                  <td><EnvTag name={d.environment} /></td>
                  <td><Link to={`/deployments/${d.id}`}><StatusBadge status={d.status} /></Link></td>
                  <td><code>{shortSha(d.commit_sha)}</code>{d.rollback_of && <span className="tag">rollback</span>}</td>
                  <td>{d.triggered_by?.name ?? <span className="muted">system</span>}</td>
                  <td>{duration(d.duration_seconds)}</td>
                  <td className="muted">{relativeTime(d.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}

function Stat({ label, value, tone }: { label: string; value: number | string; tone?: string }) {
  return (
    <div className={`stat${tone ? ` tone-${tone}` : ""}`}>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}
