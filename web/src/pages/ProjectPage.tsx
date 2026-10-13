import { NavLink, Outlet, useOutletContext, useParams } from "react-router-dom";
import { useProject } from "../api/hooks";
import type { Project, Role } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, Loading } from "../components/Feedback";

export interface ProjectCtx { project: Project; role: Role | undefined }
export const useProjectCtx = () => useOutletContext<ProjectCtx>();

/** Project shell: header + tabs; child routes get the project and role. */
export function ProjectPage() {
  const { id = "" } = useParams();
  const q = useProject(id);
  const { roleIn } = useAuth();
  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorState error={q.error} retry={() => void q.refetch()} />;
  const project = q.data;
  const tabs = [
    { to: "", label: "Overview", end: true },
    { to: "deployments", label: "Deployments" },
    { to: "environments", label: "Environments" },
    { to: "logs", label: "Logs" },
  ];
  return (
    <>
      <header className="page-head">
        <div>
          <h1>{project.name}</h1>
          <a className="muted mono" href={project.repository_url} target="_blank" rel="noreferrer noopener">{project.repository_url.replace("https://", "")}</a>
        </div>
        <span className="tag">your role: {roleIn(project.organization_id) ?? "none"}</span>
      </header>
      <nav className="tabs" aria-label="Project sections">
        {tabs.map((t) => (
          <NavLink key={t.label} to={t.to} end={t.end} className={({ isActive }) => `tab${isActive ? " active" : ""}`}>{t.label}</NavLink>
        ))}
      </nav>
      <Outlet context={{ project, role: roleIn(project.organization_id) } satisfies ProjectCtx} />
    </>
  );
}
