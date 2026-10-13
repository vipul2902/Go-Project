import { useState, type FormEvent } from "react";
import { useCreateEnvironment, useDeleteVariable, useEnvironments, useSetVariable, useUpdateEnvironment, useVariables } from "../api/hooks";
import type { Environment } from "../api/types";
import { allows, roleToChange } from "../auth/AuthContext";
import { EnvTag } from "../components/Badges";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Empty, ErrorState, InlineError, Loading } from "../components/Feedback";
import { useProjectCtx } from "./ProjectPage";

const allEnvs = ["development", "staging", "production"] as const;

export function EnvironmentsPage() {
  const { project, role } = useProjectCtx();
  const q = useEnvironments(project.id);
  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorState error={q.error} retry={() => void q.refetch()} />;
  const existing = new Set(q.data.data.map((e) => e.name));
  const creatable = allEnvs.filter((n) => !existing.has(n) && allows(role, roleToChange(n)));
  return (
    <>
      {q.data.data.map((e) => <EnvironmentSection key={e.id} env={e} />)}
      {q.data.data.length === 0 && <Empty title="No environments yet" />}
      {creatable.length > 0 && <CreateEnvironment names={creatable} />}
    </>
  );
}

function CreateEnvironment({ names }: { names: string[] }) {
  const { project } = useProjectCtx();
  const [name, setName] = useState(names[0] ?? "");
  const [port, setPort] = useState(8080);
  const create = useCreateEnvironment(project.id);
  const submit = (e: FormEvent) => { e.preventDefault(); create.mutate({ name, port }); };
  return (
    <form className="card form" onSubmit={submit} aria-label="Create environment">
      <h2>Add environment</h2>
      <div className="row">
        <label>Name<select value={name} onChange={(e) => setName(e.target.value)}>{names.map((n) => <option key={n}>{n}</option>)}</select></label>
        <label>Port<input type="number" min={1} max={65535} value={port} onChange={(e) => setPort(Number(e.target.value))} /></label>
        <button className="btn btn-primary" disabled={create.isPending}>Create</button>
      </div>
      <InlineError error={create.error} />
    </form>
  );
}

function EnvironmentSection({ env }: { env: Environment }) {
  const { project, role } = useProjectCtx();
  const canEdit = allows(role, roleToChange(env.name));
  const update = useUpdateEnvironment(project.id, env.name);
  const [form, setForm] = useState({
    replicas: env.replicas, port: env.port, health: env.health_check_path ?? "", auto: env.auto_rollback, ...env.resources,
  });
  const set = (k: string, v: string | number | boolean) => setForm((f) => ({ ...f, [k]: v }));
  const save = (e: FormEvent) => {
    e.preventDefault();
    update.mutate({
      replicas: form.replicas, port: form.port, health_check_path: form.health, auto_rollback: form.auto,
      resources: { cpu_request: form.cpu_request, cpu_limit: form.cpu_limit, memory_request: form.memory_request, memory_limit: form.memory_limit },
    });
  };

  return (
    <section className="card">
      <div className="row between"><h2 className="row"><EnvTag name={env.name} /> <span className="mono muted">{env.namespace}</span></h2>
        {!canEdit && <span className="tag">read-only{env.name === "production" ? " (production needs admin)" : ""}</span>}</div>
      <form className="form form-grid" onSubmit={save} aria-label={`${env.name} configuration`}>
        <fieldset disabled={!canEdit}>
          <label>Replicas<input type="number" min={0} max={20} value={form.replicas} onChange={(e) => set("replicas", Number(e.target.value))} /></label>
          <label>Port<input type="number" min={1} max={65535} value={form.port} onChange={(e) => set("port", Number(e.target.value))} /></label>
          <label>Health path<input value={form.health} placeholder="/healthz (empty: TCP check)" onChange={(e) => set("health", e.target.value)} /></label>
          <label>CPU request<input value={form.cpu_request} onChange={(e) => set("cpu_request", e.target.value)} /></label>
          <label>CPU limit<input value={form.cpu_limit} onChange={(e) => set("cpu_limit", e.target.value)} /></label>
          <label>Memory request<input value={form.memory_request} onChange={(e) => set("memory_request", e.target.value)} /></label>
          <label>Memory limit<input value={form.memory_limit} onChange={(e) => set("memory_limit", e.target.value)} /></label>
          <label className="check"><input type="checkbox" checked={form.auto} onChange={(e) => set("auto", e.target.checked)} /> Auto-rollback on failed rollout</label>
          {canEdit && <button className="btn btn-primary" disabled={update.isPending}>{update.isPending ? "Saving…" : "Save configuration"}</button>}
          {update.isSuccess && <span className="ok-text">Saved. Applies to the next deployment.</span>}
        </fieldset>
        <InlineError error={update.error} />
      </form>
      <Variables env={env} canEdit={canEdit} />
    </section>
  );
}

function Variables({ env, canEdit }: { env: Environment; canEdit: boolean }) {
  const { project } = useProjectCtx();
  const q = useVariables(project.id, env.name);
  const setVar = useSetVariable(project.id, env.name);
  const del = useDeleteVariable(project.id, env.name);
  const [key, setKey] = useState("");
  const [val, setVal] = useState("");
  const [secret, setSecret] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const add = (e: FormEvent) => {
    e.preventDefault();
    setVar.mutate(secret ? { key, secret_ref: val } : { key, value: val }, { onSuccess: () => { setKey(""); setVal(""); } });
  };
  return (
    <div className="vars">
      <h3>Variables</h3>
      {q.isPending ? <Loading /> : q.isError ? <ErrorState error={q.error} /> : (
        <table className="table">
          <thead><tr><th>Key</th><th>Value</th><th /></tr></thead>
          <tbody>
            {q.data.data.length === 0 && <tr><td colSpan={3} className="muted">No variables</td></tr>}
            {q.data.data.map((v) => (
              <tr key={v.key}>
                <td className="mono">{v.key}</td>
                <td className="mono">{v.secret ? <span title="Kubernetes Secret reference; the value never passes through CloudDeploy">🔒 {v.secret_ref}</span> : v.value}</td>
                <td>{canEdit && <button className="btn btn-ghost btn-sm" onClick={() => setDeleting(v.key)}>Delete</button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {canEdit && (
        <form className="row" onSubmit={add} aria-label="Add variable">
          <input placeholder="KEY" value={key} onChange={(e) => setKey(e.target.value.toUpperCase())} required pattern="[A-Z_][A-Z0-9_]*" />
          <input className="grow" placeholder={secret ? "secret-name/key" : "value"} value={val} onChange={(e) => setVal(e.target.value)} />
          <label className="check"><input type="checkbox" checked={secret} onChange={(e) => setSecret(e.target.checked)} /> secret reference</label>
          <button className="btn btn-primary btn-sm" disabled={setVar.isPending}>Save</button>
        </form>
      )}
      <InlineError error={setVar.error} />
      <ConfirmDialog open={deleting !== null} title={`Delete ${deleting}?`} confirmLabel="Delete" danger busy={del.isPending}
        onCancel={() => setDeleting(null)} onConfirm={() => deleting && del.mutate(deleting, { onSettled: () => setDeleting(null) })}>
        <p>The variable is removed from {env.name}. Running pods keep it until the next deployment.</p>
      </ConfirmDialog>
    </div>
  );
}
