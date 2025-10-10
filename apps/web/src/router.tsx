import { QueryClient } from "@tanstack/react-query";
import {
  Link,
  Outlet,
  RouterProvider,
  createRootRouteWithContext,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import { TanStackRouterDevtools } from "@tanstack/router-devtools";
import { ListPage } from "./pages/ListPage";
import { AddPage } from "./pages/AddPage";

export interface RouterContext {
  queryClient: QueryClient;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
});

const listRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  validateSearch: (search: Record<string, unknown>) => ({
    q: typeof search.q === "string" ? search.q : "",
    tags:
      typeof search.tags === "string"
        ? search.tags.split(",").filter(Boolean)
        : Array.isArray(search.tags)
        ? (search.tags.filter((value): value is string => typeof value === "string") as string[])
        : [],
    favorite:
      typeof search.favorite === "string"
        ? search.favorite === "true"
          ? true
          : search.favorite === "false"
          ? false
          : undefined
        : typeof search.favorite === "boolean"
        ? search.favorite
        : undefined,
    page:
      typeof search.page === "string"
        ? Math.max(1, Number.parseInt(search.page, 10) || 1)
        : typeof search.page === "number"
        ? Math.max(1, Math.floor(search.page))
        : 1,
  }),
  component: () => {
    const navigate = listRoute.useNavigate();
    const search = listRoute.useSearch();
    return (
      <ListPage
        search={search}
        onSearchChange={(next) =>
          navigate({
            to: "/",
            search: {
              q: next.q ?? "",
              favorite: next.favorite,
              tags: next.tags && next.tags.length > 0 ? next.tags : undefined,
              page: next.page && next.page > 1 ? next.page : undefined,
            },
          })
        }
      />
    );
  },
});

const addRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/add",
  component: () => {
    const navigate = addRoute.useNavigate();
    return (
      <AddPage
        onSuccess={() => navigate({ to: "/", search: { q: "", favorite: undefined } })}
      />
    );
  },
});

const routeTree = rootRoute.addChildren([listRoute, addRoute]);

export const queryClient = new QueryClient();

export const router = createRouter({
  routeTree,
  context: { queryClient },
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

function RootLayout() {
  return (
    <div className="min-h-screen bg-slate-950 text-slate-100">
      <header className="border-b border-slate-800">
        <nav className="mx-auto flex max-w-4xl items-center justify-between px-6 py-4">
          <Link
            to="/"
            search={{ q: "", favorite: undefined }}
            className="text-lg font-semibold text-slate-100"
          >
            Keepstack
          </Link>
          <Link
            to="/add"
            search={{}}
            className="text-sm font-medium text-slate-300 hover:text-white"
          >
            Add link
          </Link>
        </nav>
      </header>
      <main className="mx-auto max-w-4xl px-6 py-8">
        <Outlet />
      </main>
      {import.meta.env.DEV && <TanStackRouterDevtools position="bottom-right" />}
    </div>
  );
}

export function AppRouter() {
  return <RouterProvider router={router} />;
}
