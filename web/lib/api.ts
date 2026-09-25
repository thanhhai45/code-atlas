// Server-side client for the Go API. API_URL is read at request time, so the
// same image works locally (http://localhost:8080) and in compose (http://api:8080).
export const API_URL = process.env.API_URL ?? "http://localhost:8080";

export type Hit = {
  id: number;
  score?: number;
  full_name: string;
  name: string;
  owner: string;
  description: string;
  url: string;
  language?: string;
  license?: string;
  topics: string[];
  stars: number;
  forks: number;
  archived: boolean;
  pushed_at: string;
  highlight?: Record<string, string[]>;
};

export type Bucket = { key: string; count: number };

export type SearchResult = {
  total: number;
  page: number;
  size: number;
  took_ms: number;
  hits: Hit[];
  facets: Record<string, Bucket[]>;
  /** search_after cursor for the next page; absent on the last page. */
  next_cursor?: string;
};

export type Repository = Omit<Hit, "score" | "highlight"> & {
  homepage?: string;
  license_name?: string;
  watchers: number;
  open_issues: number;
  default_branch?: string;
  fork: boolean;
  readme?: string;
  created_at: string;
  updated_at: string;
  categories?: string[];
  technologies?: string[];
  use_cases?: string[];
};

async function get<T>(path: string): Promise<T | null> {
  const res = await fetch(`${API_URL}${path}`, { cache: "no-store" });
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(`API ${path} failed with ${res.status}`);
  return res.json() as Promise<T>;
}

export function search(params: URLSearchParams) {
  return get<SearchResult>(`/search?${params.toString()}`);
}

export function getRepository(id: string) {
  return get<Repository>(`/repositories/${encodeURIComponent(id)}`);
}

export async function getSimilar(id: string) {
  const res = await get<{ items: Hit[] }>(`/repositories/${encodeURIComponent(id)}/similar?size=6`);
  return res?.items ?? [];
}
