import Link from "next/link";
import type { SearchResult } from "@/lib/api";
import { withParams } from "@/lib/params";

// Mirrors search.MaxResultWindow in the API: offsets beyond it are rejected by
// Elasticsearch, so deeper pages are reached with search_after cursors.
const MAX_RESULT_WINDOW = 10_000;

function NavLink({ href, children }: { href?: string; children: React.ReactNode }) {
  return href ? (
    <Link href={href} className="text-accent hover:underline">
      {children}
    </Link>
  ) : (
    <span className="text-muted">{children}</span>
  );
}

/**
 * Page numbers inside the result window, cursors beyond it. A cursor page cannot
 * know its page number or go back one page (search_after only moves forward),
 * so it offers "first page" instead of "previous".
 */
export default function Pagination({ result, params }: { result: SearchResult; params: URLSearchParams }) {
  const totalPages = Math.max(1, Math.ceil(result.total / result.size));
  const windowPages = Math.floor(MAX_RESULT_WINDOW / result.size);
  const nextByCursor = result.next_cursor ? withParams(params, { page: undefined, cursor: result.next_cursor }) : undefined;

  if (params.has("cursor")) {
    return (
      <nav className="mt-6 flex items-center justify-center gap-4 text-sm" aria-label="Pagination">
        <NavLink href={withParams(params, { page: undefined })}>« First page</NavLink>
        <span className="text-muted">Deep results</span>
        <NavLink href={nextByCursor}>Next →</NavLink>
      </nav>
    );
  }

  if (totalPages <= 1) return null;
  const page = result.page;
  const previous = page > 1 ? withParams(params, { page: String(page - 1) }) : undefined;
  let next: string | undefined;
  if (page < Math.min(totalPages, windowPages)) {
    next = withParams(params, { page: String(page + 1) });
  } else if (page < totalPages) {
    next = nextByCursor; // the next page lies beyond the offset window
  }

  return (
    <nav className="mt-6 flex items-center justify-center gap-4 text-sm" aria-label="Pagination">
      <NavLink href={previous}>← Previous</NavLink>
      <span className="text-muted">
        Page {page.toLocaleString()} of {totalPages.toLocaleString()}
      </span>
      <NavLink href={next}>Next →</NavLink>
    </nav>
  );
}
