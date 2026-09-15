import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, KeyRound, Plus } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCan, useCurrentUser } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatAgo,
  formatDateTime,
  MessageRow,
  roles,
  SecretValue,
  StatusDot,
} from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type ApiToken = Schemas["ApiToken"];
type Role = Schemas["Role"];

function tokenState(t: ApiToken): "revoked" | "expired" | "active" {
  if (t.revoked_at) return "revoked";
  if (t.expires_at && new Date(t.expires_at).getTime() <= Date.now())
    return "expired";
  return "active";
}

export function ApiTokensPage() {
  const allowed = useCan("listApiTokens");
  const canCreate = useCan("createApiToken");
  const canRevoke = useCan("revokeApiToken");
  const canListUsers = useCan("listUsers");
  const qc = useQueryClient();
  const tokens = useQuery({
    queryKey: ["api-tokens"],
    queryFn: async () => unwrap(await api.GET("/api-tokens")),
    enabled: allowed,
  });
  const users = useQuery({
    queryKey: ["users"],
    queryFn: async () => unwrap(await api.GET("/users")),
    enabled: canListUsers,
  });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<ApiToken | null>(null);
  const owners = new Map((users.data ?? []).map((u) => [u.id, u.username]));
  const rows = tokens.data ?? [];

  if (!allowed) {
    return (
      <>
        <PageHeader title="API tokens" />
        <Alert>
          <AlertDescription>
            API tokens are managed by administrators.
          </AlertDescription>
        </Alert>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title="API tokens"
        description="Bearer tokens for automation. A token acts with its own role, which never exceeds its creator's."
        actions={
          canCreate && (
            <Button data-testid="token-add" onClick={() => setCreating(true)}>
              <KeyRound className="mr-1.5 h-4 w-4" />
              Create token
            </Button>
          )
        }
      />
      <ErrorAlert
        error={tokens.error}
        prefix="Could not load API tokens"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Name</TableHead>
              <TableHead className="h-10">Token</TableHead>
              <TableHead className="h-10">Role</TableHead>
              <TableHead className="h-10">Owner</TableHead>
              <TableHead className="h-10">State</TableHead>
              <TableHead className="h-10">Last used</TableHead>
              <TableHead className="h-10">Expires</TableHead>
              {canRevoke && <TableHead className="h-10 w-16" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((t) => {
              const state = tokenState(t);
              return (
                <TableRow key={t.id} data-testid={`token-row-${t.name}`}>
                  <TableCell className="py-2.5 font-medium whitespace-nowrap">
                    {t.name}
                    <div className="text-muted-foreground text-xs font-normal">
                      created {formatDateTime(t.created_at)}
                    </div>
                  </TableCell>
                  <TableCell className="text-muted-foreground py-2.5 font-mono text-[13px] whitespace-nowrap">
                    {t.prefix}…
                  </TableCell>
                  <TableCell className="py-2.5">
                    <Badge
                      variant={t.role === "admin" ? "default" : "secondary"}
                    >
                      {t.role}
                    </Badge>
                  </TableCell>
                  <TableCell className="py-2.5">
                    {owners.get(t.user_id) ?? (
                      <span className="text-muted-foreground font-mono text-xs">
                        {t.user_id.slice(0, 8)}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="py-2.5">
                    <StatusDot
                      tone={
                        state === "active"
                          ? "success"
                          : state === "revoked"
                            ? "destructive"
                            : "muted"
                      }
                    >
                      {state}
                    </StatusDot>
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    <span title={formatDateTime(t.last_used_at)}>
                      {formatAgo(t.last_used_at)}
                    </span>
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    {t.expires_at ? (
                      <span title={formatDateTime(t.expires_at)}>
                        {formatAgo(t.expires_at)}
                      </span>
                    ) : (
                      <span className="text-muted-foreground">never</span>
                    )}
                  </TableCell>
                  {canRevoke && (
                    <TableCell className="py-1.5 text-right">
                      {state === "active" && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="hover:text-destructive h-8 w-8"
                          data-testid={`token-revoke-${t.name}`}
                          aria-label={`Revoke ${t.name}`}
                          title="Revoke"
                          onClick={() => setRevoking(t)}
                        >
                          <Ban className="h-4 w-4" />
                        </Button>
                      )}
                    </TableCell>
                  )}
                </TableRow>
              );
            })}
            {tokens.isPending && <MessageRow colSpan={8}>Loading…</MessageRow>}
            {tokens.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={8}>No API tokens yet.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {creating && <TokenDialog onClose={() => setCreating(false)} />}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          description="Requests using this token are refused from now on. This cannot be undone."
          confirmLabel="Revoke token"
          pendingLabel="Revoking…"
          onConfirm={async () => {
            unwrap(
              await api.DELETE("/api-tokens/{id}", {
                params: { path: { id: revoking.id } },
              }),
            );
            await qc.invalidateQueries({ queryKey: ["api-tokens"] });
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </>
  );
}

const expiryOptions = [
  { value: "never", label: "Never", days: 0 },
  { value: "30", label: "30 days", days: 30 },
  { value: "90", label: "90 days", days: 90 },
  { value: "365", label: "1 year", days: 365 },
];

function TokenDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const { user } = useCurrentUser();
  const rank = roles.findIndex((r) => r.value === user?.role);
  const allowedRoles = roles.slice(0, rank + 1);
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [expiry, setExpiry] = useState("90");

  const create = useMutation({
    mutationFn: async () => {
      const days = expiryOptions.find((o) => o.value === expiry)?.days ?? 0;
      return unwrap(
        await api.POST("/api-tokens", {
          body: {
            name: name.trim(),
            role,
            expires_at: days
              ? new Date(Date.now() + days * 86_400_000).toISOString()
              : null,
          },
        }),
      );
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-tokens"] }),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        {create.data ? (
          <>
            <DialogHeader>
              <DialogTitle>Token created</DialogTitle>
              <DialogDescription>
                Copy it now: it is shown only once. Send it as{" "}
                <code className="font-mono text-xs">
                  Authorization: Bearer &lt;token&gt;
                </code>
                .
              </DialogDescription>
            </DialogHeader>
            <SecretValue value={create.data.token} testId="token-value" />
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create API token</DialogTitle>
              <DialogDescription>
                The token is tied to your account and stops working if your
                account is deleted.
              </DialogDescription>
            </DialogHeader>
            <form onSubmit={submit} className="grid gap-4">
              <ErrorAlert error={create.error} />
              <div className="grid gap-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="token-name">Name</Label>
                  <HelpTip id="token-name" label="Name" />
                </div>
                <Input
                  id="token-name"
                  data-testid="token-name"
                  required
                  maxLength={64}
                  placeholder="ci-deploy"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="grid gap-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label htmlFor="token-role">Role</Label>
                    <HelpTip id="token-role" label="Role" />
                  </div>
                  <Select
                    value={role}
                    onValueChange={(v) => setRole(v as Role)}
                  >
                    <SelectTrigger
                      id="token-role"
                      data-testid="token-role"
                      className="h-9"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {allowedRoles.map((r) => (
                        <SelectItem key={r.value} value={r.value}>
                          {r.value}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label htmlFor="token-expiry">Expires</Label>
                    <HelpTip id="token-expiry" label="Expires" />
                  </div>
                  <Select value={expiry} onValueChange={setExpiry}>
                    <SelectTrigger
                      id="token-expiry"
                      data-testid="token-expiry"
                      className="h-9"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {expiryOptions.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <p className="text-muted-foreground -mt-2 text-xs">
                {roles.find((r) => r.value === role)?.help}
              </p>
              <DialogFooter className="gap-2 pt-2">
                <Button type="button" variant="outline" onClick={onClose}>
                  Cancel
                </Button>
                <Button
                  type="submit"
                  data-testid="token-save"
                  disabled={create.isPending}
                >
                  <Plus className="mr-1.5 h-4 w-4" />
                  {create.isPending ? "Creating…" : "Create token"}
                </Button>
              </DialogFooter>
            </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
