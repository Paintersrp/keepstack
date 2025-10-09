import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listLinks, type ListLinksResponse } from "../api/client";
import { Button } from "../components/ui/button";

type SearchState = {
  q: string;
  favorite?: boolean;
};

interface ListPageProps {
  search: SearchState;
  onSearchChange: (next: SearchState) => void;
}

export function ListPage({ search, onSearchChange }: ListPageProps) {
  const [query, setQuery] = useState(search.q ?? "");

  useEffect(() => {
    setQuery(search.q ?? "");
  }, [search.q]);

  const queryKey = useMemo(() => ["links", search], [search]);

  const { data, isLoading, isError, error } = useQuery<ListLinksResponse, Error>({
    queryKey,
    queryFn: () => listLinks({ q: search.q || undefined, favorite: search.favorite }),
  });

  const handleSubmit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    onSearchChange({ ...search, q: query.trim() });
  };

  const toggleFavorite = () => {
    onSearchChange({ ...search, favorite: search.favorite ? undefined : true });
  };

  return (
    <div className="space-y-6">
      <section className="flex flex-col gap-4 rounded-lg border border-slate-800 bg-slate-900/60 p-6">
        <form onSubmit={handleSubmit} className="flex flex-col gap-3 sm:flex-row">
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search your links"
            className="flex-1 rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-sm outline-none focus:border-slate-400"
          />
          <div className="flex gap-3">
            <Button type="submit">Search</Button>
            <Button type="button" variant={search.favorite ? "secondary" : "default"} onClick={toggleFavorite}>
              {search.favorite ? "Showing favorites" : "All links"}
            </Button>
          </div>
        </form>
        {isError && <p className="text-sm text-red-400">{error?.message ?? "Failed to load links"}</p>}
      </section>

      <section className="space-y-4">
        {isLoading && <p className="text-sm text-slate-300">Loading...</p>}
        {!isLoading && data && data.items.length === 0 && (
          <p className="text-sm text-slate-400">No links found. Try saving one from the Add page.</p>
        )}
        {data?.items.map((item) => (
          <article key={item.id} className="space-y-2 rounded-lg border border-slate-800 bg-slate-900/50 p-5">
            <div className="flex items-center justify-between gap-2">
              <a
                href={item.url}
                target="_blank"
                rel="noreferrer"
                className="text-lg font-semibold text-slate-100 hover:underline"
              >
                {item.title || item.archive_title || item.source_domain || extractDomain(item.url)}
              </a>
              {item.favorite && <span className="text-xs uppercase tracking-wide text-amber-400">Favorite</span>}
            </div>
            <div className="flex flex-wrap items-center gap-3 text-xs text-slate-400">
              {item.source_domain && <span>{item.source_domain}</span>}
              {item.byline && <span>By {item.byline}</span>}
              {item.lang && <span className="uppercase">{item.lang}</span>}
              {item.word_count > 0 && <span>{item.word_count.toLocaleString()} words</span>}
            </div>
            <p className="text-sm text-slate-300 max-h-20 overflow-hidden">
              {item.extracted_text ? truncate(item.extracted_text, 240) : "Awaiting processing..."}
            </p>
            {item.tags.length > 0 && (
              <div className="flex flex-wrap gap-2 text-xs">
                {item.tags.map((tag) => (
                  <span key={tag.id} className="rounded-full bg-slate-800 px-2 py-1 text-slate-300">
                    #{tag.name}
                  </span>
                ))}
              </div>
            )}
            {item.highlights.length > 0 && (
              <p className="text-xs text-amber-300">
                {item.highlights.length} highlight{item.highlights.length > 1 ? "s" : ""}
              </p>
            )}
            <p className="text-xs text-slate-500">
              Saved {new Date(item.created_at).toLocaleString()}
            </p>
          </article>
        ))}
      </section>
    </div>
  );
}

function extractDomain(url: string) {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

function truncate(value: string, length: number) {
  if (value.length <= length) return value;
  return `${value.slice(0, length)}…`;
}
