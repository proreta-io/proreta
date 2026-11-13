import type { ResourceCounts } from "../api/endpoints";
import { isUnlimited } from "./limits";

/** Every limit of a plan, in the order the form and the list show them. */
export const LIMITS: { field: keyof ResourceCounts; label: string; hint: string }[] = [
  { field: "monitors", label: "Monitors", hint: "HTTPS and TCP monitors." },
  {
    field: "heartbeats",
    label: "Heartbeats",
    hint: "Heartbeat (watchdog) monitors, counted apart from monitors.",
  },
  {
    field: "channels",
    label: "Notification channels",
    hint: "Slack and webhook channels; deleted ones do not count.",
  },
  { field: "members", label: "Members", hint: "People in the organization plus open invitations." },
  { field: "projects", label: "Projects", hint: "Live projects; the Default project counts." },
  {
    field: "statusPages",
    label: "Status pages",
    hint: "Public status pages that are not deleted.",
  },
  {
    field: "specula",
    label: "Specula",
    hint: "Executors in a customer network; revoked ones do not count.",
  },
];

/** "3", or "Unlimited" for the value that means no quota. */
export const limitText = (n: number) => (isUnlimited(n) ? "Unlimited" : String(n));
