import { NextRequest } from "next/server";
import { API_URL } from "@/lib/api";

// Proxies autocomplete requests so the browser never needs to reach the Go API directly.
export async function GET(request: NextRequest) {
  const q = request.nextUrl.searchParams.get("q") ?? "";
  if (!q.trim()) return Response.json({ items: [] });
  const res = await fetch(`${API_URL}/suggest?q=${encodeURIComponent(q)}&size=8`, { cache: "no-store" });
  if (!res.ok) return Response.json({ items: [] }, { status: 502 });
  return Response.json(await res.json());
}
