import { render } from "solid-js/web";
import { RouterProvider, createRouter } from "@tanstack/solid-router";
import { routeTree } from "./routeTree.gen";
import { onUnauthorized } from "./lib/api";
import "./styles.css";

const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/solid-router" {
  interface Register {
    router: typeof router;
  }
}

// A 401 or explicit logout sends the user back to the login screen.
onUnauthorized(() => {
  void router.navigate({ to: "/login" });
});

render(() => <RouterProvider router={router} />, document.getElementById("root")!);
