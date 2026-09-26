"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useRef, useState } from "react";

type Suggestion = { id: number; full_name: string; description: string; stars: number; language?: string };

export default function SearchBox() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const [query, setQuery] = useState(searchParams.get("q") ?? "");
  const [items, setItems] = useState<Suggestion[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const latest = useRef(0);
  const visible = query.trim().length >= 2 ? items : [];

  useEffect(() => {
    const q = query.trim();
    if (q.length < 2) return;
    // Debounce and drop out-of-order responses.
    const ticket = ++latest.current;
    const timer = setTimeout(async () => {
      try {
        const res = await fetch(`/api/suggest?q=${encodeURIComponent(q)}`);
        const data = (await res.json()) as { items: Suggestion[] };
        if (ticket === latest.current) setItems(data.items ?? []);
      } catch {
        if (ticket === latest.current) setItems([]);
      }
    }, 150);
    return () => clearTimeout(timer);
  }, [query]);

  function submit(q: string) {
    const next = new URLSearchParams(searchParams);
    next.delete("page");
    next.delete("cursor");
    if (q.trim()) next.set("q", q.trim());
    else next.delete("q");
    setOpen(false);
    router.push(`/?${next.toString()}`);
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setOpen(true);
      setActive((i) => Math.min(i + 1, visible.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => Math.max(i - 1, -1));
    } else if (e.key === "Escape") {
      setOpen(false);
    } else if (e.key === "Enter" && open && active >= 0 && visible[active]) {
      e.preventDefault();
      setOpen(false);
      router.push(`/repositories/${visible[active].id}`);
    }
  }

  return (
    <form
      role="search"
      className="relative w-full"
      onSubmit={(e) => {
        e.preventDefault();
        submit(query);
      }}
    >
      <input
        type="search"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setOpen(true);
          setActive(-1);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        onKeyDown={onKeyDown}
        placeholder="Search open source: vector database, rag framework, ruby elasticsearch…"
        aria-label="Search repositories"
        aria-autocomplete="list"
        className="w-full rounded-lg border border-border bg-surface px-4 py-3 text-base outline-none focus:border-accent focus:ring-2 focus:ring-accent/30"
      />
      {open && visible.length > 0 && (
        <ul
          role="listbox"
          className="absolute z-10 mt-1 w-full overflow-hidden rounded-lg border border-border bg-surface shadow-lg"
        >
          {visible.map((s, i) => (
            <li key={s.id} role="option" aria-selected={i === active}>
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => router.push(`/repositories/${s.id}`)}
                className={`flex w-full items-baseline justify-between gap-3 px-4 py-2 text-left ${
                  i === active ? "bg-accent/10" : "hover:bg-accent/5"
                }`}
              >
                <span className="truncate">
                  <span className="font-medium">{s.full_name}</span>
                  <span className="ml-2 text-sm text-muted">{s.description}</span>
                </span>
                <span className="shrink-0 text-xs text-muted">★ {s.stars.toLocaleString()}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </form>
  );
}
