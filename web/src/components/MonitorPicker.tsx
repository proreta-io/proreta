import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useId, useState, type KeyboardEvent } from "react";
import { errorMessage } from "../api/client";
import { listMonitorsPage, type MonitorListItem } from "../api/endpoints";
import { useDebounced } from "../lib/useDebounced";
import { cx, Field, Input } from "./ui";

/** How many matches the list offers; typing narrows the rest on the server. */
const SHOWN = 20;

export type PickedMonitor = Pick<MonitorListItem, "id" | "name" | "type" | "projectId">;

export const monitorTypeLabel = (type: string) =>
  type === "heartbeat" ? "heartbeat" : type === "tcp" ? "TCP" : "HTTPS";

/**
 * Search-as-you-type choice of one monitor: the server searches (name, URL or host:port) and
 * sends one short page, so the browser never loads the whole list. An ARIA combobox: the
 * input keeps focus, Arrow keys move through the list, Enter picks, Escape closes it; the number
 * of matches is announced.
 */
export default function MonitorPicker({
  label,
  projectId,
  exclude,
  picked,
  onPick,
  projectOf,
  className,
}: {
  label: string;
  projectId?: string;
  /** Monitors that are not offered (already added). */
  exclude: ReadonlySet<string>;
  picked: PickedMonitor | null;
  onPick: (monitor: PickedMonitor | null) => void;
  /** The project's name of each match, when the monitors come from several projects. */
  projectOf?: (projectId: string) => string;
  className?: string;
}) {
  const listId = useId();
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const search = useDebounced(text.trim(), 250);
  // Room for the monitors that are hidden because they are already used.
  const limit = Math.min(200, SHOWN + exclude.size);
  const matches = useQuery({
    queryKey: ["monitors", "picker", projectId ?? "all", search, limit],
    queryFn: () => listMonitorsPage(projectId, undefined, { q: search }, limit),
    enabled: open,
    placeholderData: keepPreviousData,
  });
  const options = (matches.data?.items ?? []).filter((m) => !exclude.has(m.id)).slice(0, SHOWN);
  const more =
    matches.data?.nextCursor != null || options.length < (matches.data?.items.length ?? 0);
  const current = Math.min(active, Math.max(0, options.length - 1));
  const optionId = (i: number) => `${listId}-${i}`;

  function choose(m: PickedMonitor) {
    onPick({ id: m.id, name: m.name, type: m.type, projectId: m.projectId });
    setText(`${m.name} (${monitorTypeLabel(m.type)})`);
    setOpen(false);
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) setOpen(true);
      else if (options.length > 0) {
        const step = e.key === "ArrowDown" ? 1 : -1;
        setActive((current + step + options.length) % options.length);
      }
    } else if (e.key === "Enter" && open) {
      // Picking, never submitting the form around the picker.
      e.preventDefault();
      const m = options[current];
      if (m) choose(m);
    } else if (e.key === "Escape" && open) {
      e.stopPropagation();
      setOpen(false);
    }
  }

  const status = !open
    ? ""
    : matches.isError
      ? `Cannot search monitors. ${errorMessage(matches.error)}`
      : matches.data === undefined || matches.isPlaceholderData || text.trim() !== search
        ? "Searching…"
        : options.length === 0
          ? "No monitors match."
          : `${options.length}${more ? " or more" : ""} ${options.length === 1 && !more ? "monitor" : "monitors"} found. Use the up and down arrow keys, Enter to choose.`;

  return (
    <Field label={label} className={className}>
      {(c) => (
        <div className="relative" onBlur={() => setOpen(false)}>
          <Input
            {...c}
            role="combobox"
            aria-expanded={open}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={open && options.length > 0 ? optionId(current) : undefined}
            autoComplete="off"
            maxLength={100}
            placeholder="Search by name or URL"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setActive(0);
              setOpen(true);
              if (picked) onPick(null);
            }}
            onFocus={() => setOpen(true)}
            onKeyDown={onKeyDown}
          />
          <ul
            id={listId}
            role="listbox"
            aria-label={`${label}, matches`}
            hidden={!open}
            className="absolute z-10 mt-1 max-h-72 w-full overflow-auto border border-input bg-field shadow-md"
          >
            {options.map((m, i) => (
              // eslint-disable-next-line jsx-a11y/click-events-have-key-events -- the input owns the keyboard (aria-activedescendant); the options are for the mouse.
              <li
                key={m.id}
                id={optionId(i)}
                role="option"
                aria-selected={i === current}
                // Keeps focus in the input: a click must not blur and close the list first.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => choose(m)}
                className={cx(
                  "flex cursor-pointer items-baseline justify-between gap-3 px-2.5 py-2 text-body",
                  i === current && "bg-muted",
                )}
              >
                <span className="min-w-0 truncate">{m.name}</span>
                <span className="flex max-w-1/2 shrink-0 gap-1 text-caption text-muted-foreground">
                  {projectOf && (
                    <span className="min-w-0 truncate" title={projectOf(m.projectId)}>
                      {projectOf(m.projectId)} ·
                    </span>
                  )}
                  <span className="flex-none font-mono">{monitorTypeLabel(m.type)}</span>
                </span>
              </li>
            ))}
            {open && options.length === 0 && matches.data !== undefined && (
              <li role="presentation" className="px-2.5 py-2 text-caption text-muted-foreground">
                No monitors match.
              </li>
            )}
            {open && more && options.length > 0 && (
              <li role="presentation" className="px-2.5 py-2 text-caption text-muted-foreground">
                More monitors match: type to narrow the list.
              </li>
            )}
          </ul>
          <p role="status" className="sr-only">
            {status}
          </p>
        </div>
      )}
    </Field>
  );
}
