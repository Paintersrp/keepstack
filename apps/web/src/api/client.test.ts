import { afterEach, describe, expect, test, vi } from "vitest";
import { listTags, type TagWithCount } from "./client";

const originalFetch = global.fetch;

afterEach(() => {
  global.fetch = originalFetch;
  vi.restoreAllMocks();
});

describe("listTags", () => {
  test("returns tags without reshaping the payload", async () => {
    const responseBody: TagWithCount[] = [
      { id: 1, name: "reading", link_count: 4 }
    ];

    const mockFetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(responseBody), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );

    global.fetch = mockFetch;

    await expect(listTags()).resolves.toEqual(responseBody);
    expect(mockFetch).toHaveBeenCalledWith("/api/tags", expect.objectContaining({ method: "GET" }));
  });
});
