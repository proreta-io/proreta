import { ApiError } from "../api/client";
import type { Project, Specula } from "../api/endpoints";
import {
  executorNotes,
  emptySpeculaForm,
  mapSpeculaErrors,
  runSettings,
  sharedWith,
  toSpeculaRequest,
  validateSpeculaForm,
} from "./specula";

const specula = (over: Partial<Specula> = {}): Specula => ({
  id: "s1",
  name: "Prod",
  allProjects: true,
  projectIds: [],
  status: "online",
  version: "0.1.0",
  hostname: "pod-1",
  lastSeenAt: "2026-11-12T10:00:00Z",
  createdAt: "2026-11-01T00:00:00Z",
  revokedAt: null,
  capabilities: ["schedule/1"],
  scheduleIssuedAt: null,
  bufferDepth: null,
  oldestUnsentAt: null,
  bufferDegraded: false,
  ...over,
});

describe("validateSpeculaForm", () => {
  it("accepts a name shared with all projects", () => {
    expect(validateSpeculaForm({ ...emptySpeculaForm, name: "Prod" })).toEqual({});
  });

  it("checks the name after trimming", () => {
    expect(validateSpeculaForm({ ...emptySpeculaForm, name: "  " }).name).toBe("Enter a name.");
    expect(validateSpeculaForm({ ...emptySpeculaForm, name: "a".repeat(81) }).name).toBe(
      "Use 80 characters or fewer.",
    );
    expect(validateSpeculaForm({ ...emptySpeculaForm, name: "a".repeat(80) }).name).toBeUndefined();
  });

  it("needs a project when not shared with all", () => {
    const v = { ...emptySpeculaForm, name: "Prod", allProjects: false };
    expect(validateSpeculaForm(v).projectIds).toMatch(/at least one project/);
    expect(validateSpeculaForm({ ...v, projectIds: ["p1"] })).toEqual({});
  });
});

describe("toSpeculaRequest", () => {
  it("sends the project list only when not shared with all", () => {
    expect(toSpeculaRequest({ name: " Prod ", allProjects: true, projectIds: ["p1"] })).toEqual({
      name: "Prod",
      allProjects: true,
    });
    expect(toSpeculaRequest({ name: "Prod", allProjects: false, projectIds: ["p1"] })).toEqual({
      name: "Prod",
      allProjects: false,
      projectIds: ["p1"],
    });
  });
});

describe("mapSpeculaErrors", () => {
  it("puts known field errors next to fields and the rest in one message", () => {
    const err = new ApiError({
      type: "x",
      title: "Validation failed",
      status: 422,
      fieldErrors: [
        { field: "name", message: "is already used by another Specula" },
        { field: "other", message: "odd" },
      ],
    });
    expect(mapSpeculaErrors(err)).toEqual({
      fields: { name: "Is already used by another Specula" },
      general: "other: odd",
    });
  });
});

describe("sharedWith", () => {
  const projects = [
    { id: "p1", name: "Payments" },
    { id: "p2", name: "Search" },
  ] as Project[];

  it("names the projects, counts unknown ones, and says all", () => {
    expect(sharedWith(specula(), projects)).toBe("All projects");
    expect(sharedWith(specula({ allProjects: false, projectIds: ["p1", "p2"] }), projects)).toBe(
      "Payments, Search",
    );
    expect(sharedWith(specula({ allProjects: false, projectIds: ["p1", "px"] }), projects)).toBe(
      "2 projects",
    );
    expect(sharedWith(specula({ allProjects: false, projectIds: [] }), projects)).toBe(
      "Projects you see",
    );
  });
});

describe("runSettings", () => {
  it("prints the two settings an executor needs", () => {
    expect(runSettings("https://proreta.example.com", "spc_x")).toBe(
      "PRORETA_URL=https://proreta.example.com\nPRORETA_SPECULA_TOKEN=spc_x",
    );
  });
});

describe("executorNotes", () => {
  const now = Date.parse("2026-11-12T12:00:00Z");
  const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString();

  it("says nothing before the first call or after revoking", () => {
    expect(executorNotes(specula({ status: "pending", capabilities: [] }), now)).toEqual([]);
    expect(executorNotes(specula({ status: "revoked" }), now)).toEqual([]);
  });

  it("warns that an executor without schedule mode stops checking", () => {
    const notes = executorNotes(specula({ capabilities: ["tcp"] }), now);
    expect(notes).toEqual(["Pull mode: it stops checking while Proreta cannot be reached."]);
  });

  it("shows the schedule age and an empty buffer as the schedule alone", () => {
    const s = specula({ scheduleIssuedAt: ago(600), bufferDepth: 0 });
    expect(executorNotes(s, now)).toEqual(["Schedule issued 10 min ago"]);
  });

  it("calls a recent small buffer waiting and an old one on a working link a replay", () => {
    const waiting = specula({ bufferDepth: 1, oldestUnsentAt: ago(20) });
    expect(executorNotes(waiting, now)).toEqual([
      "1 result waiting to be sent, oldest checked 20 s ago",
    ]);
    const replay = specula({ bufferDepth: 340, oldestUnsentAt: ago(7200) });
    expect(executorNotes(replay, now)).toEqual(["Replaying 340 results, oldest checked 2 h ago"]);
  });

  it("never calls the buffer of an unreachable Specula a replay", () => {
    const s = specula({ status: "offline", bufferDepth: 12, oldestUnsentAt: ago(3600) });
    expect(executorNotes(s, now)).toEqual([
      "12 results waiting to be sent, oldest checked 1 h ago",
    ]);
  });
});
