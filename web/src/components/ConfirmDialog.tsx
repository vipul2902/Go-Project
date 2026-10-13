import { useEffect, useRef, type ReactNode } from "react";

/**
 * Confirmation for destructive or production-affecting actions. Uses the
 * native <dialog> element: focus trapping, Escape to close and inert
 * background come from the browser.
 */
export function ConfirmDialog(props: {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  // Open with showModal() so the dialog is truly modal (focus trap, inert
  // page, backdrop). Rendering the `open` attribute instead would make it
  // NON-modal, which is exactly the bug the first screenshot showed.
  useEffect(() => {
    const d = ref.current;
    if (!props.open || !d || d.open) return;
    if (typeof d.showModal === "function") d.showModal();
    else d.setAttribute("open", ""); // test environments without <dialog> support
  }, [props.open]);

  if (!props.open) return null;
  return (
    <dialog ref={ref} className="dialog" aria-labelledby="dialog-title" onCancel={(e) => { e.preventDefault(); props.onCancel(); }}>
      <h2 id="dialog-title">{props.title}</h2>
      <div className="dialog-body">{props.children}</div>
      <div className="dialog-actions">
        <button className="btn btn-ghost" onClick={props.onCancel} disabled={props.busy}>Cancel</button>
        <button className={`btn ${props.danger ? "btn-danger" : "btn-primary"}`} onClick={props.onConfirm} disabled={props.busy}>
          {props.busy ? "Working…" : props.confirmLabel}
        </button>
      </div>
    </dialog>
  );
}
