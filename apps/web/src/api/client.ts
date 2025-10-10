export interface TagSummary {
  id: number;
  name: string;
}

export interface TagWithCount extends TagSummary {
  link_count: number;
}

export interface HighlightSummary {
  id: string;
  quote: string;
  annotation?: string | null;
  created_at: string;
  updated_at: string;
}

export interface LinkSummary {
  id: string;
  url: string;
  title: string;
  source_domain: string;
  favorite: boolean;
  created_at: string;
  read_at?: string | null;
  archive_title: string;
  byline: string;
  lang: string;
  word_count: number;
  extracted_text: string;
  tags: TagSummary[];
  highlights: HighlightSummary[];
}

export interface ListLinksResponse {
  items: LinkSummary[];
  total_count: number;
  limit: number;
  offset: number;
}

const API_BASE = import.meta.env.VITE_API_BASE_URL ?? "/api";

type RequestInitWithBody = RequestInit & { body?: BodyInit | null };

async function request<T>(path: string, init?: RequestInitWithBody): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    headers: {
      "Content-Type": "application/json"
    },
    ...init
  });

  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `request failed (${response.status})`);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}

export interface ListLinksParams {
  q?: string;
  favorite?: boolean;
  tags?: string[];
  limit?: number;
  offset?: number;
}

export async function listLinks(params: ListLinksParams): Promise<ListLinksResponse> {
  const query = new URLSearchParams();
  if (params.q) query.set("q", params.q);
  if (typeof params.favorite === "boolean") query.set("favorite", String(params.favorite));
  if (params.tags && params.tags.length > 0) query.set("tags", params.tags.join(","));
  if (typeof params.limit === "number") query.set("limit", String(params.limit));
  if (typeof params.offset === "number") query.set("offset", String(params.offset));

  const queryString = query.toString();
  const path = queryString ? `/links?${queryString}` : "/links";
  return request<ListLinksResponse>(path, { method: "GET" });
}

export interface CreateLinkInput {
  url: string;
  title?: string;
  favorite?: boolean;
}

export interface CreateLinkResponse {
  id: string;
  url: string;
}

export function createLink(input: CreateLinkInput): Promise<CreateLinkResponse> {
  return request<CreateLinkResponse>("/links", {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export interface UpdateLinkInput {
  favorite?: boolean;
  tags?: string[];
}

export function updateLink(linkId: string, input: UpdateLinkInput): Promise<LinkSummary> {
  return request<LinkSummary>(`/links/${linkId}`, {
    method: "PATCH",
    body: JSON.stringify(input)
  });
}

export function listTags(): Promise<TagWithCount[]> {
  return request<TagWithCount[]>("/tags", { method: "GET" });
}

export interface CreateTagInput {
  name: string;
}

export function createTag(input: CreateTagInput): Promise<TagWithCount> {
  return request<TagWithCount>("/tags", {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function deleteTag(tagId: number): Promise<void> {
  return request<void>(`/tags/${tagId}`, { method: "DELETE" });
}

export interface CreateHighlightInput {
  quote: string;
  annotation?: string;
}

export function createHighlight(
  linkId: string,
  input: CreateHighlightInput
): Promise<HighlightSummary> {
  return request<HighlightSummary>(`/links/${linkId}/highlights`, {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function deleteHighlight(linkId: string, highlightId: string): Promise<void> {
  return request<void>(`/links/${linkId}/highlights/${highlightId}`, {
    method: "DELETE"
  });
}
