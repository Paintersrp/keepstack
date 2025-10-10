import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createHighlight,
  deleteHighlight,
  listLinks,
  listTags,
  updateLink,
  type HighlightSummary,
  type LinkSummary,
  type ListLinksResponse,
  type TagWithCount,
} from "../api/client";
import { Button } from "../components/ui/button";
import { cn } from "../lib/utils";

const PAGE_SIZE = 20;

type SearchState = {
  q: string;
  favorite?: boolean;
  tags?: string[];
  page?: number;
};

type LinksQueryKey = ["links", SearchState];

interface ListPageProps {
  search: SearchState;
  onSearchChange: (next: SearchState) => void;
}

export function ListPage({ search, onSearchChange }: ListPageProps) {
  const [query, setQuery] = useState(search.q ?? "");
  const selectedTags = search.tags ?? [];
  const currentPage = search.page ?? 1;
  const queryKey = useMemo<LinksQueryKey>(() => ["links", search], [search]);
  const [highlightLinkId, setHighlightLinkId] = useState<string | null>(null);

  useEffect(() => {
    setQuery(search.q ?? "");
  }, [search.q]);

  const {
    data,
    isLoading,
    isError,
    error,
    isFetching,
  } = useQuery<ListLinksResponse, Error>({
    queryKey,
    queryFn: () =>
      listLinks({
        q: search.q || undefined,
        favorite: search.favorite,
        tags: selectedTags.length > 0 ? selectedTags : undefined,
        limit: PAGE_SIZE,
        offset: (currentPage - 1) * PAGE_SIZE,
      }),
    refetchOnWindowFocus: false,
  });

  const {
    data: tagsData,
    isLoading: areTagsLoading,
    isError: tagsError,
    error: tagsErrorDetails,
  } = useQuery<TagWithCount[], Error>({
    queryKey: ["tags"],
    queryFn: () => listTags(),
    staleTime: 1000 * 60 * 5,
  });

  const handleSubmit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    onSearchChange({ ...search, q: query.trim(), page: 1 });
  };

  const toggleFavoriteFilter = () => {
    onSearchChange({
      ...search,
      favorite: search.favorite ? undefined : true,
      page: 1,
    });
  };

  const toggleTag = (name: string) => {
    const nextTags = selectedTags.includes(name)
      ? selectedTags.filter((tag) => tag !== name)
      : [...selectedTags, name];
    onSearchChange({ ...search, tags: nextTags, page: 1 });
  };

  const clearTags = () => {
    if (selectedTags.length === 0) return;
    onSearchChange({ ...search, tags: [], page: 1 });
  };

  const goToPage = (page: number) => {
    onSearchChange({ ...search, page });
  };

  const selectedLink = highlightLinkId
    ? data?.items.find((item) => item.id === highlightLinkId) ?? null
    : null;

  useEffect(() => {
    if (highlightLinkId && !selectedLink) {
      setHighlightLinkId(null);
    }
  }, [highlightLinkId, selectedLink]);

  const totalPages = data && data.limit > 0 ? Math.max(1, Math.ceil(data.total_count / data.limit)) : 1;

  return (
    <div className="grid gap-6 lg:grid-cols-[260px_1fr]">
      <aside className="space-y-6 rounded-lg border border-slate-800 bg-slate-900/40 p-5">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-slate-300">Filters</h2>
          {selectedTags.length > 0 && (
            <button
              type="button"
              className="text-xs text-slate-400 hover:text-slate-200"
              onClick={clearTags}
            >
              Clear
            </button>
          )}
        </div>
        <div className="space-y-3">
          <button
            type="button"
            onClick={toggleFavoriteFilter}
            className={cn(
              "flex w-full items-center justify-between rounded-md border px-3 py-2 text-sm transition",
              search.favorite
                ? "border-amber-400 bg-amber-400/10 text-amber-300"
                : "border-slate-800 bg-slate-950 text-slate-300 hover:border-slate-700 hover:text-slate-100"
            )}
          >
            <span>Favorites only</span>
            {search.favorite && <span className="text-xs uppercase">On</span>}
          </button>
          <div className="space-y-2">
            <p className="text-xs uppercase tracking-wide text-slate-400">Tags</p>
            <div className="flex flex-wrap gap-2">
              {tagsData && tagsData.length > 0 ? (
                tagsData.map((tag) => (
                  <TagChip
                    key={tag.id}
                    tag={tag}
                    selected={selectedTags.includes(tag.name)}
                    onToggle={() => toggleTag(tag.name)}
                  />
                ))
              ) : areTagsLoading ? (
                <p className="text-xs text-slate-500">Loading tags…</p>
              ) : tagsError ? (
                <p className="text-xs text-red-400">{tagsErrorDetails?.message ?? "Failed to load tags"}</p>
              ) : (
                <p className="text-xs text-slate-500">No tags yet</p>
              )}
            </div>
          </div>
        </div>
      </aside>

      <div className="space-y-6">
        <section className="rounded-lg border border-slate-800 bg-slate-900/60 p-6">
          <form onSubmit={handleSubmit} className="flex flex-col gap-3 sm:flex-row">
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search your links"
              className="flex-1 rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-sm outline-none focus:border-slate-400"
            />
            <div className="flex gap-3">
              <Button type="submit">Search</Button>
              <Button type="button" variant={search.favorite ? "secondary" : "default"} onClick={toggleFavoriteFilter}>
                {search.favorite ? "Showing favorites" : "All links"}
              </Button>
            </div>
          </form>
          {isError && <p className="mt-3 text-sm text-red-400">{error?.message ?? "Failed to load links"}</p>}
        </section>

        {selectedTags.length > 0 && (
          <div className="flex flex-wrap gap-2 text-sm">
            {selectedTags.map((tag) => (
              <span key={tag} className="rounded-full bg-slate-800 px-3 py-1 text-slate-200">
                #{tag}
              </span>
            ))}
          </div>
        )}

        <section className="space-y-4">
          {(isLoading || isFetching) && <p className="text-sm text-slate-300">Loading...</p>}
          {!isLoading && data && data.items.length === 0 && (
            <p className="text-sm text-slate-400">No links found. Try saving one from the Add page.</p>
          )}
          {data?.items.map((item) => (
            <LinkCard
              key={item.id}
              link={item}
              queryKey={queryKey}
              onOpenHighlights={() => setHighlightLinkId(item.id)}
            />
          ))}
        </section>

        {data && data.total_count > data.limit && (
          <nav className="flex items-center justify-between rounded-lg border border-slate-800 bg-slate-900/60 p-4 text-sm">
            <span className="text-slate-400">
              Page {currentPage} of {totalPages}
            </span>
            <div className="flex items-center gap-2">
              <Button type="button" variant="secondary" disabled={currentPage <= 1} onClick={() => goToPage(Math.max(1, currentPage - 1))}>
                Previous
              </Button>
              <Button
                type="button"
                variant="secondary"
                disabled={currentPage >= totalPages}
                onClick={() => goToPage(Math.min(totalPages, currentPage + 1))}
              >
                Next
              </Button>
            </div>
          </nav>
        )}
      </div>

      {selectedLink && (
        <HighlightDrawer
          link={selectedLink}
          queryKey={queryKey}
          onClose={() => setHighlightLinkId(null)}
        />
      )}
    </div>
  );
}

interface LinkCardProps {
  link: LinkSummary;
  queryKey: LinksQueryKey;
  onOpenHighlights: () => void;
}

function LinkCard({ link, queryKey, onOpenHighlights }: LinkCardProps) {
  const queryClient = useQueryClient();
  const { mutate: toggleFavorite, isPending } = useMutation({
    mutationFn: (nextFavorite: boolean) => updateLink(link.id, { favorite: nextFavorite }),
    onSuccess: (updatedLink) => {
      queryClient.setQueryData<ListLinksResponse>(queryKey, (current) => {
        if (!current) return current;
        return {
          ...current,
          items: current.items.map((item) => (item.id === updatedLink.id ? { ...item, ...updatedLink } : item)),
        };
      });
    },
  });

  return (
    <article className="space-y-3 rounded-lg border border-slate-800 bg-slate-900/50 p-5">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-2">
          <a
            href={link.url}
            target="_blank"
            rel="noreferrer"
            className="text-lg font-semibold text-slate-100 hover:underline"
          >
            {link.title || link.archive_title || link.source_domain || extractDomain(link.url)}
          </a>
          <div className="flex flex-wrap items-center gap-3 text-xs text-slate-400">
            {link.source_domain && <span>{link.source_domain}</span>}
            {link.byline && <span>By {link.byline}</span>}
            {link.lang && <span className="uppercase">{link.lang}</span>}
            {link.word_count > 0 && <span>{link.word_count.toLocaleString()} words</span>}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant={link.favorite ? "secondary" : "default"}
            disabled={isPending}
            onClick={() => toggleFavorite(!link.favorite)}
          >
            {link.favorite ? "Favorited" : "Mark favorite"}
          </Button>
          <Button type="button" variant="secondary" onClick={onOpenHighlights}>
            View highlights
          </Button>
        </div>
      </div>
      <p className="text-sm text-slate-300">
        {link.extracted_text ? truncate(link.extracted_text, 240) : "Awaiting processing..."}
      </p>
      {link.tags.length > 0 && (
        <div className="flex flex-wrap gap-2 text-xs">
          {link.tags.map((tag) => (
            <span key={tag.id} className="rounded-full bg-slate-800 px-2 py-1 text-slate-300">
              #{tag.name}
            </span>
          ))}
        </div>
      )}
      <div className="flex flex-col gap-1 text-xs text-slate-400">
        <span>
          {link.highlights.length} highlight{link.highlights.length === 1 ? "" : "s"}
        </span>
        <span>Saved {new Date(link.created_at).toLocaleString()}</span>
      </div>
    </article>
  );
}

interface HighlightDrawerProps {
  link: LinkSummary;
  queryKey: LinksQueryKey;
  onClose: () => void;
}

function HighlightDrawer({ link, queryKey, onClose }: HighlightDrawerProps) {
  const queryClient = useQueryClient();
  const [quote, setQuote] = useState("");
  const [annotation, setAnnotation] = useState("");

  const { mutateAsync: handleCreate, isPending: isCreating, error: createError } = useMutation({
    mutationFn: () => createHighlight(link.id, { quote: quote.trim(), annotation: annotation.trim() || undefined }),
    onSuccess: (newHighlight) => {
      queryClient.setQueryData<ListLinksResponse>(queryKey, (current) => {
        if (!current) return current;
        return {
          ...current,
          items: current.items.map((item) =>
            item.id === link.id
              ? { ...item, highlights: [...item.highlights, newHighlight] }
              : item
          ),
        };
      });
      setQuote("");
      setAnnotation("");
    },
  });

  const { mutate: handleDelete, isPending: isDeleting } = useMutation({
    mutationFn: (highlightId: string) => deleteHighlight(link.id, highlightId),
    onSuccess: (_, highlightId) => {
      queryClient.setQueryData<ListLinksResponse>(queryKey, (current) => {
        if (!current) return current;
        return {
          ...current,
          items: current.items.map((item) =>
            item.id === link.id
              ? {
                  ...item,
                  highlights: item.highlights.filter((highlight) => highlight.id !== highlightId),
                }
              : item
          ),
        };
      });
    },
  });

  const submitHighlight = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!quote.trim()) return;
    await handleCreate();
  };

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-end bg-slate-950/50 backdrop-blur">
      <div className="h-full w-full max-w-lg overflow-y-auto border-l border-slate-800 bg-slate-950 p-6 shadow-xl">
        <div className="flex items-start justify-between">
          <div>
            <h2 className="text-lg font-semibold text-slate-100">Highlights</h2>
            <p className="text-sm text-slate-400">{link.title || extractDomain(link.url)}</p>
          </div>
          <Button type="button" variant="secondary" onClick={onClose}>
            Close
          </Button>
        </div>

        <form onSubmit={submitHighlight} className="mt-6 space-y-4">
          <div className="space-y-2">
            <label htmlFor="highlight-quote" className="text-sm font-medium text-slate-200">
              Quote
            </label>
            <textarea
              id="highlight-quote"
              value={quote}
              onChange={(event) => setQuote(event.target.value)}
              required
              rows={3}
              className="w-full rounded-md border border-slate-700 bg-slate-900 px-3 py-2 text-sm text-slate-100 outline-none focus:border-slate-400"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="highlight-annotation" className="text-sm font-medium text-slate-200">
              Annotation
            </label>
            <textarea
              id="highlight-annotation"
              value={annotation}
              onChange={(event) => setAnnotation(event.target.value)}
              rows={3}
              className="w-full rounded-md border border-slate-700 bg-slate-900 px-3 py-2 text-sm text-slate-100 outline-none focus:border-slate-400"
            />
          </div>
          {createError && <p className="text-sm text-red-400">{createError.message}</p>}
          <div className="flex justify-end">
            <Button type="submit" disabled={isCreating || !quote.trim()}>
              {isCreating ? "Saving..." : "Add highlight"}
            </Button>
          </div>
        </form>

        <div className="mt-8 space-y-4">
          {link.highlights.length === 0 && (
            <p className="text-sm text-slate-400">No highlights yet. Save one using the form above.</p>
          )}
          {link.highlights.map((highlight) => (
            <HighlightCard
              key={highlight.id}
              highlight={highlight}
              onDelete={() => handleDelete(highlight.id)}
              disabled={isDeleting}
            />
          ))}
        </div>
      </div>
    </div>
  );
}

interface HighlightCardProps {
  highlight: HighlightSummary;
  onDelete: () => void;
  disabled?: boolean;
}

function HighlightCard({ highlight, onDelete, disabled }: HighlightCardProps) {
  return (
    <article className="space-y-3 rounded-lg border border-slate-800 bg-slate-900/70 p-4">
      <p className="text-sm text-slate-100">“{highlight.quote}”</p>
      {highlight.annotation && <p className="text-sm text-slate-300">{highlight.annotation}</p>}
      <div className="flex items-center justify-between text-xs text-slate-500">
        <span>Saved {new Date(highlight.created_at).toLocaleString()}</span>
        <button
          type="button"
          onClick={onDelete}
          disabled={disabled}
          className="text-xs text-red-400 hover:text-red-300 disabled:opacity-60"
          aria-label="Delete highlight"
        >
          Delete
        </button>
      </div>
    </article>
  );
}

interface TagChipProps {
  tag: TagWithCount;
  selected: boolean;
  onToggle: () => void;
}

function TagChip({ tag, selected, onToggle }: TagChipProps) {
  return (
    <button
      type="button"
      onClick={onToggle}
      className={cn(
        "rounded-full border px-3 py-1 text-xs transition",
        selected
          ? "border-amber-400 bg-amber-400/10 text-amber-200"
          : "border-slate-800 bg-slate-950 text-slate-300 hover:border-slate-700 hover:text-slate-100"
      )}
    >
      #{tag.name}
      <span className="ml-1 text-[10px] text-slate-500">{tag.link_count}</span>
    </button>
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

export type { SearchState };
