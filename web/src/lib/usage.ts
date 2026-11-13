import { useQuery } from "@tanstack/react-query";
import { getUsage, POLL_MS, queryKeys, type Usage } from "../api/endpoints";
import { isUnlimited } from "./limits";
import { usePermissions } from "./permissions";

/**
 * Plan, limits and usage of the organization, polled like the monitor list. Owners and maintainers only:
 * for members and viewers it never loads, so limit hints stay off and a reached limit
 * shows up as the create's own "plan limit reached" message.
 */
export function useUsage() {
  const can = usePermissions();
  return useQuery({
    queryKey: queryKeys.usage,
    queryFn: getUsage,
    refetchInterval: POLL_MS,
    enabled: can.org("usage.read"),
  });
}

/** True when the organization cannot create another monitor (unknown usage is not a limit). */
export function atMonitorLimit(usage: Usage | undefined): boolean {
  return usage !== undefined && usage.usage.monitors >= usage.limits.monitors;
}

/** True when the organization cannot create another heartbeat (watchdog). */
export function atHeartbeatLimit(usage: Usage | undefined): boolean {
  return usage !== undefined && usage.usage.heartbeats >= usage.limits.heartbeats;
}

/** "3 monitors", "1 monitor", "unlimited monitors": a count or limit with its noun. */
export function countOf(n: number, one: string, many: string): string {
  if (isUnlimited(n)) return `unlimited ${many}`;
  return `${n} ${n === 1 ? one : many}`;
}
