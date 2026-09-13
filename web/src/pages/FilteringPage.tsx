import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, RefreshCw, Trash2, X } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  Fact,
  formatAgo,
  formatDateTime,
  formatSeconds,
  MessageRow,
  StatusDot,
  useRevealRef,
} from "@/components/common";
import { EngineGroupName, EngineGroupSelect } from "@/components/fleet";
import { PageHeader } from "@/components/layout/AppShell";
import { ListEditor } from "@/components/ListEditor";
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
import { cn } from "@/lib/utils";

type FilterList = Schemas["FilterList"];
type Kind = FilterList["kind"];

const domainRE = /^[a-z0-9_-]+(\.[a-z0-9_-]+)*$/;
const integer = new Intl.NumberFormat();

function entries(n: number): string {
  return `${integer.format(n)} ${n === 1 ? "entry" : "entries"}`;
}

export function FilteringPage() {
  const canCreate = useCan("createFilterList");
  const canUpdateAllowlist = useCan("updateAllowlist");
  const qc = useQueryClient();
  const lists = useQuery({
    queryKey: ["filter-lists"],
    queryFn: async () => unwrap(await api.GET("/filter-lists")),
  });
  const allowlist = useQuery({
    queryKey: ["allowlist"],
    queryFn: async () => unwrap(await api.GET("/allowlist")),
  });
  const [openId, setOpenId] = useState<string | null>(null);
  const [editing, setEditing] = useState<FilterList | "new" | null>(null);
  const rows = lists.data ?? [];

  return (
    <>
      <PageHeader
        title="Filtering"
        description="Blocklist subscriptions the management plane fetches and ships to every engine, and domains that are always allowed."
        actions={
          canCreate && (
            <Button data-testid="list-add" onClick={() => setEditing("new")}>
              <Plus className="mr-1.5 h-4 w-4" />
              Add list
            </Button>
          )
        }
      />
      <ErrorAlert
        error={lists.error}
        prefix="Could not load filter lists"
        className="mb-4"
      />

      <section aria-labelledby="lists-heading" className="mb-8">
        <h2 id="lists-heading" className="mb-3 text-sm font-semibold">
          Subscriptions
        </h2>
        <Card className="overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-10">Name</TableHead>
                <TableHead className="h-10">Kind</TableHead>
                <TableHead className="h-10">Source</TableHead>
                <TableHead className="h-10">Engine group</TableHead>
                <TableHead className="h-10">Refresh</TableHead>
                <TableHead className="h-10 text-right">Entries</TableHead>
                <TableHead className="h-10">Last fetched</TableHead>
                <TableHead className="h-10 w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((l) => (
                <TableRow
                  key={l.id}
                  data-testid={`list-row-${l.name}`}
                  data-state={openId === l.id ? "selected" : undefined}
                >
                  <TableCell className="py-2.5 font-medium whitespace-nowrap">
                    {l.name}
                    {!l.enabled && (
                      <Badge variant="outline" className="ml-2 font-normal">
                        disabled
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="py-2.5">
                    <KindBadge kind={l.kind} />
                  </TableCell>
                  <TableCell
                    className="max-w-[22rem] truncate py-2.5 font-mono text-[13px]"
                    title={l.url}
                  >
                    {l.url}
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    <EngineGroupName id={l.engine_group_id} />
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap tabular-nums">
                    {formatSeconds(l.refresh_interval_seconds)}
                    <span className="text-muted-foreground ml-1.5 text-xs">
                      ({l.refresh_interval_seconds} s)
                    </span>
                  </TableCell>
                  <TableCell className="py-2.5 text-right whitespace-nowrap tabular-nums">
                    {entries(l.entry_count)}
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    <span title={formatDateTime(l.last_success_at)}>
                      {formatAgo(l.last_success_at)}
                    </span>
                    {l.stale && (
                      <Badge
                        data-testid={`list-stale-${l.name}`}
                        className="border-warning/40 text-warning ml-2 bg-transparent font-medium"
                        variant="outline"
                        title={
                          l.last_error || "Not refreshed within two intervals"
                        }
                      >
                        stale
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="py-2 text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-8"
                      data-testid={`list-open-${l.name}`}
                      onClick={() => setOpenId(l.id)}
                    >
                      Details
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
              {lists.isPending && <MessageRow colSpan={8}>Loading…</MessageRow>}
              {lists.isSuccess && rows.length === 0 && (
                <MessageRow colSpan={8}>
                  No subscriptions yet.{" "}
                  {canCreate
                    ? "Add a blocklist URL to start filtering."
                    : "An operator can add one."}
                </MessageRow>
              )}
            </TableBody>
          </Table>
        </Card>
        {openId && (
          <ListDetail
            id={openId}
            onClose={() => setOpenId(null)}
            onEdit={(l) => setEditing(l)}
          />
        )}
      </section>

      <section aria-labelledby="allowlist-heading">
        <h2 id="allowlist-heading" className="mb-1 text-sm font-semibold">
          Allowlist
        </h2>
        <p className="text-muted-foreground mb-3 text-sm">
          Domains that are never blocked, whatever the lists say.
        </p>
        <ErrorAlert
          error={allowlist.error}
          prefix="Could not load the allowlist"
          className="mb-3"
        />
        <Card className="max-w-3xl overflow-hidden">
          <ListEditor
            prefix="allowlist"
            items={allowlist.data?.domains}
            loading={allowlist.isPending}
            canEdit={canUpdateAllowlist}
            inputLabel="Domain"
            placeholder="intranet.example.com"
            addLabel="Add domain"
            columnLabel="Domain"
            emptyText="No domains are allowlisted."
            savedText="Allowlist saved"
            thing="The allowlist"
            normalize={(v) => v.trim().toLowerCase().replace(/\.$/, "")}
            validate={(v) =>
              v.length <= 253 && domainRE.test(v)
                ? null
                : "Enter a domain name such as example.com"
            }
            onSave={async (domains) => {
              const saved = unwrap(
                await api.PUT("/allowlist", {
                  body: { domains, revision: allowlist.data!.revision },
                }),
              );
              qc.setQueryData(["allowlist"], saved);
            }}
          />
        </Card>
      </section>

      {editing !== null && (
        <ListDialog
          list={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onCreated={(l) => setOpenId(l.id)}
        />
      )}
    </>
  );
}

function KindBadge({ kind }: { kind: Kind }) {
  return (
    <Badge variant={kind === "block" ? "secondary" : "outline"}>
      {kind === "block" ? "Block" : "Allow"}
    </Badge>
  );
}

function ListDetail({
  id,
  onClose,
  onEdit,
}: {
  id: string;
  onClose: () => void;
  onEdit: (l: FilterList) => void;
}) {
  const qc = useQueryClient();
  const canUpdate = useCan("updateFilterList");
  const canRefresh = useCan("refreshFilterList");
  const canDelete = useCan("deleteFilterList");
  const [deleting, setDeleting] = useState(false);
  const reveal = useRevealRef<HTMLDivElement>(id);
  const q = useQuery({
    queryKey: ["filter-lists", id],
    queryFn: async () =>
      unwrap(await api.GET("/filter-lists/{id}", { params: { path: { id } } })),
  });
  const refresh = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/filter-lists/{id}/refresh", {
          params: { path: { id } },
        }),
      ),
    onSuccess: async (l) => {
      qc.setQueryData(["filter-lists", id], l);
      await qc.invalidateQueries({ queryKey: ["filter-lists"], exact: true });
    },
  });
  const l = q.data;

  return (
    <Card
      ref={reveal}
      data-testid="list-detail"
      className="mt-4 overflow-hidden"
      aria-label={l ? `Filter list ${l.name}` : "Filter list"}
    >
      <div className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h3 className="text-base font-semibold">{l?.name ?? "…"}</h3>
            {l && <KindBadge kind={l.kind} />}
            {l &&
              (l.stale ? (
                <StatusDot tone="warning">stale</StatusDot>
              ) : (
                <StatusDot tone="success">fresh</StatusDot>
              ))}
          </div>
          {l && (
            <p className="text-muted-foreground mt-1 font-mono text-xs break-all">
              {l.url}
            </p>
          )}
        </div>
        <div className="flex items-center gap-1.5">
          {l && canRefresh && (
            <Button
              variant="outline"
              size="sm"
              data-testid="list-refresh"
              disabled={refresh.isPending}
              onClick={() => refresh.mutate()}
            >
              <RefreshCw
                className={cn(
                  "mr-1.5 h-4 w-4",
                  refresh.isPending && "animate-spin",
                )}
              />
              {refresh.isPending ? "Refreshing…" : "Refresh now"}
            </Button>
          )}
          {l && canUpdate && (
            <Button
              variant="outline"
              size="sm"
              data-testid="list-edit"
              onClick={() => onEdit(l)}
            >
              <Pencil className="mr-1.5 h-4 w-4" />
              Edit
            </Button>
          )}
          {l && canDelete && (
            <Button
              variant="outline"
              size="sm"
              className="hover:text-destructive"
              data-testid="list-delete"
              onClick={() => setDeleting(true)}
            >
              <Trash2 className="mr-1.5 h-4 w-4" />
              Delete
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
        <ErrorAlert error={q.error} prefix="Could not load the list" />
        <ErrorAlert
          error={refresh.error}
          prefix="Refresh failed"
          className="mb-3"
        />
        {q.isPending && (
          <p className="text-muted-foreground text-sm">Loading…</p>
        )}
        {l && (
          <dl className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            <Fact label="Entries">{entries(l.entry_count)}</Fact>
            <Fact label="Invalid lines">
              {integer.format(l.invalid_line_count)}
            </Fact>
            <Fact label="Refresh interval">
              {formatSeconds(l.refresh_interval_seconds)} (
              {l.refresh_interval_seconds} s)
            </Fact>
            <Fact label="State">{l.enabled ? "Enabled" : "Disabled"}</Fact>
            <Fact label="Last success">
              {formatDateTime(l.last_success_at)}
            </Fact>
            <Fact label="Last attempt">
              {formatDateTime(l.last_attempt_at)}
            </Fact>
            <Fact label="Content hash" className="sm:col-span-2">
              <span className="font-mono text-xs break-all">
                {l.current_blob_sha256 ?? "—"}
              </span>
            </Fact>
            {l.last_error && (
              <Fact label="Last error" className="sm:col-span-2 lg:col-span-4">
                <span className="text-destructive">{l.last_error}</span>
              </Fact>
            )}
          </dl>
        )}
      </div>
      {deleting && l && (
        <ConfirmDialog
          title={`Delete ${l.name}?`}
          description="Engines stop applying its entries once they receive the new configuration."
          confirmLabel="Delete list"
          pendingLabel="Deleting…"
          thing="This list"
          onConfirm={async () => {
            unwrap(
              await api.DELETE("/filter-lists/{id}", {
                params: { path: { id }, query: { revision: l.revision } },
              }),
            );
            onClose();
            qc.removeQueries({ queryKey: ["filter-lists", id] });
            await qc.invalidateQueries({ queryKey: ["filter-lists"] });
          }}
          onClose={() => setDeleting(false)}
        />
      )}
    </Card>
  );
}

function ListDialog({
  list,
  onClose,
  onCreated,
}: {
  list: FilterList | null;
  onClose: () => void;
  onCreated: (l: FilterList) => void;
}) {
  const qc = useQueryClient();
  const [form, setForm] = useState({
    name: list?.name ?? "",
    kind: list?.kind ?? ("block" as Kind),
    url: list?.url ?? "",
    interval: String(list?.refresh_interval_seconds ?? 86400),
    enabled: list?.enabled ?? true,
    engine_group_id: list?.engine_group_id ?? null,
  });
  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const save = useMutation({
    mutationFn: async () => {
      const body: Schemas["FilterListInput"] = {
        name: form.name.trim(),
        kind: form.kind,
        url: form.url.trim(),
        refresh_interval_seconds: Number(form.interval),
        enabled: form.enabled,
        engine_group_id: form.engine_group_id,
      };
      if (list) {
        return unwrap(
          await api.PUT("/filter-lists/{id}", {
            params: { path: { id: list.id } },
            body: { ...body, revision: list.revision },
          }),
        );
      }
      return unwrap(await api.POST("/filter-lists", { body }));
    },
    onSuccess: async (saved) => {
      qc.setQueryData(["filter-lists", saved.id], saved);
      await qc.invalidateQueries({ queryKey: ["filter-lists"], exact: true });
      if (!list) onCreated(saved);
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
            {list ? `Edit ${list.name}` : "Add filter list"}
          </DialogTitle>
          <DialogDescription>
            The list is fetched now and then on every refresh interval; one
            domain or hosts-file entry per line.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <ErrorAlert error={save.error} thing="This list" />
          <div className="grid grid-cols-[1fr_9rem] gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="list-name">Name</Label>
              <Input
                id="list-name"
                data-testid="list-name"
                required
                maxLength={64}
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="list-kind">Kind</Label>
              <Select
                value={form.kind}
                onValueChange={(v) => set("kind", v as Kind)}
              >
                <SelectTrigger
                  id="list-kind"
                  data-testid="list-kind"
                  className="h-9"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="block">Block</SelectItem>
                  <SelectItem value="allow">Allow</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="list-url">URL</Label>
            <Input
              id="list-url"
              data-testid="list-url"
              type="url"
              className="font-mono"
              placeholder="https://lists.example.net/blocklist.txt"
              required
              pattern="https?://.+"
              value={form.url}
              onChange={(e) => set("url", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="list-engine-group">Engine group</Label>
            <EngineGroupSelect
              id="list-engine-group"
              testId="list-engine-group"
              value={form.engine_group_id}
              onChange={(v) => set("engine_group_id", v)}
            />
          </div>
          <div className="grid grid-cols-[1fr_auto] items-end gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="list-interval">Refresh interval (seconds)</Label>
              <Input
                id="list-interval"
                data-testid="list-interval"
                type="number"
                min={300}
                step={1}
                required
                aria-describedby="list-interval-hint"
                value={form.interval}
                onChange={(e) => set("interval", e.target.value)}
              />
              <p
                id="list-interval-hint"
                className="text-muted-foreground text-xs"
              >
                At least 300 seconds.
                {Number(form.interval) >= 300 &&
                  ` Every ${formatSeconds(Number(form.interval))}.`}
              </p>
            </div>
            <label className="flex h-9 items-center gap-2 self-start pt-6 text-sm">
              <Switch
                checked={form.enabled}
                onCheckedChange={(v) => set("enabled", v)}
                data-testid="list-enabled"
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
              data-testid="list-save"
              disabled={save.isPending}
            >
              {save.isPending ? "Saving…" : list ? "Save changes" : "Add list"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
