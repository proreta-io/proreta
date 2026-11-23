import type { ReactNode } from "react";
import { cx, DotShape, HollowDotShape, PauseShape, TriangleShape } from "./shapes";

export type StatusKind = "up" | "down" | "paused" | "pending";

const DEFAULT_LABEL: Record<StatusKind, string> = {
  up: "Up",
  down: "Down",
  paused: "Paused",
  pending: "Pending",
};

/**
 * Up is a quiet green dot; a filled red pill is reserved for down, the only filled status colour.
 * `inline` is the unfilled down, for sentences and summaries.
 */
export function StatusIndicator({
  status,
  label,
  inline = false,
}: {
  status: StatusKind;
  /** The word with its duration, such as "Down 12 min". */
  label?: ReactNode;
  inline?: boolean;
}) {
  const text = label ?? DEFAULT_LABEL[status];
  if (status === "down") {
    return inline ? (
      <span className="inline-flex items-center gap-2 font-semibold text-down">
        <TriangleShape />
        {text}
      </span>
    ) : (
      <span className="inline-flex items-center gap-1.5 rounded-full bg-destructive px-2 py-px font-semibold whitespace-nowrap text-destructive-foreground">
        <TriangleShape />
        {text}
      </span>
    );
  }
  return (
    <span
      className={cx(
        "inline-flex items-center gap-2 whitespace-nowrap",
        status === "up" ? "text-foreground" : "text-muted-foreground",
      )}
    >
      {status === "up" && <DotShape className="text-success" />}
      {status === "paused" && <PauseShape />}
      {status === "pending" && <HollowDotShape />}
      {text}
    </span>
  );
}

export type BadgeTone =
  "neutral" | "strong" | "plan" | "inverse" | "warning" | "success" | "danger" | "deleted";

const BADGE_TONES: Record<BadgeTone, string> = {
  neutral: "border-border bg-muted text-muted-foreground",
  strong: "border-border bg-muted text-foreground",
  plan: "border-transparent bg-accent-tint text-accent-text",
  inverse: "border-inverse bg-inverse text-inverse-foreground",
  warning: "border-warning-border bg-warning-tint text-warning",
  success: "border-success-border bg-success-tint text-success",
  danger: "border-down-border bg-down-tint text-down",
  deleted: "border-dashed border-input text-subtle-foreground",
};

/** A small rounded pill. A toned badge also needs a shape or a word that carries the meaning. */
export function Badge({
  tone = "neutral",
  icon,
  children,
}: {
  tone?: BadgeTone;
  icon?: ReactNode;
  children: ReactNode;
}) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-px text-caption leading-[1.35] font-medium whitespace-nowrap",
        BADGE_TONES[tone],
      )}
    >
      {icon}
      {children}
    </span>
  );
}
