import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { useAuth } from "./auth/AuthContext";
import { Layout } from "./components/Layout";
import { Loading } from "./components/Feedback";
import { LoginPage } from "./pages/LoginPage";
import { DashboardPage } from "./pages/DashboardPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ProjectPage } from "./pages/ProjectPage";
import { ProjectDeployments, ProjectOverview } from "./pages/ProjectOverview";
import { EnvironmentsPage } from "./pages/EnvironmentsPage";
import { LogsPage } from "./pages/LogsPage";
import { DeploymentPage } from "./pages/DeploymentPage";
import { InfrastructurePage } from "./pages/InfrastructurePage";
import { AuditPage } from "./pages/AuditPage";

function RequireAuth() {
  const { state } = useAuth();
  const location = useLocation();
  if (state.status === "loading") return <Loading label="Restoring session…" />;
  if (state.status === "anonymous") return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <Layout />;
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireAuth />}>
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/projects" element={<ProjectsPage />} />
        <Route path="/projects/:id" element={<ProjectPage />}>
          <Route index element={<ProjectOverview />} />
          <Route path="deployments" element={<ProjectDeployments />} />
          <Route path="environments" element={<EnvironmentsPage />} />
          <Route path="logs" element={<LogsPage />} />
        </Route>
        <Route path="/deployments/:id" element={<DeploymentPage />} />
        <Route path="/infrastructure" element={<InfrastructurePage />} />
        <Route path="/audit" element={<AuditPage />} />
        <Route path="*" element={<Navigate to="/dashboard" replace />} />
      </Route>
    </Routes>
  );
}
