import { createRootRoute, Outlet } from "@tanstack/solid-router";
import { Toaster } from "~/components/toast";

export const Route = createRootRoute({
  component: () => (
    <>
      <Outlet />
      <Toaster />
    </>
  ),
});
