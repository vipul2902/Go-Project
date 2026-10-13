import type { ReactNode } from "react";
import { errorMessage } from "../api/client";

export function Loading({ label = "Loading…" }: { label?: string }) {
  return <div className="state" role="status"><span className="spinner" aria-hidden /> {label}</div>;
}

export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div className="state state-error" role="alert">
      <strong>Couldn’t load this.</strong> {errorMessage(error)}
      {retry && <button className="btn btn-ghost" onClick={retry}>Try again</button>}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return <div className="state state-empty"><strong>{title}</strong>{children && <div>{children}</div>}</div>;
}

export function InlineError({ error }: { error: unknown }) {
  if (!error) return null;
  return <p className="inline-error" role="alert">{errorMessage(error)}</p>;
}
