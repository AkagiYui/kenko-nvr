import { createFileRoute, redirect, useNavigate } from "@tanstack/solid-router";
import { createSignal } from "solid-js";
import { isAuthed, login } from "~/lib/api";
import { toast } from "~/components/toast";

export const Route = createFileRoute("/login")({
  beforeLoad: () => {
    if (isAuthed()) throw redirect({ to: "/dashboard" });
  },
  component: LoginPage,
});

function LoginPage() {
  const navigate = useNavigate();
  const [username, setUsername] = createSignal("admin");
  const [password, setPassword] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  const doLogin = async () => {
    setBusy(true);
    try {
      await login(username(), password());
      await navigate({ to: "/dashboard" });
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="flex min-h-screen items-center justify-center p-4">
      <div class="card w-[340px] bg-base-200 border border-base-300 shadow-xl">
        <div class="card-body">
          <h1 class="text-2xl font-bold">Kenko NVR</h1>
          <p class="text-base-content/60 mb-2">请登录以继续</p>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              void doLogin();
            }}
          >
            <label class="label" for="login-user">
              <span class="label-text">用户名</span>
            </label>
            <input
              id="login-user"
              class="input input-bordered w-full"
              autocomplete="username"
              value={username()}
              onInput={(e) => setUsername(e.currentTarget.value)}
            />

            <label class="label mt-2" for="login-pass">
              <span class="label-text">密码</span>
            </label>
            <input
              id="login-pass"
              type="password"
              class="input input-bordered w-full"
              autocomplete="current-password"
              value={password()}
              onInput={(e) => setPassword(e.currentTarget.value)}
            />

            <button type="submit" class="btn btn-primary w-full mt-5" disabled={busy()}>
              {busy() ? "登录中…" : "登录"}
            </button>
          </form>
        </div>
      </div>
    </div>
  );
}
