import { ApiError } from "../api/client";
import type { Project, Specula } from "../api/endpoints";
import {
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
