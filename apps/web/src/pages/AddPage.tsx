import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createLink } from "../api/client";
import { Button } from "../components/ui/button";

type AddPageProps = {
  onSuccess: () => void;
};

export function AddPage({ onSuccess }: AddPageProps) {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");

  const mutation = useMutation({
    mutationFn: createLink,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["links"], exact: false });
      setUrl("");
      setTitle("");
      onSuccess();
    },
  });

  const handleSubmit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!url.trim()) {
      return;
    }
    mutation.mutate({ url: url.trim(), title: title.trim() || undefined });
  };

  return (
    <div className="mx-auto max-w-xl space-y-6">
      <section className="space-y-2">
        <h1 className="text-2xl font-semibold">Save a link</h1>
        <p className="text-sm text-slate-300">
          Keepstack will fetch the page, extract the readable content, and make it searchable for later.
        </p>
      </section>
      <form onSubmit={handleSubmit} className="space-y-4 rounded-lg border border-slate-800 bg-slate-900/60 p-6">
        <label className="block space-y-2 text-sm">
          <span>URL</span>
          <input
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder="https://example.com/article"
            required
            className="w-full rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-sm outline-none focus:border-slate-400"
          />
        </label>
        <label className="block space-y-2 text-sm">
          <span>Title (optional)</span>
          <input
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder="Custom title"
            className="w-full rounded-md border border-slate-700 bg-slate-950 px-3 py-2 text-sm outline-none focus:border-slate-400"
          />
        </label>
        {mutation.isError && (
          <p className="text-sm text-red-400">{(mutation.error as Error)?.message ?? "Failed to save link"}</p>
        )}
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? "Saving..." : "Save link"}
        </Button>
      </form>
    </div>
  );
}
