import { useState, type FormEvent } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";

import { type Schemas } from "@/api/client";
import {
  useCreateForwardZone,
  useDeleteForwardZone,
  useForwardZones,
  useUpdateForwardZone,
} from "@/api/resolution";
import { useCan } from "@/auth/AuthProvider";
import { ConfirmDialog, ErrorAlert, MessageRow } from "@/components/common";
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
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { splitList } from "@/pages/ResolutionSection";

type ForwardZone = Schemas["ForwardZone"];

export function ForwardZonesSection() {
  const canCreate = useCan("createForwardZone");
  const canUpdate = useCan("updateForwardZone");
  const canDelete = useCan("deleteForwardZone");
  const zones = useForwardZones();
  const del = useDeleteForwardZone();
  const [editing, setEditing] = useState<ForwardZone | "new" | null>(null);
  const [deleting, setDeleting] = useState<ForwardZone | null>(null);
  const rows = zones.data ?? [];
  const cols = 3 + (canUpdate || canDelete ? 1 : 0);

  return (
    <section aria-label="Forward zones" className="mb-10">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="mb-1 text-sm font-semibold">Forward zones</h2>
          <p className="text-muted-foreground max-w-prose text-sm">
            Domains sent to specific servers in either mode, for example an
            internal corporate zone.
          </p>
        </div>
        {canCreate && (
          <Button variant="outline" onClick={() => setEditing("new")}>
            <Plus className="mr-1.5 h-4 w-4" />
            New forward zone
          </Button>
        )}
      </div>
      <ErrorAlert
        error={zones.error}
        prefix="Could not load forward zones"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Domain</TableHead>
              <TableHead>Servers</TableHead>
              <TableHead>DNSSEC</TableHead>
              {(canUpdate || canDelete) && (
                <TableHead className="w-24 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((z) => (
              <TableRow key={z.id}>
                <TableCell className="py-3 font-mono text-[13px]">
                  {z.domain}
                </TableCell>
                <TableCell className="py-3 font-mono text-[13px]">
                  {z.addresses.join(", ")}
                </TableCell>
                <TableCell className="py-3">
                  {z.validate ? (
                    <Badge variant="secondary">validated</Badge>
                  ) : (
                    <span className="text-muted-foreground">Off</span>
                  )}
                </TableCell>
                {(canUpdate || canDelete) && (
                  <TableCell className="py-2 text-right whitespace-nowrap">
                    {canUpdate && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        aria-label={`Edit ${z.domain}`}
                        onClick={() => setEditing(z)}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                    )}
                    {canDelete && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="hover:text-destructive h-8 w-8"
                        aria-label={`Delete ${z.domain}`}
                        onClick={() => setDeleting(z)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {zones.isPending && (
              <MessageRow colSpan={cols}>Loading…</MessageRow>
            )}
            {zones.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>No forward zones.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>

      {editing !== null && (
        <ForwardZoneDialog
          zone={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete forward zone"
          description={`Delete the forward zone ${deleting.domain}? Its names resolve through the resolution mode again.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This forward zone"
          onConfirm={() =>
            del.mutateAsync({ id: deleting.id, revision: deleting.revision })
          }
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

function ForwardZoneDialog({
  zone,
  onClose,
}: {
  zone: ForwardZone | null;
  onClose: () => void;
}) {
  const [form, setForm] = useState({
    domain: zone?.domain ?? "",
    addresses: zone?.addresses.join(", ") ?? "",
    validate: zone?.validate ?? false,
  });
  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const create = useCreateForwardZone();
  const update = useUpdateForwardZone();
  const save = zone ? update : create;

  function submit(e: FormEvent) {
    e.preventDefault();
    const body = {
      domain: form.domain.trim(),
      addresses: splitList(form.addresses),
      validate: form.validate,
    };
    const done = { onSuccess: onClose };
    if (zone) {
      update.mutate(
        { id: zone.id, body: { ...body, revision: zone.revision } },
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
          <DialogTitle>
            {zone ? "Edit forward zone" : "New forward zone"}
          </DialogTitle>
          <DialogDescription>
            Changes publish a new configuration version that every engine
            applies.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-1.5">
            <Label htmlFor="forward-zone-domain">Domain</Label>
            <Input
              id="forward-zone-domain"
              className="font-mono"
              placeholder="corp.example"
              required
              maxLength={255}
              value={form.domain}
              onChange={(e) => set("domain", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="forward-zone-servers">Servers</Label>
            <Input
              id="forward-zone-servers"
              className="font-mono"
              placeholder="10.0.0.53:53, 10.0.1.53:53"
              required
              value={form.addresses}
              onChange={(e) => set("addresses", e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              Comma-separated ip:port, tried in order
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch
              id="forward-zone-validate"
              checked={form.validate}
              onCheckedChange={(v) => set("validate", v)}
            />
            <Label htmlFor="forward-zone-validate">Validate DNSSEC</Label>
          </div>
          <ErrorAlert error={save.error} thing="This forward zone" />
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
