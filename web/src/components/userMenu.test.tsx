import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { json, mockApi, renderApp, session } from "../test/utils";
import { initials } from "./UserMenu";

describe("initials", () => {
  it("takes two letters from the login", () => {
    expect(initials("jonathan.whitfield@acme.example.com")).toBe("JW");
    expect(initials("alice@example.com")).toBe("AL");
    expect(initials("admin")).toBe("AD");
    expect(initials("")).toBe("?");
  });
});

describe("account menu", () => {
  it("opens with the login and closes on Escape and on a click outside", async () => {
    mockApi({
      "GET /api/v1/auth/me": () =>
        json({
          ...session,
          user: { ...session.user, login: "jonathan.whitfield@acme.example.com" },
        }),
    });
    renderApp("/settings");
    const banner = await screen.findByRole("banner");
    const account = await within(banner).findByRole("button", { name: /, account$/ });
    expect(account).toHaveTextContent("JW");
    expect(screen.queryByRole("button", { name: "Log out" })).toBeNull();

    await userEvent.click(account);
    expect(account).toHaveAttribute("aria-expanded", "true");
    expect(within(banner).getByText("jonathan.whitfield@acme.example.com")).toBeInTheDocument();
    expect(within(banner).getByRole("button", { name: "Log out" })).toBeInTheDocument();

    // Escape from inside the panel hands the focus back to the button.
    within(banner).getByRole("link", { name: "Profile" }).focus();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("button", { name: "Log out" })).toBeNull();
    expect(account).toHaveFocus();

    await userEvent.click(account);
    await userEvent.click(screen.getByRole("main"));
    expect(screen.queryByRole("button", { name: "Log out" })).toBeNull();
  });
});
