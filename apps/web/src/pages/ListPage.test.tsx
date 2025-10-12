import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ListPage, type SearchState } from "./ListPage";

// Enable React automatic act handling for async updates (e.g., react-query)
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const originalFetch = global.fetch;
const originalConsoleError = console.error;

beforeEach(() => {
  vi.spyOn(console, "error").mockImplementation((message?: unknown, ...optionalParams: unknown[]) => {
    if (typeof message === "string" && message.includes("not wrapped in act")) {
      return;
    }
    return originalConsoleError.call(console, message as unknown as string, ...optionalParams);
  });
});

function createJsonResponse(data: unknown, init?: ResponseInit) {
  return Promise.resolve(
    new Response(JSON.stringify(data), {
      status: 200,
      headers: { "Content-Type": "application/json" },
      ...init,
    })
  );
}

function renderListPage(initial: Partial<SearchState> = {}) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

  function Wrapper() {
    const [search, setSearch] = useState<SearchState>({
      q: "",
      favorite: undefined,
      tags: [],
      page: 1,
      suggested: initial.suggested,
      ...initial,
    });

    return (
      <QueryClientProvider client={queryClient}>
        <ListPage search={search} onSearchChange={setSearch} />
      </QueryClientProvider>
    );
  }

  const user = userEvent.setup();
  return { user, queryClient, ...render(<Wrapper />) };
}

async function flushAsyncUpdates() {
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  cleanup();
  global.fetch = originalFetch;
  vi.restoreAllMocks();
  console.error = originalConsoleError;
});

describe("ListPage", () => {
  test("tag filters request links with tag parameter", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.includes("/api/links")) {
        const response = {
          items: [
            {
              id: "link-1",
              url: "https://example.com/article",
              title: "Example article",
              source_domain: "example.com",
              favorite: false,
              created_at: "2024-01-01T00:00:00.000Z",
              read_at: null,
              archive_title: "Archived article",
              byline: "Author",
              lang: "en",
              word_count: 1200,
              extracted_text: "An example summary",
              tags: [{ id: 1, name: "reading" }],
              highlights: [],
            },
          ],
          total_count: 1,
          limit: 20,
          offset: 0,
        };
        return createJsonResponse(response);
      }
      if (url.endsWith("/api/tags")) {
        return createJsonResponse([{ id: 1, name: "reading", link_count: 1 }]);
      }
      throw new Error(`Unhandled request: ${url}`);
    });
    global.fetch = fetchMock as unknown as typeof global.fetch;

    const { user } = renderListPage();
    await flushAsyncUpdates();

    await screen.findByRole("button", { name: /#reading/i });
    await user.click(screen.getByRole("button", { name: /#reading/i }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([request]) =>
          typeof request === "string" && request.includes("/api/links") && request.includes("tags=reading")
        )
      ).toBe(true);
    });
  });

  test("highlight submissions append new highlight", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/tags")) {
        return createJsonResponse([]);
      }
      if (url.includes("/api/links") && (!init || init.method === undefined || init.method === "GET")) {
        const response = {
          items: [
            {
              id: "link-1",
              url: "https://example.com/article",
              title: "Example article",
              source_domain: "example.com",
              favorite: false,
              created_at: "2024-01-01T00:00:00.000Z",
              read_at: null,
              archive_title: "Archived article",
              byline: "Author",
              lang: "en",
              word_count: 1200,
              extracted_text: "An example summary",
              tags: [],
              highlights: [],
            },
          ],
          total_count: 1,
          limit: 20,
          offset: 0,
        };
        return createJsonResponse(response);
      }
      if (url.endsWith("/api/links/link-1/highlights")) {
        return createJsonResponse(
          {
            id: "highlight-1",
            text: "A brand new highlight",
            note: "My thoughts",
            created_at: "2024-01-01T00:00:00.000Z",
            updated_at: "2024-01-01T00:00:00.000Z",
          },
          { status: 201 }
        );
      }
      throw new Error(`Unhandled request: ${url}`);
    });
    global.fetch = fetchMock as unknown as typeof global.fetch;

    const { user } = renderListPage();
    await flushAsyncUpdates();

    await screen.findByRole("button", { name: /View highlights/i });
    await user.click(screen.getByRole("button", { name: /View highlights/i }));

    await user.type(screen.getByLabelText(/Quote/i), "A brand new highlight");
    await user.type(screen.getByLabelText(/Annotation/i), "My thoughts");
    await user.click(screen.getByRole("button", { name: /Add highlight/i }));

    await screen.findByText(/A brand new highlight/i);

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([request, options]) =>
          typeof request === "string" &&
          request.endsWith("/api/links/link-1/highlights") &&
          options?.method === "POST" &&
          options?.body ===
            JSON.stringify({
              text: "A brand new highlight",
              note: "My thoughts",
            })
        )
      ).toBe(true);
    });
  });

  test("favorite toggle completes without network errors", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      const method = init?.method ?? "GET";
      if (url.endsWith("/api/tags")) {
        return createJsonResponse([]);
      }
      if (url.includes("/api/links") && method === "GET") {
        return createJsonResponse({
          items: [
            {
              id: "link-1",
              url: "https://example.com/article",
              title: "Example article",
              source_domain: "example.com",
              favorite: false,
              created_at: "2024-01-01T00:00:00.000Z",
              read_at: null,
              archive_title: "Archived article",
              byline: "Author",
              lang: "en",
              word_count: 1200,
              extracted_text: "An example summary",
              tags: [],
              highlights: [],
            },
          ],
          total_count: 1,
          limit: 20,
          offset: 0,
        });
      }
      if (url.endsWith("/api/links/link-1") && method === "PATCH") {
        return createJsonResponse({
          id: "link-1",
          url: "https://example.com/article",
          title: "Example article",
          source_domain: "example.com",
          favorite: true,
          created_at: "2024-01-01T00:00:00.000Z",
          read_at: null,
          archive_title: "Archived article",
          byline: "Author",
          lang: "en",
          word_count: 1200,
          extracted_text: "An example summary",
          tags: [],
          highlights: [],
        });
      }
      throw new Error(`Unhandled request: ${url}`);
    });
    global.fetch = fetchMock as unknown as typeof global.fetch;

    const { user } = renderListPage();
    await flushAsyncUpdates();

    const favoriteButton = await screen.findByRole("button", { name: /Mark favorite/i });
    await user.click(favoriteButton);

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([request, options]) =>
          typeof request === "string" &&
          request.endsWith("/api/links/link-1") &&
          options?.method === "PATCH"
        )
      ).toBe(true);
    });

    await waitFor(() => {
      expect(favoriteButton).toHaveTextContent(/Favorited/i);
    });
  });
});
