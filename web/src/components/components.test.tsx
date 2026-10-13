import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Timeline } from "./Timeline";
import { ConfirmDialog } from "./ConfirmDialog";
import type { Deployment } from "../api/types";

const base = { created_at: new Date().toISOString(), started_at: null, completed_at: null } as unknown as Deployment;

describe("Timeline", () => {
  it("marks every step done for a running deployment", () => {
    const { container } = render(<Timeline d={{ ...base, status: "RUNNING", started_at: base.created_at, completed_at: base.created_at }} />);
    expect(container.querySelectorAll(".step-done")).toHaveLength(4);
    expect(container.querySelector(".step-failed")).toBeNull();
  });
  it("shows where a failed deployment stopped", () => {
    const { container } = render(<Timeline d={{ ...base, status: "FAILED", started_at: base.created_at, completed_at: base.created_at }} />);
    expect(container.querySelectorAll(".step-done")).toHaveLength(1); // queued; it had reached building
    expect(screen.getByText("failed")).toBeInTheDocument();
  });
  it("highlights the current step of an active deployment", () => {
    const { container } = render(<Timeline d={{ ...base, status: "DEPLOYING", started_at: base.created_at }} />);
    expect(container.querySelector(".step-current .step-name")).toHaveTextContent("deploying");
  });
});

describe("ConfirmDialog", () => {
  it("does nothing until confirmed; cancel does not confirm", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const { rerender } = render(<ConfirmDialog open={false} title="Roll back?" confirmLabel="Roll back" onConfirm={onConfirm} onCancel={onCancel}>x</ConfirmDialog>);
    expect(screen.queryByText("Roll back?")).toBeNull();
    rerender(<ConfirmDialog open title="Roll back?" confirmLabel="Roll back" onConfirm={onConfirm} onCancel={onCancel}>x</ConfirmDialog>);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onConfirm).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Roll back" }));
    expect(onConfirm).toHaveBeenCalledOnce();
  });
  it("opens as a modal dialog (showModal), not via the open attribute", () => {
    const showModal = vi.fn(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
    HTMLDialogElement.prototype.showModal = showModal;
    try {
      render(<ConfirmDialog open title="t" confirmLabel="Go" onConfirm={() => {}} onCancel={() => {}}>x</ConfirmDialog>);
      expect(showModal).toHaveBeenCalledOnce();
    } finally {
      delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).showModal;
    }
  });
  it("disables both buttons while busy", () => {
    render(<ConfirmDialog open busy title="t" confirmLabel="Go" onConfirm={() => {}} onCancel={() => {}}>x</ConfirmDialog>);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Working…" })).toBeDisabled();
  });
});
