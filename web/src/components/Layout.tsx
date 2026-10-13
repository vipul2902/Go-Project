import { NavLink, Outlet } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";

const nav = [
  { to: "/dashboard", label: "Dashboard" },
  { to: "/projects", label: "Projects" },
  { to: "/infrastructure", label: "Infrastructure" },
  { to: "/audit", label: "Audit log" },
];

export function Layout() {
  const { state, logout } = useAuth();
  const me = state.status === "authenticated" ? state.me : null;
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand"><span className="logo" aria-hidden>▲</span> CloudDeploy</div>
        <nav aria-label="Main">
          {nav.map((n) => (
            <NavLink key={n.to} to={n.to} className={({ isActive }) => `navlink${isActive ? " active" : ""}`}>{n.label}</NavLink>
          ))}
        </nav>
        {me && (
          <div className="whoami">
            <div className="whoami-name">{me.user.name}</div>
            <div className="whoami-meta">{me.user.email}</div>
            {me.memberships.map((m) => <div key={m.organization_id} className="whoami-meta">{m.organization_name} · {m.role}</div>)}
            <button className="btn btn-ghost btn-sm" onClick={() => void logout()}>Sign out</button>
          </div>
        )}
      </aside>
      <main className="content"><Outlet /></main>
    </div>
  );
}
