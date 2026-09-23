import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import type { Schemas } from "@/api/client";
import {
  useCatalogZone,
  useCatalogZones,
  useCreateCatalogZone,
  useDeleteCatalogZone,
} from "@/api/m8";
import { useTsigKeys } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  MessageRow,
  formatDateTime,
} from "@/components/common";
import { EngineGroupSelect } from "@/components/fleet";
import { HelpTip } from "@/components/HelpTip";
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
import { splitList } from "@/lib/zoneRdataHints";

export function CatalogZonesPage() {
  const [params, setParams] = useSearchParams();
  const selected = params.get("catalog") ?? "";
  const list = useCatalogZones();
  const detail = useCatalogZone(selected);
  const canCreate = useCan("createCatalogZone");
  const canDelete = useCan("deleteCatalogZone");
  const del = useDeleteCatalogZone();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<Schemas["CatalogZone"] | null>(null);
  return (
    <>
      <PageHeader
        title="Catalog zones"
        description="Publish member zones to secondaries or consume a remote catalog."
        actions={
          canCreate && (
            <Button
              data-testid="catalog-create"
              onClick={() => setCreating(true)}
            >
              New catalog zone
            </Button>
          )
        }
      />
      <ErrorAlert error={list.error} prefix="Could not load catalogs" />
      <Card className="min-w-0 overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Members</TableHead>
              <TableHead>Problem</TableHead>
              {canDelete && <TableHead>Actions</TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.data?.map((c) => (
              <TableRow key={c.id}>
                <TableCell>
                  <Link className="underline" to={`?catalog=${c.id}`}>
                    {c.name}
                  </Link>
                </TableCell>
                <TableCell>
                  {c.role === "producer" ? "Producer" : "Consumer"}
                </TableCell>
                <TableCell>{c.members.length}</TableCell>
                <TableCell className="text-destructive">
                  {c.broken_reason || "—"}
                </TableCell>
                {canDelete && (
                  <TableCell>
                    <Button variant="outline" onClick={() => setDeleting(c)}>
                      Delete
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {list.isPending && <MessageRow colSpan={5}>Loading…</MessageRow>}
            {list.isSuccess && !list.data.length && (
              <MessageRow colSpan={5}>No catalog zones.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {selected && (
        <section aria-label="Catalog details" className="mt-6 grid gap-3">
          <ErrorAlert error={detail.error} prefix="Could not load catalog" />
          {detail.isPending && <p>Loading catalog…</p>}
          {detail.data && (
            <>
              <h2 className="text-lg font-semibold break-all">
                {detail.data.name}
              </h2>
              <Link to={`/zones/${detail.data.zone_id}`} className="underline">
                Zone transfer settings
              </Link>
              <ErrorAlert
                error={detail.data.broken_reason}
                prefix="Catalog is broken"
              />
              {detail.data.role === "consumer" && (
                <p className="text-muted-foreground text-sm">
                  An empty catalog deletes every member zone this catalog
                  created. Deleting the catalog here preserves its members as
                  ordinary secondaries.
                </p>
              )}
              <p className="text-muted-foreground text-sm">
                Processed serial:{" "}
                {detail.data.processed_serial ?? "Not processed"} ·{" "}
                {detail.data.processed_at
                  ? formatDateTime(detail.data.processed_at)
                  : "No reconciliation yet"}
              </p>
              <Card className="min-w-0 overflow-hidden">
                <Table data-testid="catalog-members">
                  <TableHeader>
                    <TableRow>
                      <TableHead>Zone</TableHead>
                      <TableHead>Label</TableHead>
                      <TableHead>State</TableHead>
                      <TableHead>Issue</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {detail.data.members.map((m) => (
                      <TableRow key={`${m.label}:${m.name}`}>
                        <TableCell>
                          {m.zone_id ? (
                            <Link
                              className="underline"
                              to={`/zones/${m.zone_id}`}
                            >
                              {m.name}
                            </Link>
                          ) : (
                            m.name
                          )}
                        </TableCell>
                        <TableCell>{m.label}</TableCell>
                        <TableCell>
                          {m.state === "clash" ? "Clash" : "Configured"}
                        </TableCell>
                        <TableCell>{m.issue || "—"}</TableCell>
                      </TableRow>
                    ))}
                    {!detail.data.members.length && (
                      <MessageRow colSpan={4}>No members.</MessageRow>
                    )}
                  </TableBody>
                </Table>
              </Card>
            </>
          )}
        </section>
      )}
      {creating && <CreateCatalog onClose={() => setCreating(false)} />}
      {deleting && (
        <ConfirmDialog
          title="Delete catalog zone"
          description={
            deleting.role === "consumer"
              ? `Delete ${deleting.name}? Member zones stay as ordinary secondaries.`
              : `Delete ${deleting.name}? Member zones remain, but this catalog stops publishing them.`
          }
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await del.mutateAsync(deleting.id);
            if (selected === deleting.id) {
              const next = new URLSearchParams(params);
              next.delete("catalog");
              setParams(next);
            }
          }}
        />
      )}
    </>
  );
}

function CreateCatalog({ onClose }: { onClose: () => void }) {
  const create = useCreateCatalogZone();
  const keys = useTsigKeys();
  const [name, setName] = useState("");
  const [role, setRole] = useState<"producer" | "consumer">("producer");
  const [group, setGroup] = useState<string | null>(null);
  const [allow, setAllow] = useState("");
  const [primary, setPrimary] = useState("");
  const [key, setKey] = useState("none");
  const [error, setError] = useState("");
  function submit(e: FormEvent) {
    e.preventDefault();
    if (
      !name.trim() ||
      (role === "producer" ? !splitList(allow).length : !primary.trim())
    ) {
      setError(
        role === "producer"
          ? "Enter a catalog name and at least one transfer CIDR"
          : "Enter a catalog name and primary address",
      );
      return;
    }
    setError("");
    const tsig_key_id = key === "none" ? null : key;
    create.mutate(
      {
        name: name.trim(),
        role,
        engine_group_id: group,
        ...(role === "producer"
          ? { transfer: { allow_cidrs: splitList(allow), tsig_key_id } }
          : { primaries: [{ address: primary.trim(), tsig_key_id }] }),
      },
      { onSuccess: onClose },
    );
  }
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>New catalog zone</DialogTitle>
          <DialogDescription>
            Producers publish members; consumers reconcile zones from a remote
            catalog.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <fieldset disabled={create.isPending} className="grid gap-4">
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="catalog-name">Catalog zone name</Label>
                <HelpTip id="catalog-name" label="Catalog zone name" />
              </div>
              <Input
                id="catalog-name"
                value={name}
                maxLength={255}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="catalog-role">Role</Label>
                <HelpTip id="catalog-role" label="Role" />
              </div>
              <Select
                value={role}
                onValueChange={(v) => setRole(v as typeof role)}
              >
                <SelectTrigger id="catalog-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="producer">Producer</SelectItem>
                  <SelectItem value="consumer">Consumer</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="catalog-group">Engine group</Label>
                <HelpTip id="catalog-group" label="Engine group" />
              </div>
              <EngineGroupSelect
                testId="catalog-group"
                id="catalog-group"
                value={group}
                onChange={setGroup}
              />
            </div>
            {role === "producer" ? (
              <div className="grid gap-1.5">
                <div className="flex gap-1.5">
                  <Label htmlFor="catalog-allow">Transfer allowed from</Label>
                  <HelpTip id="catalog-allow" label="Transfer allowed from" />
                </div>
                <Input
                  id="catalog-allow"
                  value={allow}
                  onChange={(e) => setAllow(e.target.value)}
                  placeholder="192.0.2.0/24"
                />
              </div>
            ) : (
              <div className="grid gap-1.5">
                <div className="flex gap-1.5">
                  <Label htmlFor="catalog-primary">Primary address</Label>
                  <HelpTip id="catalog-primary" label="Primary address" />
                </div>
                <Input
                  id="catalog-primary"
                  value={primary}
                  onChange={(e) => setPrimary(e.target.value)}
                  placeholder="192.0.2.53:53"
                />
              </div>
            )}
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="catalog-tsig">TSIG key</Label>
                <HelpTip id="catalog-tsig" label="TSIG key" />
              </div>
              <Select
                value={key}
                onValueChange={setKey}
                disabled={!keys.isSuccess}
              >
                <SelectTrigger id="catalog-tsig">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">None</SelectItem>
                  {keys.data?.map((k) => (
                    <SelectItem key={k.id} value={k.id}>
                      {k.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </fieldset>
          <ErrorAlert error={keys.error} prefix="Could not load TSIG keys" />
          <ErrorAlert error={error || create.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
