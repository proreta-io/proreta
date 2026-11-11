import { validatePasswordChange } from "./passwordForm";

const ok = {
  current: "old passphrase 1",
  next: "a new long passphrase",
  confirm: "a new long passphrase",
};

describe("password change policy (mirrors the server)", () => {
  it("accepts a valid change", () => {
    expect(validatePasswordChange(ok, "admin")).toEqual({});
  });

  it("asks for the current password", () => {
    expect(validatePasswordChange({ ...ok, current: "" }, "admin").current).toBe(
      "Enter your current password",
    );
  });

  it.each([
    ["short", "Use at least 12 characters"],
    ["x".repeat(129), "Use at most 128 characters"],
    ["ADMIN@example.com", "Do not use your login as the password"],
    ["old passphrase 1", "Choose a password different from the current one"],
  ])("rejects the new password %j", (next, message) => {
    const login = "admin@example.com";
    expect(validatePasswordChange({ ...ok, next, confirm: next }, login).next).toBe(message);
  });

  it("counts code points like the server", () => {
    expect(
      validatePasswordChange({ ...ok, next: "😀".repeat(12), confirm: "😀".repeat(12) }, "a"),
    ).toEqual({});
  });

  it("checks the confirmation", () => {
    expect(validatePasswordChange({ ...ok, confirm: "different" }, "admin").confirm).toBe(
      "The passwords do not match",
    );
  });
});
