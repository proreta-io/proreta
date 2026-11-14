import { ApiError } from "../api/client";
import type { StatusPageDomain } from "../api/endpoints";

/** At most this many domains per page (the server's cap, besides the plan limit). */
export const DOMAINS_PER_PAGE = 3;

export const CUSTOM_DOMAINS_DISABLED_TYPE = "urn:proreta:problem:custom-domains-disabled";
export const DOMAIN_UNAVAILABLE_TYPE = "urn:proreta:problem:domain-unavailable";

type State = StatusPageDomain["state"];

/** The installation has custom domains turned off: the whole section stays out of the page. */
export function isDomainsDisabled(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404 && err.type === CUSTOM_DOMAINS_DISABLED_TYPE;
}

export const STATE_TEXT: Record<State, string> = {
  pending: "Pending",
  verified: "Verified",
  active: "Active",
  failed: "Failed",
};

/** True while the host answers with the page (verified hosts are served, waiting for the CNAME). */
export const isServing = (d: StatusPageDomain) =>
  (d.state === "verified" || d.state === "active") && !d.disabled;

export const ERROR_TEXT: Record<NonNullable<StatusPageDomain["lastError"]>, string> = {
  "txt-missing": "The TXT record was not found.",
  "txt-mismatch": "A TXT record exists, but its value is not the one shown.",
  "cname-missing": "The CNAME record was not found.",
  "cname-wrong": "The CNAME record points somewhere else.",
  "dns-error": "The DNS lookup itself failed. This is not counted against the domain.",
};

/** What the owner does or can expect next, in words, per state. */
export function nextStep(d: StatusPageDomain, expires?: string): string {
  if (d.disabled) return "Serving was stopped by the platform administrator. Contact support.";
  switch (d.state) {
    case "pending":
      return `Publish the TXT record, then check. Checks also run in the background, every few minutes at first.${
        expires ? ` The domain is removed if the record is not found by ${expires}.` : ""
      }`;
    case "verified":
      return "The TXT record was found and the page is served on this host. Add the CNAME record so visitors reach it.";
    case "active":
      return "The page is served on this domain. It is checked every 6 hours; if both records are missing for 7 days, serving stops.";
    case "failed":
      return "Not served: neither record was found for 7 days. Publish the records again; serving resumes as soon as one is found.";
  }
}

/** A calm sentence for the live region after "Check now". */
export function checkResult(d: StatusPageDomain): string {
  switch (d.state) {
    case "active":
      return `${d.hostUnicode} is active.`;
    case "verified":
      return `${d.hostUnicode}: the TXT record was found. Waiting for the CNAME record.`;
    case "failed":
      return `${d.hostUnicode} is not served. ${d.lastError ? ERROR_TEXT[d.lastError] : ""}`.trim();
    case "pending":
      return `${d.hostUnicode}: not verified yet. ${
        d.lastError ? ERROR_TEXT[d.lastError] + " " : ""
      }DNS changes can take a while to spread; checks continue in the background.`;
  }
}

/** "30 seconds", "2 minutes": how long to wait after a 429. */
export function waitText(seconds: number | undefined): string {
  if (!seconds || seconds < 1) return "a minute";
  if (seconds < 90) return `${seconds} second${seconds === 1 ? "" : "s"}`;
  const m = Math.ceil(seconds / 60);
  return `${m} minutes`;
}

/** The message for a failed check: a 429 is a wait, not an error. */
export function checkError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 429)
      return `Too many checks in a short time. Try again in ${waitText(err.retryAfter)}.`;
    if (err.type === DOMAIN_UNAVAILABLE_TYPE) return "This host is not available for this page.";
    return err.detail ?? err.title;
  }
  return "The check failed. Try again.";
}
