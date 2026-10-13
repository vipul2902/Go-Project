import { Link } from "react-router-dom";
import { useEnvironments, useHealth, useProjects } from "../api/hooks";
import type { Environment, Project } from "../api/types";
import { EnvTag, HealthBadge } from "../components/Badges";
import { Empty, ErrorState, Loading } from "../components/Feedback";

/** Every environment across projects: where it runs and how it is doing.
 *  Cloud infrastructure (Terraform: AKS, ACR, databases) arrives in Phase 17. */
export function InfrastructurePage() {
  const q = useProjects();
  return (
    <>
      <header className="page-head"><h1>Infrastructure</h1></header>
      <section className="card">
        <h2>Environments</h2>
        {q.isPending ? <Loading /> : q.isError ? <ErrorState error={q.error} /> : q.data.data.length === 0 ? <Empty title="No projects" /> : (
          <table className="table">
            <thead><tr><th>Project</th><th>Environment</th><th>Namespace</th><th>Replicas</th><th>Resources (limits)</th><th>Health</th></tr></thead>
            <tbody>{q.data.data.map((p) => <ProjectRows key={p.id} project={p} />)}</tbody>
          </table>
        )}
      </section>
      <section className="card muted-card">
        <h2>Cloud resources</h2>
        <p className="muted">Provisioning the platform itself (resource group, container registry, AKS, managed PostgreSQL and Redis) is defined with Terraform in a later phase. This page will list those resources and their state.</p>
      </section>
    </>
  );
}

function ProjectRows({ project }: { project: Project }) {
  const envs = useEnvironments(project.id);
  if (!envs.data) return null;
  return <>{envs.data.data.map((e) => <EnvRow key={e.id} project={project} env={e} />)}</>;
}

function EnvRow({ project, env }: { project: Project; env: Environment }) {
  const h = useHealth(project.id, env.name);
  return (
    <tr>
      <td><Link to={`/projects/${project.id}`}>{project.name}</Link></td>
      <td><EnvTag name={env.name} /></td>
      <td className="mono">{env.namespace}</td>
      <td>{h.data ? `${h.data.replicas.available}/${h.data.replicas.desired}` : env.replicas}</td>
      <td className="mono">{env.resources.cpu_limit} · {env.resources.memory_limit}</td>
      <td>{h.data ? <HealthBadge status={h.data.status} /> : <span className="muted">{h.isPending ? "…" : "unavailable"}</span>}</td>
    </tr>
  );
}
