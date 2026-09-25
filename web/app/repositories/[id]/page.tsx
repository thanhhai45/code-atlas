import Link from "next/link";
import { notFound } from "next/navigation";
import RepoCard from "@/components/RepoCard";
import { getRepository, getSimilar } from "@/lib/api";
import { timeAgo } from "@/lib/params";

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface p-3">
      <div className="text-xs uppercase tracking-wide text-muted">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  );
}

export default async function RepositoryPage(props: PageProps<"/repositories/[id]">) {
  const { id } = await props.params;
  if (!/^\d+$/.test(id)) notFound();
  const [repo, similar] = await Promise.all([getRepository(id), getSimilar(id).catch(() => [])]);
  if (!repo) notFound();

  return (
    <article>
      <Link href="/" className="text-sm text-accent hover:underline">
        ← Back to search
      </Link>
      <header className="mt-3">
        <h1 className="text-2xl font-bold">
          <a href={repo.url} className="hover:underline" target="_blank" rel="noreferrer">
            {repo.full_name}
          </a>
          {repo.archived && <span className="ml-3 rounded bg-warn/15 px-2 py-0.5 align-middle text-sm text-warn">archived</span>}
        </h1>
        <p className="mt-2 text-muted">{repo.description}</p>
        <div className="mt-3 flex flex-wrap gap-1">
          {repo.topics.map((t) => (
            <Link key={t} href={`/?topic=${encodeURIComponent(t)}`} className="rounded-full bg-accent/10 px-2 py-0.5 text-xs text-accent hover:bg-accent/20">
              {t}
            </Link>
          ))}
        </div>
      </header>

      <section className="mt-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Stars" value={repo.stars.toLocaleString()} />
        <Stat label="Forks" value={repo.forks.toLocaleString()} />
        <Stat label="Language" value={repo.language || "—"} />
        <Stat label="License" value={repo.license || "Unknown"} />
        <Stat label="Last push" value={timeAgo(repo.pushed_at)} />
        <Stat label="Created" value={new Date(repo.created_at).getFullYear().toString()} />
        <Stat label="Open issues" value={repo.open_issues.toLocaleString()} />
        <Stat label="Homepage" value={repo.homepage ? new URL(repo.homepage.startsWith("http") ? repo.homepage : `https://${repo.homepage}`).hostname : "—"} />
      </section>

      {repo.readme && (
        <section className="mt-8">
          <h2 className="mb-2 text-lg font-semibold">README</h2>
          <pre className="max-h-[480px] overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-surface p-4 font-mono text-xs leading-relaxed">
            {repo.readme}
          </pre>
        </section>
      )}

      <section className="mt-8">
        <h2 className="mb-1 text-lg font-semibold">Similar repositories</h2>
        <p className="mb-3 text-xs text-muted">Lexical similarity (more_like_this over description, topics and README).</p>
        {similar.length === 0 ? (
          <p className="text-sm text-muted">No similar repositories found yet.</p>
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {similar.map((hit) => (
              <RepoCard key={hit.id} hit={hit} />
            ))}
          </div>
        )}
      </section>
    </article>
  );
}
