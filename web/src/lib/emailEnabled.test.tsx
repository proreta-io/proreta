import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { json, mockApi, problem } from "../test/utils";
import { useEmailEnabled } from "./oauth";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const providers = (emailEnabled: boolean) => ({
  "GET /api/v1/auth/providers": () =>
    json({ providers: [], passwordLoginEnabled: true, registrationEnabled: true, emailEnabled }),
});

describe("useEmailEnabled", () => {
  it("is false until the server says otherwise", async () => {
    mockApi(providers(true));
    const { result } = renderHook(() => useEmailEnabled(), { wrapper });
    expect(result.current).toBe(false);
    await waitFor(() => expect(result.current).toBe(true));
  });

  it("is false when the server does not send email", async () => {
    const mock = mockApi(providers(false));
    const { result } = renderHook(() => useEmailEnabled(), { wrapper });
    await waitFor(() => expect(mock).toHaveBeenCalled());
    expect(result.current).toBe(false);
  });

  it("is false when the answer cannot be loaded", async () => {
    const mock = mockApi({ "GET /api/v1/auth/providers": () => problem(500, "Boom") });
    const { result } = renderHook(() => useEmailEnabled(), { wrapper });
    await waitFor(() => expect(mock).toHaveBeenCalled());
    expect(result.current).toBe(false);
  });
});
