/** The api builds the link from PRORETA_PUBLIC_URL; without one it is a path on this origin. */
export function absoluteLink(url: string) {
  return url.startsWith("/") ? `${globalThis.location.origin}${url}` : url;
}
