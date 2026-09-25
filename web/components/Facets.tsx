import Link from "next/link";
import type { Bucket } from "@/lib/api";
import { toggle, withParams } from "@/lib/params";

const STAR_RANGES: Record<string, { min?: string; max?: string }> = {
  "<100": { max: "99" },
  "100-1K": { min: "100", max: "999" },
  "1K-5K": { min: "1000", max: "4999" },
  "5K-10K": { min: "5000", max: "9999" },
  "10K+": { min: "10000" },
};

const ACTIVITY_LABELS: Record<string, string> = {
  "30d": "Pushed in last 30 days",
  "90d": "Pushed in last 90 days",
  "1y": "Pushed in last year",
};

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mb-6">
      <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted">{title}</h3>
      <ul className="space-y-1 text-sm">{children}</ul>
    </section>
  );
}

function Row({ href, label, count, selected }: { href: string; label: string; count: number; selected: boolean }) {
  return (
    <li>
      <Link
        href={href}
        className={`flex items-center justify-between rounded px-2 py-1 ${
          selected ? "bg-accent/15 font-medium text-accent" : "hover:bg-accent/5"
        } ${count === 0 && !selected ? "opacity-50" : ""}`}
      >
        <span className="truncate">
          {selected ? "✓ " : ""}
          {label}
        </span>
        <span className="text-xs tabular-nums text-muted">{count.toLocaleString()}</span>
      </Link>
    </li>
  );
}

export default function Facets({ facets, params }: { facets: Record<string, Bucket[]>; params: URLSearchParams }) {
  const terms = (facet: string, key: string, title: string) => {
    const buckets = facets[facet] ?? [];
    const selected = params.getAll(key);
    // Keep selected values visible even if they fell out of the top-N buckets.
    const missing = selected.filter((s) => !buckets.some((b) => b.key === s)).map((key) => ({ key, count: 0 }));
    const all = [...missing, ...buckets];
    if (all.length === 0) return null;
    return (
      <Section title={title}>
        {all.map((b) => (
          <Row key={b.key} href={toggle(params, key, b.key)} label={b.key} count={b.count} selected={selected.includes(b.key)} />
        ))}
      </Section>
    );
  };

  const min = params.get("min_stars") ?? undefined;
  const max = params.get("max_stars") ?? undefined;
  const within = params.get("pushed_within");

  return (
    <aside>
      {terms("language", "language", "Language")}
      {terms("license", "license", "License")}
      <Section title="Stars">
        {(facets.stars ?? []).map((b) => {
          const r = STAR_RANGES[b.key] ?? {};
          const selected = r.min === min && r.max === max;
          const href = selected
            ? withParams(params, { min_stars: undefined, max_stars: undefined, page: undefined })
            : withParams(params, { min_stars: r.min, max_stars: r.max, page: undefined });
          return <Row key={b.key} href={href} label={b.key} count={b.count} selected={selected} />;
        })}
      </Section>
      <Section title="Activity">
        {Object.keys(ACTIVITY_LABELS)
          .map((key) => (facets.activity ?? []).find((b) => b.key === key) ?? { key, count: 0 })
          .map((b) => (
            <Row
              key={b.key}
              href={toggle(params, "pushed_within", b.key)}
              label={ACTIVITY_LABELS[b.key]}
              count={b.count}
              selected={within === b.key}
            />
          ))}
      </Section>
      {terms("topics", "topic", "Topics")}
    </aside>
  );
}
