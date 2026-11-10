/**
 * A limit without a quota: the largest value a limit may hold (plans.Unlimited in the backend,
 * the `community` plan). Never shown as a number.
 */
export const UNLIMITED = 2147483647;

export function isUnlimited(limit: number): boolean {
  return limit >= UNLIMITED;
}
