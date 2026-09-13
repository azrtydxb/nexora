import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";

import { api, ApiError, unwrap, type Schemas } from "@/api/client";
import { useCan } from "@/auth/AuthProvider";
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

type Upstream = Schemas["Upstream"];
type Protocol = Upstream["protocol"];

// Labels lead with the short protocol name that the table badges show.
const protocolLabels: Record<Protocol, string> = {
  udp: "udp",
  tcp: "tcp",
  dot: "dot · DNS over TLS",
  doh: "doh · DNS over HTTPS",
};

const conflictMessage =
  "This upstream was changed by someone else — reload to see the latest version";

function errorMessage(err: unknown): string {
  if (err instanceof ApiError && err.status === 409) return conflictMessage;
  return err instanceof Error ? err.message : String(err);
}

export function UpstreamsPage() {
  const canCreate = useCan("createUpstream");
  const canUpdate = useCan("updateUpstream");
  const canDelete = useCan("deleteUpstream");
  const list = useQuery({
    queryKey: ["upstreams"],
    queryFn: async () => unwrap(await api.GET("/upstreams")),
  });
  const rows = [...(list.data ?? [])].sort(
    (a, b) => a.position - b.position || a.name.localeCompare(b.name),
  );
  const [editing, setEditing] = useState<Upstream | "new" | null>(null);
  const [deleting, setDeleting] = useState<Upstream | null>(null);

  return (
    <>
      <PageHeader
        title="Upstreams"
        description="Resolvers the engines forward to, tried in the order listed."
        actions={
          canCreate && (
            <Button
              data-testid="upstream-add"
              onClick={() => setEditing("new")}
            >
              <Plus className="mr-1.5 h-4 w-4" />
              Add upstream
            </Button>
          )
        }
      />
      {list.error && (
        <Alert variant="destructive" className="mb-4">
          <AlertDescription>
            Could not load upstreams: {errorMessage(list.error)}
          </AlertDescription>
        </Alert>
      )}
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-12">#</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Protocol</TableHead>
              <TableHead>Target</TableHead>
              <TableHead className="text-right">Timeout</TableHead>
              <TableHead>State</TableHead>
              {(canUpdate || canDelete) && <TableHead className="w-24" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((u) => (
              <TableRow key={u.id} data-testid={`upstream-row-${u.name}`}>
                <TableCell className="text-muted-foreground py-3">
                  {u.position}
                </TableCell>
                <TableCell className="py-3 font-medium whitespace-nowrap">
                  {u.name}
                </TableCell>
                <TableCell className="py-3">
                  <Badge variant="secondary" title={protocolLabels[u.protocol]}>
                    {u.protocol.toUpperCase()}
                  </Badge>
                </TableCell>
                <TableCell className="py-3 font-mono text-[13px]">
                  {u.protocol === "doh" ? u.doh_url : u.address}
                  {u.protocol === "dot" && u.tls_server_name && (
                    <span className="text-muted-foreground">
                      {" "}
                      ({u.tls_server_name})
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-3 text-right tabular-nums">
                  {u.timeout_ms} ms
                </TableCell>
                <TableCell className="py-3">
                  <span className="inline-flex items-center gap-1.5 text-sm">
                    <span
                      className={
                        u.enabled
                          ? "bg-success h-1.5 w-1.5 rounded-full"
                          : "bg-muted-foreground/50 h-1.5 w-1.5 rounded-full"
                      }
                    />
                    {u.enabled ? "Enabled" : "Disabled"}
                  </span>
                </TableCell>
                {(canUpdate || canDelete) && (
                  <TableCell className="py-2 text-right whitespace-nowrap">
                    {canUpdate && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        data-testid={`upstream-edit-${u.name}`}
                        aria-label={`Edit ${u.name}`}
                        onClick={() => setEditing(u)}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                    )}
                    {canDelete && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="hover:text-destructive h-8 w-8"
                        data-testid={`upstream-delete-${u.name}`}
                        aria-label={`Delete ${u.name}`}
                        onClick={() => setDeleting(u)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {list.isSuccess && rows.length === 0 && (
              <TableRow className="hover:bg-transparent">
                <TableCell
                  colSpan={7}
                  className="text-muted-foreground py-10 text-center"
                >
                  No upstreams yet.{" "}
                  {canCreate
                    ? "Add one so the engines can resolve names."
                    : "An operator can add one."}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {editing !== null && (
        <UpstreamDialog
          upstream={editing === "new" ? null : editing}
          nextPosition={rows.length}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <DeleteDialog upstream={deleting} onClose={() => setDeleting(null)} />
      )}
    </>
  );
}

function UpstreamDialog({
  upstream,
  nextPosition,
  onClose,
}: {
  upstream: Upstream | null;
  nextPosition: number;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [form, setForm] = useState({
    name: upstream?.name ?? "",
    protocol: upstream?.protocol ?? ("udp" as Protocol),
    address: upstream?.address ?? "",
    tls_server_name: upstream?.tls_server_name ?? "",
    doh_url: upstream?.doh_url ?? "",
    timeout_ms: String(upstream?.timeout_ms ?? 250),
    enabled: upstream?.enabled ?? true,
  });
  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const save = useMutation({
    mutationFn: async () => {
      const p = form.protocol;
      const body: Schemas["UpstreamInput"] = {
        name: form.name.trim(),
        protocol: p,
        address: p === "doh" ? "" : form.address.trim(),
        tls_server_name: p === "dot" ? form.tls_server_name.trim() : "",
        doh_url: p === "doh" ? form.doh_url.trim() : "",
        timeout_ms: Number(form.timeout_ms),
        ca_certificate_pem: upstream?.ca_certificate_pem ?? "",
        position: upstream?.position ?? nextPosition,
        enabled: form.enabled,
      };
      if (upstream) {
        return unwrap(
          await api.PUT("/upstreams/{id}", {
            params: { path: { id: upstream.id } },
            body: { ...body, revision: upstream.revision },
          }),
        );
      }
      return unwrap(await api.POST("/upstreams", { body }));
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["upstreams"] });
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
            {upstream ? `Edit ${upstream.name}` : "Add upstream"}
          </DialogTitle>
          <DialogDescription>
            Changes publish a new configuration version that every engine
            applies.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          {save.error && (
            <Alert variant="destructive" data-testid="upstream-error">
              <AlertDescription>{errorMessage(save.error)}</AlertDescription>
            </Alert>
          )}
          <div className="grid gap-1.5">
            <Label htmlFor="upstream-name">Name</Label>
            <Input
              id="upstream-name"
              data-testid="upstream-name"
              required
              maxLength={64}
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="upstream-protocol">Protocol</Label>
            <Select
              value={form.protocol}
              onValueChange={(v) => set("protocol", v as Protocol)}
            >
              <SelectTrigger
                id="upstream-protocol"
                data-testid="upstream-protocol"
                className="h-9"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(Object.keys(protocolLabels) as Protocol[]).map((p) => (
                  <SelectItem
                    key={p}
                    value={p}
                    data-testid={`upstream-protocol-${p}`}
                  >
                    {protocolLabels[p]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {form.protocol === "doh" ? (
            <div className="grid gap-1.5">
              <Label htmlFor="upstream-doh-url">DoH URL</Label>
              <Input
                id="upstream-doh-url"
                data-testid="upstream-doh-url"
                className="font-mono"
                placeholder="https://dns.example.net/dns-query"
                required
                value={form.doh_url}
                onChange={(e) => set("doh_url", e.target.value)}
              />
            </div>
          ) : (
            <div className="grid gap-1.5">
              <Label htmlFor="upstream-address">Address</Label>
              <Input
                id="upstream-address"
                data-testid="upstream-address"
                className="font-mono"
                placeholder={
                  form.protocol === "dot" ? "192.0.2.10:853" : "192.0.2.10:53"
                }
                required
                value={form.address}
                onChange={(e) => set("address", e.target.value)}
              />
            </div>
          )}
          {form.protocol === "dot" && (
            <div className="grid gap-1.5">
              <Label htmlFor="upstream-tls-name">TLS server name</Label>
              <Input
                id="upstream-tls-name"
                data-testid="upstream-tls-name"
                className="font-mono"
                placeholder="dns.example.net"
                required
                value={form.tls_server_name}
                onChange={(e) => set("tls_server_name", e.target.value)}
              />
            </div>
          )}
          <div className="grid grid-cols-[1fr_auto] items-end gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="upstream-timeout">Timeout (ms)</Label>
              <Input
                id="upstream-timeout"
                data-testid="upstream-timeout"
                type="number"
                min={50}
                max={5000}
                required
                value={form.timeout_ms}
                onChange={(e) => set("timeout_ms", e.target.value)}
              />
            </div>
            <label className="flex h-9 items-center gap-2 text-sm">
              <Switch
                checked={form.enabled}
                onCheckedChange={(v) => set("enabled", v)}
                data-testid="upstream-enabled"
              />
              Enabled
            </label>
          </div>
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="upstream-save"
              disabled={save.isPending}
            >
              {save.isPending
                ? "Saving…"
                : upstream
                  ? "Save changes"
                  : "Add upstream"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DeleteDialog({
  upstream,
  onClose,
}: {
  upstream: Upstream;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const remove = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE("/upstreams/{id}", {
          params: {
            path: { id: upstream.id },
            query: { revision: upstream.revision },
          },
        }),
      ),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["upstreams"] });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete {upstream.name}?</DialogTitle>
          <DialogDescription>
            Engines stop forwarding to{" "}
            <span className="font-mono">
              {upstream.protocol === "doh"
                ? upstream.doh_url
                : upstream.address}
            </span>{" "}
            once they apply the new configuration.
          </DialogDescription>
        </DialogHeader>
        {remove.error && (
          <Alert variant="destructive">
            <AlertDescription>{errorMessage(remove.error)}</AlertDescription>
          </Alert>
        )}
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            data-testid="confirm-delete"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : "Delete upstream"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
