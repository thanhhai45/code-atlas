import Link from "next/link";
import { Suspense } from "react";
import Facets from "@/components/Facets";
import RepoCard from "@/components/RepoCard";
import SearchBox from "@/components/SearchBox";
import Pagination from "@/components/Pagination";
import { search } from "@/lib/api";
import { toSearchParams, withParams } from "@/lib/params";

const MODES = [
  { key: "lexical", label: "Keyword", title: "BM25 full-text search (default)" },
  { key: "hybrid", label: "Hybrid", title: "Keyword + semantic (embedding) similarity" },
  { key: "semantic", label: "Semantic", title: "Embedding similarity only" },
];

const SORTS = [
  { key: "relevance", label: "Best match" },
  { key: "stars", label: "Most stars" },
  { key: "updated", label: "Recently updated" },
];

export default async function Home(props: PageProps<"/">) {
  const params = toSearchParams(await props.searchParams);
  let result;
  try {
    result = await search(params);
  } catch (err) {
    return (
      <>
        <Suspense>
          <SearchBox />
        </Suspense>
        <p className="mt-8 rounded-lg border border-warn/40 bg-warn/10 p-4 text-sm">
          The search API is unavailable: {(err as Error).message}
        </p>
      </>
    );
  }
  if (!result) return null;

  const sort = params.get("sort") ?? "relevance";
  const mode = params.get("mode") ?? "lexical";
  const modeNote =
    params.get("q") && mode !== result.mode
      ? sort !== "relevance"
        ? "Semantic matching only applies to “Best match” ordering."
        : "Semantic search is unavailable right now; showing keyword results."
      : null;
  const activeFilters = ["language", "license", "topic", "min_stars", "max_stars", "pushed_within"].some((k) => params.has(k));

  return (
    <>
      <Suspense>
        <SearchBox />
      </Suspense>

      <div className="mt-6 grid grid-cols-1 gap-8 md:grid-cols-[220px_1fr]">
        {/* On small screens results come first; facets follow. */}
        <div className="order-2 md:order-1">
          <Facets facets={result.facets} params={params} />
        </div>

        <section className="order-1 md:order-2">
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3 text-sm">
            <p className="text-muted">
              <span className="font-semibold text-foreground">{result.total.toLocaleString()}</span> repositories
              <span className="ml-2 text-xs">({result.took_ms} ms)</span>
              {activeFilters && (
                <Link href={params.get("q") ? `/?q=${encodeURIComponent(params.get("q")!)}` : "/"} className="ml-3 text-accent hover:underline">
                  Clear filters
                </Link>
              )}
            </p>
            <nav className="flex gap-1" aria-label="Search mode">
              {MODES.map((m) => (
                <Link
                  key={m.key}
                  title={m.title}
                  href={withParams(params, { mode: m.key === "lexical" ? undefined : m.key, page: undefined })}
                  className={`rounded px-2 py-1 ${mode === m.key ? "bg-accent/15 font-medium text-accent" : "text-muted hover:bg-accent/5"}`}
                >
                  {m.label}
                </Link>
              ))}
            </nav>
            <nav className="flex gap-1" aria-label="Sort">
              {SORTS.map((s) => (
                <Link
                  key={s.key}
                  href={withParams(params, { sort: s.key === "relevance" ? undefined : s.key, page: undefined })}
                  className={`rounded px-2 py-1 ${sort === s.key ? "bg-accent/15 font-medium text-accent" : "text-muted hover:bg-accent/5"}`}
                >
                  {s.label}
                </Link>
              ))}
            </nav>
          </div>

          {modeNote && <p className="mb-3 text-xs text-muted">{modeNote}</p>}

          {result.hits.length === 0 ? (
            <p className="rounded-lg border border-border bg-surface p-8 text-center text-muted">
              No repositories match. Try fewer filters or different words.
            </p>
          ) : (
            <div className="space-y-3">
              {result.hits.map((hit) => (
                <RepoCard key={hit.id} hit={hit} />
              ))}
            </div>
          )}

          <Pagination result={result} params={params} />
        </section>
      </div>
    </>
  );
}
