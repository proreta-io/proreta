import { ApiError, errorMessage } from "../api/client";
import type { Project, Specula, SpeculaCreate, SpeculaStatus } from "../api/endpoints";

export const SPECULA_NAME_MAX = 80;

export interface SpeculaFormValues {
  name: string;
  allProjects: boolean;
  projectIds: string[];
}

export const emptySpeculaForm: SpeculaFormValues = { name: "", allProjects: true, projectIds: [] };

export const valuesFromSpecula = (s: Specula): SpeculaFormValues => ({
  name: s.name,
  allProjects: s.allProjects,
  projectIds: s.projectIds,
});

export type SpeculaFormField = "name" | "projectIds";
export type SpeculaFormErrors = Partial<Record<SpeculaFormField, string>>;

export function validateSpeculaForm(v: SpeculaFormValues): SpeculaFormErrors {
  const errors: SpeculaFormErrors = {};
  const name = v.name.trim();
  if (!name) errors.name = "Enter a name.";
  else if ([...name].length > SPECULA_NAME_MAX) {
    errors.name = `Use ${SPECULA_NAME_MAX} characters or fewer.`;
  }
  if (!v.allProjects && v.projectIds.length === 0) {
    errors.projectIds = "Choose at least one project, or share it with all projects.";
  }
  return errors;
}

/** The request body; the project list is only meaningful while not shared with everyone. */
export function toSpeculaRequest(v: SpeculaFormValues): SpeculaCreate {
  return {
    name: v.name.trim(),
    allProjects: v.allProjects,
    ...(v.allProjects ? {} : { projectIds: v.projectIds }),
  };
}

/** Field errors of a failed save next to the form's fields; anything else is one message. */
export function mapSpeculaErrors(err: unknown): { fields: SpeculaFormErrors; general?: string } {
  if (!(err instanceof ApiError)) return { fields: {}, general: errorMessage(err) };
  const fields: SpeculaFormErrors = {};
  const other: string[] = [];
  for (const fe of err.fieldErrors) {
    const message = fe.message ? fe.message[0]!.toUpperCase() + fe.message.slice(1) : fe.message;
    if (fe.field === "name" || fe.field === "projectIds") fields[fe.field] ??= message;
    else other.push(`${fe.field}: ${fe.message}`);
  }
  if (other.length > 0) return { fields, general: other.join(" ") };
  if (Object.keys(fields).length > 0) return { fields };
  return { fields, general: errorMessage(err) };
}

export const STATUS_LABELS: Record<SpeculaStatus, string> = {
  pending: "Waiting",
  online: "Online",
  offline: "Offline",
  revoked: "Revoked",
};

export const STATUS_HINTS: Record<SpeculaStatus, string> = {
  pending: "Registered but it has not connected yet.",
  online: "Connected in the last 90 seconds.",
  offline: "Connected before, but silent for more than 90 seconds.",
  revoked: "Its token no longer works.",
};

/** Who may use a Specula, in words; project names fall back to a count when unknown. */
export function sharedWith(s: Specula, projects: Project[] | undefined): string {
  if (s.allProjects) return "All projects";
  if (s.projectIds.length === 0) return "Projects you see";
  const names = s.projectIds.map((id) => projects?.find((p) => p.id === id)?.name).filter(Boolean);
  if (names.length === s.projectIds.length) return names.join(", ");
  return `${s.projectIds.length} ${s.projectIds.length === 1 ? "project" : "projects"}`;
}

/** The command-line settings an executor needs, for the one-time panel. */
export function runSettings(url: string, token: string): string {
  return `PRORETA_URL=${url}\nPRORETA_SPECULA_TOKEN=${token}`;
}
