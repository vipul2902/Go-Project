import { useState } from "react";
import { useEnvironments } from "../api/hooks";
import type { AppLogLine } from "../api/types";
import { allows } from "../auth/AuthContext";
import { Empty, ErrorState, Loading } from "../components/Feedback";
import { LogViewer, type ViewLine } from "../components/LogViewer";
import { useProjectCtx } from "./ProjectPage";

let seq = 0;
const parseAppLine = (data: unknown): ViewLine => {
  const l = data as AppLogLine;
  return { key: `${l.pod}-${l.time}-${seq++}`, prefix: l.pod.split("-").slice(-1)[0], text: l.line };
};

export function LogsPage() {
  const { project, role } = useProjectCtx();
  const envs = useEnvironments(project.id);
  const [env, setEnv] = useState<string>("");
  if (!allows(role, "developer")) {
    return <Empty title="Application logs need the developer role">App output often contains sensitive data.</Empty>;
  }
  if (envs.isPending) return <Loading />;
  if (envs.isError) return <ErrorState error={envs.error} />;
  const list = envs.data.data;
  const selected = env || list[0]?.name || "";
  if (!selected) return <Empty title="No environments yet" />;
  return (
    <section className="card">
      <div className="row between">
        <h2>Application logs</h2>
        <label>Environment <select value={selected} onChange={(e) => setEnv(e.target.value)}>{list.map((e) => <option key={e.id}>{e.name}</option>)}</select></label>
      </div>
      <LogViewer key={selected} path={`/projects/${project.id}/environments/${selected}/logs?follow=true&tail=200`} parse={parseAppLine}
        emptyText="No output yet. The app may not print anything." height={520} />
    </section>
  );
}
