import { createFileRoute } from "@tanstack/solid-router";
import { createResource, createSignal, For, Show } from "solid-js";
import { api, getUsername } from "~/lib/api";
import { toast } from "~/components/toast";
import { Modal } from "~/components/Modal";
import { fmtTime } from "~/lib/format";
import type { Role, User } from "~/lib/types";

export const Route = createFileRoute("/_authed/users")({
  component: Users,
});

const ROLE_LABEL: Record<Role, string> = {
  admin: "管理员",
  operator: "操作员",
  viewer: "访客",
};

function Users() {
  const [users, { refetch }] = createResource<User[]>(() => api("/users"));
  const [editing, setEditing] = createSignal<User | null>(null);
  const [adding, setAdding] = createSignal(false);

  const del = async (u: User) => {
    if (!confirm(`删除用户「${u.username}」？`)) return;
    try {
      await api(`/users/${u.id}`, { method: "DELETE" });
      toast("已删除");
      void refetch();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <>
      <div class="flex items-center mb-5">
        <h1 class="text-[22px] font-semibold flex-1">用户管理</h1>
        <button class="btn btn-primary btn-sm" onClick={() => setAdding(true)}>
          添加用户
        </button>
      </div>

      <div class="card bg-base-200 border border-base-300 overflow-x-auto">
        <table class="table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>角色</th>
              <th>创建时间</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <For each={users()}>
              {(u) => (
                <tr class="hover">
                  <td>
                    {u.username}
                    <Show when={u.username === getUsername()}>
                      <span class="badge badge-ghost badge-sm ml-2">当前</span>
                    </Show>
                  </td>
                  <td>{ROLE_LABEL[u.role] ?? u.role}</td>
                  <td>{fmtTime(u.createdAt)}</td>
                  <td class="text-right whitespace-nowrap">
                    <button class="btn btn-ghost btn-xs" onClick={() => setEditing(u)}>
                      编辑
                    </button>
                    <button class="btn btn-error btn-outline btn-xs ml-1" onClick={() => void del(u)}>
                      删除
                    </button>
                  </td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>

      <Show when={adding()}>
        <UserForm
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false);
            void refetch();
          }}
        />
      </Show>
      <Show when={editing()}>
        {(u) => (
          <UserForm
            user={u()}
            onClose={() => setEditing(null)}
            onSaved={() => {
              setEditing(null);
              void refetch();
            }}
          />
        )}
      </Show>
    </>
  );
}

function UserForm(props: { user?: User; onClose: () => void; onSaved: () => void }) {
  const u = props.user;
  const [username, setUsername] = createSignal(u?.username ?? "");
  const [password, setPassword] = createSignal("");
  const [role, setRole] = createSignal<Role>(u?.role ?? "viewer");

  const save = async () => {
    const body = { username: username(), password: password(), role: role() };
    if (u) await api(`/users/${u.id}`, { method: "PUT", body });
    else await api("/users", { method: "POST", body });
    toast("已保存");
    props.onSaved();
  };

  return (
    <Modal title={u ? "编辑用户" : "添加用户"} width={420} onOk={save} onClose={props.onClose}>
      <div class="space-y-3">
        <div>
          <label class="label"><span class="label-text">用户名</span></label>
          <input class="input input-bordered w-full" value={username()} onInput={(e) => setUsername(e.currentTarget.value)} />
        </div>
        <div>
          <label class="label">
            <span class="label-text">密码</span>
            <Show when={u}>
              <span class="label-text-alt text-base-content/40">留空表示不修改</span>
            </Show>
          </label>
          <input type="password" class="input input-bordered w-full" value={password()} onInput={(e) => setPassword(e.currentTarget.value)} />
        </div>
        <div>
          <label class="label"><span class="label-text">角色</span></label>
          <select class="select select-bordered w-full" value={role()} onChange={(e) => setRole(e.currentTarget.value as Role)}>
            <option value="admin">管理员（全部权限）</option>
            <option value="operator">操作员（管理摄像头、控制云台/对讲）</option>
            <option value="viewer">访客（仅查看）</option>
          </select>
        </div>
      </div>
    </Modal>
  );
}
