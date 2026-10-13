import type { Deployment } from "../api/types";
import { relativeTime } from "./format";

const steps = ["QUEUED", "BUILDING", "DEPLOYING", "RUNNING"] as const;

/** Where a deployment is in its lifecycle, and where it stopped if it did. */
export function Timeline({ d }: { d: Deployment }) {
  const terminalFailure = d.status === "FAILED" || d.status === "CANCELLED";
  // How far it got: FAILED/CANCELLED deployments are drawn up to the step
  // they had reached (started_at means it reached BUILDING).
  const reached = d.status === "RUNNING" ? 3 : terminalFailure ? (d.started_at ? 1 : 0) : steps.indexOf(d.status as (typeof steps)[number]);
  const time = (i: number) => (i === 0 ? d.created_at : i === 1 ? d.started_at : i === 3 ? d.completed_at : null);

  return (
    <ol className="timeline" aria-label="Deployment progress">
      {steps.map((s, i) => {
        const state = i < reached || (i === reached && d.status === "RUNNING") ? "done" : i === reached && !terminalFailure ? "current" : "todo";
        return (
          <li key={s} className={`step step-${state}`}>
            <span className="dot" aria-hidden />
            <span className="step-name">{s.toLowerCase()}</span>
            <span className="step-time">{time(i) ? relativeTime(time(i)) : ""}</span>
          </li>
        );
      })}
      {terminalFailure && (
        <li className="step step-failed">
          <span className="dot" aria-hidden />
          <span className="step-name">{d.status.toLowerCase()}</span>
          <span className="step-time">{relativeTime(d.completed_at)}</span>
        </li>
      )}
    </ol>
  );
}
