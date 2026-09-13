import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, KeyRound, Plus, Trash2, X } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  Fact,
  formatAgo,
  formatDateTime,
  MessageRow,
  SecretValue,
  StatusDot,
  useRevealRef,
} from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
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

type Engine = Schemas["Engine"];
type JoinToken = Schemas["JoinToken"];

const statusTone: Record<
  Engine["status"],
  "success" | "warning" | "destructive" | "muted"
> = {
  current: "success",
  behind: "warning",
  ahead: "warning",
  rejected: "destructive",
  disconnected: "muted",
  revoked: "destructive",
};

const statusHelp: Record<Engine["status"], string> = {
  current: "Running the latest configuration version",
  behind: "Connected but not yet on the latest configuration version",
  ahead:
    "Reports a configuration version newer than the database holds (restored backup?)",
  rejected: "Refused the latest configuration version",
  disconnected: "No live control stream",
  revoked: "Certificate revoked; the engine is refused by the management plane",
};

export function EnginesPage() {
  const canTokens = useCan("listJoinTokens");
  const engines = useQuery({
    queryKey: ["engines"],
    queryFn: async () => unwrap(await api.GET("/engines")),
    refetchInterval: 10_000,
  });
  const [openId, setOpenId] = useState<string | null>(null);
  const rows = engines.data ?? [];

  return (
    <>
      <PageHeader
        title="Engines"
        description="DNS engines enrolled with this management plane and the configuration each one runs."
      />
      <ErrorAlert
        error={engines.error}
        prefix="Could not load engines"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Node</TableHead>
              <TableHead className="h-10">Status</TableHead>
              <TableHead className="h-10 text-right">Config version</TableHead>
              <TableHead className="h-10">Engine version</TableHead>
              <TableHead className="h-10">Last seen</TableHead>
              <TableHead className="h-10">Problem</TableHead>
              <TableHead className="h-10 w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((e) => (
              <TableRow
                key={e.id}
                data-testid={`engine-row-${e.node_name}`}
                data-state={openId === e.id ? "selected" : undefined}
              >
                <TableCell className="py-2.5 font-medium whitespace-nowrap">
                  {e.node_name}
                </TableCell>
                <TableCell className="py-2.5" title={statusHelp[e.status]}>
                  <StatusDot tone={statusTone[e.status]}>{e.status}</StatusDot>
                </TableCell>
                <TableCell className="py-2.5 text-right tabular-nums">
                  {e.applied_version || "—"}
                </TableCell>
                <TableCell className="text-muted-foreground py-2.5 font-mono text-[13px]">
                  {e.engine_version || "—"}
                </TableCell>
                <TableCell className="py-2.5 whitespace-nowrap">
                  <span title={formatDateTime(e.last_seen_at)}>
                    {e.connected ? "now" : formatAgo(e.last_seen_at)}
                  </span>
                </TableCell>
                <TableCell
                  className="text-destructive max-w-[18rem] truncate py-2.5 text-sm"
                  title={problem(e)}
                >
                  {problem(e)}
                </TableCell>
                <TableCell className="py-2 text-right">
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-8"
                    data-testid={`engine-open-${e.node_name}`}
                    onClick={() => setOpenId(e.id)}
                  >
                    Details
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {engines.isPending && <MessageRow colSpan={7}>Loading…</MessageRow>}
            {engines.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={7}>
                No engines enrolled yet.{" "}
                {canTokens
                  ? "Create a join token below and start an engine with it."
                  : "An administrator can create a join token to enrol one."}
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {openId && <EngineDetail id={openId} onClose={() => setOpenId(null)} />}
      {canTokens && <JoinTokens />}
    </>
  );
}

function problem(e: Engine): string {
  if (e.status === "rejected" && e.rejected_reason)
    return `v${e.rejected_version}: ${e.rejected_reason}`;
  return e.persist_error;
}

function EngineDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const qc = useQueryClient();
  const canDelete = useCan("deleteEngine");
  const [deleting, setDeleting] = useState(false);
  const reveal = useRevealRef<HTMLDivElement>(id);
  const q = useQuery({
    queryKey: ["engines", id],
    queryFn: async () =>
      unwrap(await api.GET("/engines/{id}", { params: { path: { id } } })),
    refetchInterval: 10_000,
  });
  const e = q.data;
  return (
    <Card
      ref={reveal}
      data-testid="engine-detail"
      className="mt-4 overflow-hidden"
    >
      <div className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
        <div className="flex items-center gap-3">
          <h2 className="text-base font-semibold">{e?.node_name ?? "…"}</h2>
          {e && <StatusDot tone={statusTone[e.status]}>{e.status}</StatusDot>}
        </div>
        <div className="flex items-center gap-1.5">
          {e && canDelete && (
            <Button
              variant="outline"
              size="sm"
              className="hover:text-destructive"
              data-testid="engine-delete"
              onClick={() => setDeleting(true)}
            >
              <Trash2 className="mr-1.5 h-4 w-4" />
              Remove engine
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="h-9 w-9"
            aria-label="Close details"
            onClick={onClose}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </div>
      <div className="px-5 py-4">
        <ErrorAlert error={q.error} prefix="Could not load the engine" />
        {q.isPending && (
          <p className="text-muted-foreground text-sm">Loading…</p>
        )}
        {e && (
          <dl className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            <Fact label="Status">{statusHelp[e.status]}</Fact>
            <Fact label="Applied config version">{e.applied_version}</Fact>
            <Fact label="Rejected config version">
              {e.rejected_version ?? "—"}
            </Fact>
            <Fact label="Engine version">
              <span className="font-mono text-xs">
                {e.engine_version || "—"}
              </span>
            </Fact>
            <Fact label="Enrolled">{formatDateTime(e.enrolled_at)}</Fact>
            <Fact label="Last seen">{formatDateTime(e.last_seen_at)}</Fact>
            <Fact label="Engine ID" className="sm:col-span-2">
              <span className="font-mono text-xs break-all">{e.id}</span>
            </Fact>
            {e.rejected_reason && (
              <Fact
                label="Rejection reason"
                className="sm:col-span-2 lg:col-span-4"
              >
                <span className="text-destructive">{e.rejected_reason}</span>
              </Fact>
            )}
            {e.persist_error && (
              <Fact
                label="Persist error"
                className="sm:col-span-2 lg:col-span-4"
              >
                <span className="text-destructive">{e.persist_error}</span>
              </Fact>
            )}
          </dl>
        )}
      </div>
      {deleting && e && (
        <ConfirmDialog
          title={`Remove ${e.node_name}?`}
          description="Its certificate stops being accepted, so the engine loses its control connection. Enrol it again with a new join token to bring it back."
          confirmLabel="Remove engine"
          pendingLabel="Removing…"
          onConfirm={async () => {
            unwrap(
              await api.DELETE("/engines/{id}", { params: { path: { id } } }),
            );
            onClose();
            qc.removeQueries({ queryKey: ["engines", id] });
            await qc.invalidateQueries({ queryKey: ["engines"] });
          }}
          onClose={() => setDeleting(false)}
        />
      )}
    </Card>
  );
}

function tokenState(t: JoinToken): "revoked" | "expired" | "active" {
  if (t.revoked_at) return "revoked";
  if (new Date(t.expires_at).getTime() <= Date.now()) return "expired";
  return "active";
}

function JoinTokens() {
  const qc = useQueryClient();
  const canCreate = useCan("createJoinToken");
  const canRevoke = useCan("revokeJoinToken");
  const tokens = useQuery({
    queryKey: ["join-tokens"],
    queryFn: async () => unwrap(await api.GET("/join-tokens")),
  });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<JoinToken | null>(null);
  const rows = tokens.data ?? [];

  return (
    <section aria-labelledby="join-tokens-heading" className="mt-10">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="join-tokens-heading" className="text-sm font-semibold">
            Join tokens
          </h2>
          <p className="text-muted-foreground mt-0.5 text-sm">
            An engine presents a join token once to enrol and receive its
            certificate.
          </p>
        </div>
        {canCreate && (
          <Button
            variant="outline"
            data-testid="jointoken-add"
            onClick={() => setCreating(true)}
          >
            <KeyRound className="mr-1.5 h-4 w-4" />
            Create join token
          </Button>
        )}
      </div>
      <ErrorAlert
        error={tokens.error}
        prefix="Could not load join tokens"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Name</TableHead>
              <TableHead className="h-10">State</TableHead>
              <TableHead className="h-10 text-right">Uses</TableHead>
              <TableHead className="h-10">Created</TableHead>
              <TableHead className="h-10">Expires</TableHead>
              {canRevoke && <TableHead className="h-10 w-16" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((t) => {
              const state = tokenState(t);
              return (
                <TableRow key={t.id} data-testid={`jointoken-row-${t.name}`}>
                  <TableCell className="py-2.5 font-medium">{t.name}</TableCell>
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
                  <TableCell className="py-2.5 text-right tabular-nums">
                    {t.uses}
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    {formatDateTime(t.created_at)}
                    <span className="text-muted-foreground">
                      {" "}
                      by {t.created_by}
                    </span>
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    <span title={formatDateTime(t.expires_at)}>
                      {formatAgo(t.expires_at)}
                    </span>
                  </TableCell>
                  {canRevoke && (
                    <TableCell className="py-1.5 text-right">
                      {state === "active" && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="hover:text-destructive h-8 w-8"
                          data-testid={`jointoken-revoke-${t.name}`}
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
            {tokens.isPending && <MessageRow colSpan={6}>Loading…</MessageRow>}
            {tokens.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={6}>No join tokens.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {creating && <JoinTokenDialog onClose={() => setCreating(false)} />}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          description="Engines can no longer enrol with this token. Engines already enrolled keep working."
          confirmLabel="Revoke token"
          pendingLabel="Revoking…"
          onConfirm={async () => {
            unwrap(
              await api.DELETE("/join-tokens/{id}", {
                params: { path: { id: revoking.id } },
              }),
            );
            await qc.invalidateQueries({ queryKey: ["join-tokens"] });
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </section>
  );
}

const ttlOptions = [
  { value: "3600", label: "1 hour" },
  { value: "86400", label: "24 hours" },
  { value: "604800", label: "7 days" },
  { value: "2592000", label: "30 days" },
];

function JoinTokenDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [ttl, setTtl] = useState("86400");
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/join-tokens", {
          body: { name: name.trim(), ttl_seconds: Number(ttl) },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["join-tokens"] }),
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
              <DialogTitle>Join token created</DialogTitle>
              <DialogDescription>
                Copy it now: it is shown only once. Put it in the engine's{" "}
                <code className="font-mono text-xs">join_token_file</code>.
              </DialogDescription>
            </DialogHeader>
            <SecretValue value={create.data.token} testId="jointoken-value" />
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create join token</DialogTitle>
              <DialogDescription>
                A token can enrol engines until it expires or is revoked.
              </DialogDescription>
            </DialogHeader>
            <form onSubmit={submit} className="grid gap-4">
              <ErrorAlert error={create.error} />
              <div className="grid grid-cols-[1fr_10rem] gap-4">
                <div className="grid gap-1.5">
                  <Label htmlFor="jointoken-name">Name</Label>
                  <Input
                    id="jointoken-name"
                    data-testid="jointoken-name"
                    required
                    maxLength={64}
                    placeholder="rack-3 engines"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="jointoken-ttl">Valid for</Label>
                  <Select value={ttl} onValueChange={setTtl}>
                    <SelectTrigger
                      id="jointoken-ttl"
                      data-testid="jointoken-ttl"
                      className="h-9"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {ttlOptions.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <DialogFooter className="gap-2 pt-2">
                <Button type="button" variant="outline" onClick={onClose}>
                  Cancel
                </Button>
                <Button
                  type="submit"
                  data-testid="jointoken-save"
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
