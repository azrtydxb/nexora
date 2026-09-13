import { useState, type FormEvent } from "react";
import { KeyRound, Plus } from "lucide-react";
import { Link, useNavigate } from "react-router";

import { type Schemas } from "@/api/client";
import { useCreateZone, useTsigKeys, useZones } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import { fqdn, splitList } from "@/lib/zoneRdataHints";
import {
  ErrorAlert,
  formatAgo,
  MessageRow,
  StatusDot,
} from "@/components/common";
import { EngineGroupName, EngineGroupSelect } from "@/components/fleet";
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

type Zone = Schemas["Zone"];

export function SecondaryState({ zone }: { zone: Zone }) {
  const s = zone.secondary_status;
  if (!s) return null;
  if (s.expired) return <StatusDot tone="destructive">expired</StatusDot>;
  if (s.last_error)
    return (
      <span title={s.last_error}>
        <StatusDot tone="warning">
          last success {formatAgo(s.last_success_at)}
        </StatusDot>
      </span>
    );
  return (
    <StatusDot tone={s.last_success_at ? "success" : "muted"}>
      last success {formatAgo(s.last_success_at)}
    </StatusDot>
  );
}

export function ZonesPage() {
  const canCreate = useCan("createZone");
  const canListKeys = useCan("listTsigKeys");
  const zones = useZones();
  const [creating, setCreating] = useState(false);
  const rows = zones.data ?? [];

  return (
    <>
      <PageHeader
        title="Zones"
        description="Authoritative zones the engines serve: primary zones edited here or through dynamic updates, and secondary zones transferred from their primaries."
        actions={
          <>
            {canListKeys && (
              <Link
                to="/zones/tsig-keys"
                className="border-input bg-background hover:bg-accent hover:text-accent-foreground focus-visible:ring-ring inline-flex h-10 items-center rounded-md border px-4 text-sm font-medium focus-visible:ring-2 focus-visible:outline-none"
              >
                <KeyRound className="mr-1.5 h-4 w-4" />
                TSIG keys
              </Link>
            )}
            {canCreate && (
              <Button onClick={() => setCreating(true)}>
                <Plus className="mr-1.5 h-4 w-4" />
                New zone
              </Button>
            )}
          </>
        }
      />
      <ErrorAlert
        error={zones.error}
        prefix="Could not load zones"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Zone</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead className="text-right">Serial</TableHead>
              <TableHead>DNSSEC</TableHead>
              <TableHead>Transfers</TableHead>
              <TableHead>Engine group</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((z) => (
              <TableRow key={z.id}>
                <TableCell className="py-3 font-mono text-[13px]">
                  <Link
                    to={`/zones/${z.id}`}
                    className="text-primary font-medium hover:underline"
                  >
                    {z.name}
                  </Link>
                </TableCell>
                <TableCell className="py-3">
                  <Badge variant="secondary">
                    {z.kind === "primary" ? "Primary" : "Secondary"}
                  </Badge>
                </TableCell>
                <TableCell className="py-3 text-right tabular-nums">
                  {z.serial}
                </TableCell>
                <TableCell className="py-3">
                  {z.dnssec_enabled ? (
                    <Badge variant="outline" className="border-success/40">
                      signed
                    </Badge>
                  ) : (
                    <span className="text-muted-foreground text-sm">
                      unsigned
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-3">
                  {z.kind === "secondary" ? (
                    <SecondaryState zone={z} />
                  ) : (
                    <span className="text-muted-foreground text-sm">
                      {z.transfer.allow_cidrs.length === 0
                        ? "refused"
                        : z.transfer.allow_cidrs.join(", ")}
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-3 whitespace-nowrap">
                  <EngineGroupName id={z.engine_group_id} />
                </TableCell>
              </TableRow>
            ))}
            {zones.isPending && <MessageRow colSpan={6}>Loading…</MessageRow>}
            {zones.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={6}>No zones yet.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {creating && <NewZoneDialog onClose={() => setCreating(false)} />}
    </>
  );
}

type Kind = Zone["kind"];

function NewZoneDialog({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const create = useCreateZone();
  const keys = useTsigKeys();
  const [kind, setKind] = useState<Kind>("primary");
  const [engineGroup, setEngineGroup] = useState<string | null>(null);
  const [form, setForm] = useState({
    name: "",
    default_ttl: "3600",
    mname: "",
    rname: "",
    nameservers: "",
    primaries: "",
    primary_key: "none",
  });
  const set = (key: keyof typeof form, value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    const name = fqdn(form.name);
    const body: Schemas["ZoneCreate"] =
      kind === "primary"
        ? {
            name,
            kind,
            engine_group_id: engineGroup,
            default_ttl: Number(form.default_ttl),
            soa: { mname: fqdn(form.mname), rname: fqdn(form.rname) },
            nameservers: splitList(form.nameservers).map(fqdn),
          }
        : {
            name,
            kind,
            engine_group_id: engineGroup,
            primaries: splitList(form.primaries).map((address) => ({
              address,
              tsig_key_id:
                form.primary_key === "none" ? null : form.primary_key,
            })),
          };
    create.mutate(body, {
      onSuccess: (z) => {
        onClose();
        void navigate(`/zones/${z.id}`);
      },
    });
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New zone</DialogTitle>
          <DialogDescription>
            A primary zone starts with its SOA and apex NS records; a secondary
            zone copies its primaries by zone transfer.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid grid-cols-[1fr_9rem] gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="zone-name">Zone name</Label>
              <Input
                id="zone-name"
                className="font-mono"
                placeholder="example.com."
                maxLength={255}
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="zone-kind">Kind</Label>
              <Select value={kind} onValueChange={(v) => setKind(v as Kind)}>
                <SelectTrigger id="zone-kind" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="primary">Primary</SelectItem>
                  <SelectItem value="secondary">Secondary</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="zone-engine-group">Engine group</Label>
            <EngineGroupSelect
              id="zone-engine-group"
              testId="zone-engine-group"
              value={engineGroup}
              onChange={setEngineGroup}
            />
          </div>
          {kind === "primary" ? (
            <>
              <div className="grid grid-cols-[1fr_9rem] gap-4">
                <div className="grid gap-1.5">
                  <Label htmlFor="zone-mname">Primary name server</Label>
                  <Input
                    id="zone-mname"
                    className="font-mono"
                    placeholder="ns1.example.com."
                    value={form.mname}
                    onChange={(e) => set("mname", e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="zone-ttl">Default TTL</Label>
                  <Input
                    id="zone-ttl"
                    type="number"
                    min={0}
                    value={form.default_ttl}
                    onChange={(e) => set("default_ttl", e.target.value)}
                  />
                </div>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="zone-rname">Responsible mailbox</Label>
                <Input
                  id="zone-rname"
                  className="font-mono"
                  placeholder="hostmaster.example.com."
                  value={form.rname}
                  onChange={(e) => set("rname", e.target.value)}
                />
                <p className="text-muted-foreground text-xs">
                  The SOA mailbox, with the @ written as a dot.
                </p>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="zone-ns">Name servers</Label>
                <Input
                  id="zone-ns"
                  className="font-mono"
                  placeholder="ns1.example.com., ns2.example.com."
                  value={form.nameservers}
                  onChange={(e) => set("nameservers", e.target.value)}
                />
                <p className="text-muted-foreground text-xs">
                  Apex NS targets, separated by commas.
                </p>
              </div>
            </>
          ) : (
            <div className="grid grid-cols-[1fr_11rem] gap-4">
              <div className="grid gap-1.5">
                <Label htmlFor="zone-primaries">Primaries</Label>
                <Input
                  id="zone-primaries"
                  className="font-mono"
                  placeholder="192.0.2.53:53"
                  value={form.primaries}
                  onChange={(e) => set("primaries", e.target.value)}
                />
                <p className="text-muted-foreground text-xs">
                  ip:port, separated by commas.
                </p>
              </div>
              <div className="grid content-start gap-1.5">
                <Label htmlFor="zone-primary-key">Transfer TSIG key</Label>
                <Select
                  value={form.primary_key}
                  onValueChange={(v) => set("primary_key", v)}
                >
                  <SelectTrigger id="zone-primary-key" className="h-9">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">None</SelectItem>
                    {(keys.data ?? []).map((k) => (
                      <SelectItem key={k.id} value={k.id}>
                        {k.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
          )}
          <ErrorAlert error={create.error} />
          <DialogFooter className="gap-2 pt-2">
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
