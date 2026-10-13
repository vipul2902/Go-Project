import { useState } from "react";
import { useAudit } from "../api/hooks";
import { allows, useAuth } from "../auth/AuthContext";
import { Empty, ErrorState, Loading } from "../components/Feedback";
import { relativeTime } from "../components/format";

export function AuditPage() {
  const { state } = useAuth();
  const orgs = state.status === "authenticated" ? state.me.memberships.filter((m) => allows(m.role, "admin")) : [];
  const [org, setOrg] = useState(orgs[0]?.organization_id ?? "");
  const [action, setAction] = useState("");
  const q = useAudit(org, action);

  if (orgs.length === 0) return <><header className="page-head"><h1>Audit log</h1></header><Empty title="The audit log is visible to organization admins" /></>;
  return (
    <>
      <header className="page-head">
        <h1>Audit log</h1>
        <div className="row">
          <select aria-label="Organization" value={org} onChange={(e) => setOrg(e.target.value)}>{orgs.map((o) => <option key={o.organization_id} value={o.organization_id}>{o.organization_name}</option>)}</select>
          <input aria-label="Filter by action" placeholder="action, e.g. deployment.rollback" value={action} onChange={(e) => setAction(e.target.value.trim())} />
        </div>
      </header>
      <section className="card">
        {q.isPending ? <Loading /> : q.isError ? <ErrorState error={q.error} /> : q.data.data.length === 0 ? <Empty title="No matching events" /> : (
          <table className="table">
            <thead><tr><th>When</th><th>Actor</th><th>Action</th><th>Resource</th><th>Details</th><th>IP</th></tr></thead>
            <tbody>
              {q.data.data.map((e) => (
                <tr key={e.id}>
                  <td className="muted" title={e.created_at}>{relativeTime(e.created_at)}</td>
                  <td>{e.actor_email ?? <span className="muted">system</span>}</td>
                  <td><code>{e.action}</code></td>
                  <td className="mono muted">{e.resource_type}{e.resource_id ? ` ${e.resource_id.slice(0, 8)}` : ""}</td>
                  <td className="muted small">{Object.entries(e.metadata).filter(([k]) => k !== "request_id").map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : String(v)}`).join(" · ")}</td>
                  <td className="mono muted">{e.ip_address ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}
