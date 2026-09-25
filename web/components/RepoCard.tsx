import Link from "next/link";
import type { Hit } from "@/lib/api";
import { formatCount, timeAgo } from "@/lib/params";

// Highlight fragments are HTML-escaped by Elasticsearch (encoder: html); only <mark> is markup.
function Html({ html, className }: { html: string; className?: string }) {
  return <span className={className} dangerouslySetInnerHTML={{ __html: html }} />;
}

export default function RepoCard({ hit }: { hit: Hit }) {
  const description = hit.highlight?.description?.[0];
  const readme = hit.highlight?.readme;
  return (
    <article className="rounded-lg border border-border bg-surface p-4">
      <div className="flex items-baseline justify-between gap-3">
        <Link href={`/repositories/${hit.id}`} className="truncate text-lg font-semibold text-accent hover:underline">
          {hit.full_name}
        </Link>
        <span className="shrink-0 text-sm text-muted" title={`${hit.stars.toLocaleString()} stars`}>
          ★ {formatCount(hit.stars)}
        </span>
      </div>
      <p className="mt-1 text-sm leading-relaxed">
        {description ? <Html html={description} /> : hit.description || <em className="text-muted">No description</em>}
      </p>
      {readme && (
        <p className="mt-2 border-l-2 border-border pl-3 text-xs text-muted">
          {readme.map((f, i) => (
            <Html key={i} html={`…${f}… `} />
          ))}
        </p>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted">
        {hit.language && <span>{hit.language}</span>}
        {hit.license && <span>{hit.license}</span>}
        <span>updated {timeAgo(hit.pushed_at)}</span>
        {hit.archived && <span className="rounded bg-warn/15 px-1.5 py-0.5 text-warn">archived</span>}
        <span className="flex flex-wrap gap-1">
          {hit.topics.slice(0, 6).map((t) => (
            <Link key={t} href={`/?topic=${encodeURIComponent(t)}`} className="rounded-full bg-accent/10 px-2 py-0.5 text-accent hover:bg-accent/20">
              {t}
            </Link>
          ))}
        </span>
      </div>
    </article>
  );
}
