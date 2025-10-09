export interface LinkSummary {
  id: string;
  url: string;
  title: string;
  favorite: boolean;
  created_at: string;
  read_at?: string | null;
  extracted_text: string;
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
  limit?: number;
  offset?: number;
}

export async function listLinks(params: ListLinksParams): Promise<ListLinksResponse> {
  const query = new URLSearchParams();
  if (params.q) query.set("q", params.q);
  if (typeof params.favorite === "boolean") query.set("favorite", String(params.favorite));
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
