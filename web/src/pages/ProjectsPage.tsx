import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useCreateProject, useProjects } from "../api/hooks";
import { allows, useAuth } from "../auth/AuthContext";
import { Empty, ErrorState, InlineError, Loading } from "../components/Feedback";
import { relativeTime } from "../components/format";

export function ProjectsPage() {
  const q = useProjects();
  const { state } = useAuth();
  const [creating, setCreating] = useState(false);
  const canCreate = state.status === "authenticated" && state.me.memberships.some((m) => allows(m.role, "developer"));

  return (
    <>
      <header className="page-head">
        <h1>Projects</h1>
        {canCreate && !creating && <button className="btn btn-primary" onClick={() => setCreating(true)}>New project</button>}
      </header>
      {creating && <CreateProject onDone={() => setCreating(false)} />}
      {q.isPending ? <Loading /> : q.isError ? <ErrorState error={q.error} retry={() => void q.refetch()} /> :
        q.data.data.length === 0 ? <Empty title="No projects yet">Create one to register an application.</Empty> : (
          <div className="grid">
            {q.data.data.map((p) => (
              <Link key={p.id} to={`/projects/${p.id}`} className="card card-link">
                <h3>{p.name}</h3>
                <p className="muted mono">{p.repository_url.replace("https://", "")}</p>
                <p className="muted">branch {p.default_branch} · updated {relativeTime(p.updated_at)}</p>
              </Link>
            ))}
          </div>
        )}
    </>
  );
}

function CreateProject({ onDone }: { onDone: () => void }) {
  const { state } = useAuth();
  const orgs = state.status === "authenticated" ? state.me.memberships.filter((m) => allows(m.role, "developer")) : [];
  const [org, setOrg] = useState(orgs[0]?.organization_id ?? "");
  const [name, setName] = useState("");
  const [repo, setRepo] = useState("");
  const create = useCreateProject();
  const navigate = useNavigate();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate({ organization_id: org, name, repository_url: repo }, { onSuccess: (p) => navigate(`/projects/${p.id}`) });
  };
  return (
    <form className="card form" onSubmit={submit} aria-label="Create project">
      <h2>New project</h2>
      <label>Organization
        <select value={org} onChange={(e) => setOrg(e.target.value)}>{orgs.map((o) => <option key={o.organization_id} value={o.organization_id}>{o.organization_name}</option>)}</select>
      </label>
      <label>Name<input value={name} onChange={(e) => setName(e.target.value)} placeholder="billing-api" required pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" title="lowercase letters, digits and hyphens" /></label>
      <label>Repository (https)<input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/acme/billing-api" required type="url" /></label>
      <InlineError error={create.error} />
      <div className="row">
        <button className="btn btn-primary" disabled={create.isPending}>{create.isPending ? "Creating…" : "Create"}</button>
        <button type="button" className="btn btn-ghost" onClick={onDone}>Cancel</button>
      </div>
    </form>
  );
}
