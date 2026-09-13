import { useState, type FormEvent } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";

import { type Schemas } from "@/api/client";
import {
  useCreateRewrite,
  useDeleteRewrite,
  usePolicyGroups,
  useRewrites,
  useUpdateRewrite,
} from "@/api/policies";
import { useCan } from "@/auth/AuthProvider";
import { ConfirmDialog, ErrorAlert, MessageRow } from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
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

type Rewrite = Schemas["Rewrite"];
type RecordType = Rewrite["type"];
type PolicyGroup = Schemas["PolicyGroup"];

const recordTypes: RecordType[] = ["A", "AAAA", "CNAME"];

const valuePlaceholders: Record<RecordType, string> = {
  A: "192.168.1.50",
  AAAA: "fd00::50",
  CNAME: "target.example",
};

/** The label a rewrite's row and buttons use: name, type and value. */
function describe(r: Rewrite): string {
  return `${r.name} ${r.type} ${r.value}`;
}

export function RewritesPage() {
  const canCreate = useCan("createRewrite");
  const canUpdate = useCan("updateRewrite");
  const canDelete = useCan("deleteRewrite");
  const [scope, setScope] = useState("all");
  const rewrites = useRewrites(scope);
  const groups = usePolicyGroups();
  const del = useDeleteRewrite();
  const [editing, setEditing] = useState<Rewrite | "new" | null>(null);
  const [deleting, setDeleting] = useState<Rewrite | null>(null);
  const rows = rewrites.data ?? [];
  const groupNames = new Map((groups.data ?? []).map((g) => [g.id, g.name]));
  const cols = 5 + (canUpdate || canDelete ? 1 : 0);

  return (
    <>
      <PageHeader
        title="Rewrites"
        description="Local answers for names: A, AAAA or CNAME records served instead of forwarding. A group's rewrites apply only to its clients."
        actions={
          canCreate && (
            <Button onClick={() => setEditing("new")}>
              <Plus className="mr-1.5 h-4 w-4" />
              New rewrite
            </Button>
          )
        }
      />
      <div className="mb-4 flex items-center gap-3">
        <Label htmlFor="rewrite-scope-filter">Scope</Label>
        <Select value={scope} onValueChange={setScope}>
          <SelectTrigger id="rewrite-scope-filter" className="h-9 w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All</SelectItem>
            <SelectItem value="global">Global</SelectItem>
            {(groups.data ?? []).map((g) => (
              <SelectItem key={g.id} value={g.id}>
                {g.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <ErrorAlert
        error={rewrites.error}
        prefix="Could not load rewrites"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Value</TableHead>
              <TableHead className="text-right">TTL</TableHead>
              <TableHead>Scope</TableHead>
              {(canUpdate || canDelete) && (
                <TableHead className="w-24 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.id}>
                <TableCell className="py-3 font-mono text-[13px]">
                  {r.name}
                </TableCell>
                <TableCell className="py-3">
                  <Badge variant="secondary">{r.type}</Badge>
                </TableCell>
                <TableCell className="py-3 font-mono text-[13px]">
                  {r.value}
                </TableCell>
                <TableCell className="py-3 text-right tabular-nums">
                  {r.ttl}
                </TableCell>
                <TableCell className="py-3">
                  {r.group_id === null ? (
                    <span className="text-muted-foreground">Global</span>
                  ) : (
                    (groupNames.get(r.group_id) ?? r.group_id.slice(0, 8))
                  )}
                </TableCell>
                {(canUpdate || canDelete) && (
                  <TableCell className="py-2 text-right whitespace-nowrap">
                    {canUpdate && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        aria-label={`Edit ${describe(r)}`}
                        onClick={() => setEditing(r)}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                    )}
                    {canDelete && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="hover:text-destructive h-8 w-8"
                        aria-label={`Delete ${describe(r)}`}
                        onClick={() => setDeleting(r)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {rewrites.isPending && (
              <MessageRow colSpan={cols}>Loading…</MessageRow>
            )}
            {rewrites.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>
                {scope === "all"
                  ? "No rewrites yet."
                  : "No rewrites in this scope."}
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>

      {editing !== null && (
        <RewriteDialog
          rewrite={editing === "new" ? null : editing}
          groups={groups.data ?? []}
          defaultScope={scope === "all" ? "global" : scope}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete rewrite"
          description={`Delete the ${deleting.type} rewrite ${deleting.name} → ${deleting.value}? Clients resolve the name normally again.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This rewrite"
          onConfirm={() =>
            del.mutateAsync({ id: deleting.id, revision: deleting.revision })
          }
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

function RewriteDialog({
  rewrite,
  groups,
  defaultScope,
  onClose,
}: {
  rewrite: Rewrite | null;
  groups: PolicyGroup[];
  defaultScope: string;
  onClose: () => void;
}) {
  const [form, setForm] = useState({
    name: rewrite?.name ?? "",
    type: rewrite?.type ?? ("A" as RecordType),
    value: rewrite?.value ?? "",
    ttl: String(rewrite?.ttl ?? 300),
    scope: rewrite ? (rewrite.group_id ?? "global") : defaultScope,
  });
  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const create = useCreateRewrite();
  const update = useUpdateRewrite();
  const save = rewrite ? update : create;

  function submit(e: FormEvent) {
    e.preventDefault();
    const body = {
      name: form.name.trim(),
      type: form.type,
      value: form.value.trim(),
      ttl: Number(form.ttl),
      group_id: form.scope === "global" ? null : form.scope,
    };
    const done = { onSuccess: onClose };
    if (rewrite) {
      update.mutate(
        { id: rewrite.id, body: { ...body, revision: rewrite.revision } },
        done,
      );
    } else {
      create.mutate(body, done);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{rewrite ? "Edit rewrite" : "New rewrite"}</DialogTitle>
          <DialogDescription>
            Changes publish a new configuration version that every engine
            applies.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-1.5">
            <Label htmlFor="rewrite-name">Name</Label>
            <Input
              id="rewrite-name"
              className="font-mono"
              placeholder="host.example or *.example"
              required
              maxLength={255}
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </div>
          <div className="grid grid-cols-[8rem_1fr] gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="rewrite-type">Type</Label>
              <Select
                value={form.type}
                onValueChange={(v) => set("type", v as RecordType)}
              >
                <SelectTrigger id="rewrite-type" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {recordTypes.map((t) => (
                    <SelectItem key={t} value={t}>
                      {t}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="rewrite-value">Value</Label>
              <Input
                id="rewrite-value"
                className="font-mono"
                placeholder={valuePlaceholders[form.type]}
                required
                maxLength={255}
                value={form.value}
                onChange={(e) => set("value", e.target.value)}
              />
            </div>
          </div>
          <div className="grid grid-cols-[8rem_1fr] gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="rewrite-ttl">TTL</Label>
              <Input
                id="rewrite-ttl"
                type="number"
                min={0}
                max={86400}
                required
                value={form.ttl}
                onChange={(e) => set("ttl", e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="rewrite-scope">Scope</Label>
              <Select value={form.scope} onValueChange={(v) => set("scope", v)}>
                <SelectTrigger id="rewrite-scope" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="global">Global</SelectItem>
                  {groups.map((g) => (
                    <SelectItem key={g.id} value={g.id}>
                      {g.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <ErrorAlert error={save.error} thing="This rewrite" />
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
