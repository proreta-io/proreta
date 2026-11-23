import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import MonitorPicker, { type PickedMonitor } from "./MonitorPicker";
import { json, mockApi } from "../test/utils";

const names = ["api-internal", "api-public", "billing", "nightly-cron"];

function serve() {
  return mockApi({
    "GET /api/v1/monitors": (r: Request) => {
      const q = new URL(r.url).searchParams.get("q") ?? "";
      const items = names
        .filter((n) => n.includes(q))
        .map((name) => ({
          id: name,
          name,
          projectId: name === "billing" ? "p2" : "p1",
          type: name.includes("cron") ? "heartbeat" : "http",
        }));
      return json({ items, summary: { up: 0, down: 0, paused: 0, unknown: 0 }, nextCursor: null });
    },
  });
}

function Harness({
  onSubmit = () => {},
  exclude = new Set<string>(),
  projectOf,
}: {
  onSubmit?: () => void;
  exclude?: Set<string>;
  projectOf?: (projectId: string) => string;
}) {
  const [picked, setPicked] = useState<PickedMonitor | null>(null);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <MonitorPicker
        label="Monitor"
        exclude={exclude}
        picked={picked}
        onPick={setPicked}
        projectOf={projectOf}
      />
      <output aria-label="picked">{picked?.id ?? "none"}</output>
    </form>
  );
}

function renderPicker(props: Parameters<typeof Harness>[0] = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Harness {...props} />
    </QueryClientProvider>,
  );
}

describe("MonitorPicker", () => {
  it("names each match's project when asked to", async () => {
    serve();
    renderPicker({ projectOf: (id) => (id === "p2" ? "Billing team" : "Web") });
    await userEvent.type(screen.getByRole("combobox", { name: "Monitor" }), "bill");
    expect(await screen.findByRole("option", { name: /billing/ })).toHaveTextContent(
      /Billing team ·\s*HTTPS/,
    );
  });

  it("is a combobox that searches on the server and picks with the keyboard", async () => {
    const fetchMock = serve();
    const onSubmit = vi.fn();
    renderPicker({ onSubmit });
    const box = screen.getByRole("combobox", { name: "Monitor" });
    expect(box).toHaveAttribute("aria-expanded", "false");

    await userEvent.type(box, "api");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
    expect(box).toHaveAttribute("aria-expanded", "true");
    expect(box).toHaveAttribute("aria-controls", screen.getByRole("listbox").id);
    // One search for the settled text, with the limit; no whole-list read.
    const calls = fetchMock.mock.calls
      .map(([r]) => new URL(new Request(r).url))
      .filter((u) => u.pathname === "/api/v1/monitors");
    // The first request is the empty search when the list opens; typing sent one more, not one per letter.
    expect(calls.map((u) => u.searchParams.get("q"))).toEqual([null, "api"]);
    expect(calls[1]!.searchParams.get("limit")).toBe("20");
    expect(await screen.findByText(/2 monitors found/)).toBeInTheDocument();

    await userEvent.keyboard("{ArrowDown}");
    const options = screen.getAllByRole("option");
    expect(options[1]).toHaveAttribute("aria-selected", "true");
    expect(box).toHaveAttribute("aria-activedescendant", options[1]!.id);
    await userEvent.keyboard("{Enter}");
    expect(screen.getByLabelText("picked")).toHaveTextContent("api-public");
    expect(box).toHaveValue("api-public (HTTPS)");
    expect(box).toHaveAttribute("aria-expanded", "false");
    expect(onSubmit).not.toHaveBeenCalled(); // Enter chose, it did not submit the form
  });

  it("clears the choice when the text changes, closes with Escape and leaves out used monitors", async () => {
    serve();
    renderPicker({ exclude: new Set(["billing"]) });
    const box = screen.getByRole("combobox", { name: "Monitor" });
    await userEvent.type(box, "bil");
    expect(await screen.findByText(/No monitors match/, { selector: "p" })).toBeInTheDocument();
    await userEvent.clear(box);
    await userEvent.type(box, "i");
    await userEvent.click(await screen.findByRole("option", { name: /nightly-cron/ }));
    expect(screen.getByLabelText("picked")).toHaveTextContent("nightly-cron");
    await userEvent.type(box, "x");
    expect(screen.getByLabelText("picked")).toHaveTextContent("none");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(box).toHaveAttribute("aria-expanded", "false"));
  });
});
