// Helpers for building search URLs. Each facet value is its own query key
// (?language=Go&language=Rust) so links stay readable and shareable.
export type RawParams = Record<string, string | string[] | undefined>;

export const MULTI_KEYS = ["language", "license", "topic"] as const;
const SINGLE_KEYS = ["q", "sort", "min_stars", "max_stars", "pushed_within", "page", "include_archived"] as const;

export function toSearchParams(raw: RawParams): URLSearchParams {
  const out = new URLSearchParams();
  for (const key of [...SINGLE_KEYS, ...MULTI_KEYS]) {
    const value = raw[key];
    for (const v of Array.isArray(value) ? value : value ? [value] : []) {
      if (v !== "") out.append(key, v);
    }
  }
  return out;
}

/** Returns a copy of params with key=value toggled; any change resets to page 1. */
export function toggle(params: URLSearchParams, key: string, value: string): string {
  const next = new URLSearchParams(params);
  const values = next.getAll(key);
  next.delete(key);
  next.delete("page");
  if (values.includes(value)) {
    values.filter((v) => v !== value).forEach((v) => next.append(key, v));
  } else if ((MULTI_KEYS as readonly string[]).includes(key)) {
    [...values, value].forEach((v) => next.append(key, v));
  } else {
    next.set(key, value);
  }
  return `/?${next.toString()}`;
}

/** Returns a copy of params with the given single-valued keys replaced (undefined removes). */
export function withParams(params: URLSearchParams, changes: Record<string, string | undefined>): string {
  const next = new URLSearchParams(params);
  for (const [k, v] of Object.entries(changes)) {
    if (v === undefined) next.delete(k);
    else next.set(k, v);
  }
  return `/?${next.toString()}`;
}

export function formatCount(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

export function timeAgo(iso: string): string {
  const days = Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
  if (days < 1) return "today";
  if (days < 30) return `${days}d ago`;
  if (days < 365) return `${Math.floor(days / 30)}mo ago`;
  return `${Math.floor(days / 365)}y ago`;
}
