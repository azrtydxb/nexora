import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCan, useCurrentUser } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatDateTime,
  MessageRow,
  roles,
  StatusDot,
} from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type User = Schemas["User"];
type Role = Schemas["Role"];

const minPassword = 12;

export function UsersPage() {
  const allowed = useCan("listUsers");
  const canCreate = useCan("createUser");
  const canUpdate = useCan("updateUser");
  const canDelete = useCan("deleteUser");
  const { user: me } = useCurrentUser();
  const users = useQuery({
    queryKey: ["users"],
    queryFn: async () => unwrap(await api.GET("/users")),
    enabled: allowed,
  });
  const [editing, setEditing] = useState<User | "new" | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);
  const qc = useQueryClient();
  const rows = users.data ?? [];

  if (!allowed) {
    return (
      <>
        <PageHeader title="Users" />
        <Alert>
          <AlertDescription>
            User management is available to administrators.
          </AlertDescription>
        </Alert>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title="Users"
        description="People who can sign in to this console. Users from the identity provider appear after their first sign-in."
        actions={
          canCreate && (
            <Button data-testid="user-add" onClick={() => setEditing("new")}>
              <Plus className="mr-1.5 h-4 w-4" />
              Add user
            </Button>
          )
        }
      />
      <ErrorAlert
        error={users.error}
        prefix="Could not load users"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Username</TableHead>
              <TableHead className="h-10">Email</TableHead>
              <TableHead className="h-10">Role</TableHead>
              <TableHead className="h-10">Sign-in</TableHead>
              <TableHead className="h-10">State</TableHead>
              <TableHead className="h-10">Created</TableHead>
              {(canUpdate || canDelete) && <TableHead className="h-10 w-24" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((u) => (
              <TableRow key={u.id} data-testid={`user-row-${u.username}`}>
                <TableCell className="py-2.5 font-medium whitespace-nowrap">
                  {u.username}
                  {u.id === me?.id && (
                    <span className="text-muted-foreground ml-1.5 font-normal">
                      (you)
                    </span>
                  )}
                </TableCell>
                <TableCell className="text-muted-foreground py-2.5">
                  {u.email || "—"}
                </TableCell>
                <TableCell className="py-2.5">
                  <Badge variant={u.role === "admin" ? "default" : "secondary"}>
                    {u.role}
                  </Badge>
                </TableCell>
                <TableCell className="py-2.5">
                  {u.source === "oidc" ? "Identity provider" : "Password"}
                </TableCell>
                <TableCell className="py-2.5">
                  {u.disabled ? (
                    <StatusDot tone="muted">Disabled</StatusDot>
                  ) : (
                    <StatusDot tone="success">Active</StatusDot>
                  )}
                </TableCell>
                <TableCell className="py-2.5 whitespace-nowrap">
                  {formatDateTime(u.created_at)}
                </TableCell>
                {(canUpdate || canDelete) && (
                  <TableCell className="py-2 text-right whitespace-nowrap">
                    {canUpdate && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        data-testid={`user-edit-${u.username}`}
                        aria-label={`Edit ${u.username}`}
                        onClick={() => setEditing(u)}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                    )}
                    {canDelete && u.id !== me?.id && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="hover:text-destructive h-8 w-8"
                        data-testid={`user-delete-${u.username}`}
                        aria-label={`Delete ${u.username}`}
                        onClick={() => setDeleting(u)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {users.isPending && <MessageRow colSpan={7}>Loading…</MessageRow>}
          </TableBody>
        </Table>
      </Card>
      {editing !== null && (
        <UserDialog
          user={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.username}?`}
          description="The user is signed out everywhere and can no longer sign in."
          confirmLabel="Delete user"
          pendingLabel="Deleting…"
          thing="This user"
          onConfirm={async () => {
            unwrap(
              await api.DELETE("/users/{id}", {
                params: {
                  path: { id: deleting.id },
                  query: { revision: deleting.revision },
                },
              }),
            );
            await qc.invalidateQueries({ queryKey: ["users"] });
          }}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

function UserDialog({
  user,
  onClose,
}: {
  user: User | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [form, setForm] = useState({
    username: user?.username ?? "",
    email: user?.email ?? "",
    password: "",
    role: user?.role ?? ("viewer" as Role),
    disabled: user?.disabled ?? false,
  });
  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const local = !user || user.source === "local";

  const save = useMutation({
    mutationFn: async () => {
      if (user) {
        return unwrap(
          await api.PUT("/users/{id}", {
            params: { path: { id: user.id } },
            body: {
              revision: user.revision,
              email: form.email.trim(),
              role: form.role,
              disabled: form.disabled,
              ...(form.password ? { password: form.password } : {}),
            },
          }),
        );
      }
      return unwrap(
        await api.POST("/users", {
          body: {
            username: form.username.trim(),
            email: form.email.trim(),
            password: form.password,
            role: form.role,
          },
        }),
      );
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["users"] });
      onClose();
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {user ? `Edit ${user.username}` : "Add user"}
          </DialogTitle>
          <DialogDescription>
            {user
              ? "Disabling a user or changing their password signs them out everywhere."
              : "The user signs in with this username and password."}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <ErrorAlert error={save.error} thing="This user" />
          {!user && (
            <div className="grid gap-1.5">
              <Label htmlFor="user-username">Username</Label>
              <Input
                id="user-username"
                data-testid="user-username"
                autoComplete="off"
                required
                maxLength={64}
                value={form.username}
                onChange={(e) => set("username", e.target.value)}
              />
            </div>
          )}
          <div className="grid gap-1.5">
            <Label htmlFor="user-email">Email</Label>
            <Input
              id="user-email"
              data-testid="user-email"
              type="email"
              autoComplete="off"
              value={form.email}
              onChange={(e) => set("email", e.target.value)}
            />
          </div>
          {local && (
            <div className="grid gap-1.5">
              <Label htmlFor="user-password">
                {user ? "New password" : "Password"}
              </Label>
              <Input
                id="user-password"
                data-testid="user-password"
                type="password"
                autoComplete="new-password"
                required={!user}
                minLength={minPassword}
                placeholder={
                  user ? "Leave empty to keep the current password" : undefined
                }
                aria-describedby="user-password-hint"
                value={form.password}
                onChange={(e) => set("password", e.target.value)}
              />
              <p
                id="user-password-hint"
                className="text-muted-foreground text-xs"
              >
                At least {minPassword} characters.
              </p>
            </div>
          )}
          <div className="grid gap-1.5">
            <Label htmlFor="user-role">Role</Label>
            <Select
              value={form.role}
              onValueChange={(v) => set("role", v as Role)}
            >
              <SelectTrigger
                id="user-role"
                data-testid="user-role"
                className="h-9"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {roles.map((r) => (
                  <SelectItem key={r.value} value={r.value}>
                    {r.value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">
              {roles.find((r) => r.value === form.role)?.help}
            </p>
          </div>
          {user && (
            <label className="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5 text-sm">
              <span>
                <span className="font-medium">Disabled</span>
                <span className="text-muted-foreground block text-xs">
                  A disabled user cannot sign in.
                </span>
              </span>
              <Switch
                checked={form.disabled}
                onCheckedChange={(v) => set("disabled", v)}
                data-testid="user-disabled"
              />
            </label>
          )}
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="user-save"
              disabled={save.isPending}
            >
              {save.isPending ? "Saving…" : user ? "Save changes" : "Add user"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
