import { useState, type FormEvent, type ReactNode } from "react";
import { RotateCw } from "lucide-react";

import { type Schemas } from "@/api/client";
import {
  useConfirmKskDs,
  useStartKeyRollover,
  useUpdateZoneDnssec,
  useZoneDnssec,
} from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatDateTime,
  MessageRow,
  SavedNote,
  SecretValue,
  StatusDot,
} from "@/components/common";
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
type Dnssec = Schemas["ZoneDnssec"];
type Key = Schemas["ZoneDnssecKey"];

const algorithms: Record<number, string> = {
  13: "ECDSA P-256 SHA-256 (13)",
  8: "RSA SHA-256 (8)",
};

export function ZoneDnssecTab({ zone }: { zone: Zone }) {
  const dnssec = useZoneDnssec(zone.id);
  const d = dnssec.data;
  return (
    <div className="grid gap-8">
      <ErrorAlert error={dnssec.error} prefix="Could not load DNSSEC state" />
      {dnssec.isPending && (
        <Card className="text-muted-foreground p-5 text-sm">Loading…</Card>
      )}
      {d && (
        <>
          <SettingsForm key={String(d.enabled)} zone={zone} dnssec={d} />
          {(d.enabled || d.keys.length > 0) && (
            <>
              <KeysSection zone={zone} dnssec={d} />
              <DsSection zone={zone} dnssec={d} />
            </>
          )}
        </>
      )}
    </div>
  );
}

function SectionHeader({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h2 className="mb-1 text-sm font-semibold">{title}</h2>
        <p className="text-muted-foreground max-w-prose text-sm">
          {description}
        </p>
      </div>
      {action}
    </div>
  );
}

type Form = {
  algorithm: string;
  nsec_mode: Dnssec["nsec_mode"];
  key_backend: string;
  propagation_delay_seconds: string;
  parent_ds_ttl_seconds: string;
  zsk_lifetime_days: string;
};

const toForm = (d: Dnssec): Form => ({
  algorithm: String(d.algorithm),
  nsec_mode: d.nsec_mode,
  key_backend: d.key_backend || "default",
  propagation_delay_seconds: String(d.propagation_delay_seconds),
  parent_ds_ttl_seconds: String(d.parent_ds_ttl_seconds),
  zsk_lifetime_days: String(d.zsk_lifetime_days),
});

function SettingsForm({ zone, dnssec }: { zone: Zone; dnssec: Dnssec }) {
  const canUpdate = useCan("updateZoneDnssec") && zone.kind === "primary";
  const [form, setForm] = useState(() => toForm(dnssec));
  const update = useUpdateZoneDnssec(zone.id);
  const [disabling, setDisabling] = useState(false);
  const enabled = dnssec.enabled;
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(dnssec));
  const set = <K extends keyof Form>(key: K, value: Form[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  function body(enable: boolean): Schemas["ZoneDnssecUpdate"] {
    return {
      revision: zone.revision,
      enabled: enable,
      ...(enabled ? {} : { algorithm: Number(form.algorithm) as 8 | 13 }),
      nsec_mode: form.nsec_mode,
      ...(form.key_backend === "default"
        ? {}
        : { key_backend: form.key_backend as "kek" | "pkcs11" }),
      propagation_delay_seconds: Number(form.propagation_delay_seconds),
      parent_ds_ttl_seconds: Number(form.parent_ds_ttl_seconds),
      zsk_lifetime_days: Number(form.zsk_lifetime_days),
    };
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    update.mutate(body(true), { onSuccess: (v) => setForm(toForm(v)) });
  }

  const number = (
    id: keyof Form,
    label: string,
    help: string,
    min: number,
    max: number,
  ) => (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={`zone-dnssec-${id}`}>{label}</Label>
      <Input
        id={`zone-dnssec-${id}`}
        type="number"
        min={min}
        max={max}
        value={form[id]}
        onChange={(e) => set(id, e.target.value)}
      />
      <p className="text-muted-foreground text-xs">{help}</p>
    </div>
  );

  return (
    <section aria-label="Signing">
      <SectionHeader
        title="Online signing"
        description={
          enabled
            ? "The management plane signs every change and rolls keys on schedule; engines serve the signed zone."
            : "Signing generates a KSK and a ZSK and signs the zone. Publish the DS record at the parent afterwards to make the chain of trust."
        }
        action={
          enabled ? (
            <StatusDot tone="success">signing</StatusDot>
          ) : (
            <StatusDot tone="muted">not signed</StatusDot>
          )
        }
      />
      <form onSubmit={submit}>
        <Card className="overflow-hidden">
          <fieldset
            disabled={!canUpdate || update.isPending}
            className="grid gap-5 px-5 py-5 md:grid-cols-3"
          >
            <div className="grid content-start gap-1.5">
              <Label htmlFor="zone-dnssec-algorithm">Algorithm</Label>
              <Select
                value={form.algorithm}
                disabled={enabled || !canUpdate}
                onValueChange={(v) => set("algorithm", v)}
              >
                <SelectTrigger id="zone-dnssec-algorithm" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="13">{algorithms[13]}</SelectItem>
                  <SelectItem value="8">{algorithms[8]}</SelectItem>
                </SelectContent>
              </Select>
              {enabled && (
                <p className="text-muted-foreground text-xs">
                  Fixed while the zone is signed.
                </p>
              )}
            </div>
            <div className="grid content-start gap-1.5">
              <Label htmlFor="zone-dnssec-nsec">Denial of existence</Label>
              <Select
                value={form.nsec_mode}
                disabled={!canUpdate}
                onValueChange={(v) => set("nsec_mode", v as Form["nsec_mode"])}
              >
                <SelectTrigger id="zone-dnssec-nsec" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="nsec3">NSEC3</SelectItem>
                  <SelectItem value="nsec">NSEC</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid content-start gap-1.5">
              <Label htmlFor="zone-dnssec-backend">Key storage</Label>
              <Select
                value={form.key_backend}
                disabled={enabled || !canUpdate}
                onValueChange={(v) => set("key_backend", v)}
              >
                <SelectTrigger id="zone-dnssec-backend" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="default">
                    Default (PKCS#11 when configured)
                  </SelectItem>
                  <SelectItem value="kek">
                    KEK-encrypted in the database
                  </SelectItem>
                  <SelectItem value="pkcs11">PKCS#11 token</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {number(
              "propagation_delay_seconds",
              "Propagation delay (seconds)",
              "How long a change takes to reach every engine and cache.",
              1,
              604800,
            )}
            {number(
              "parent_ds_ttl_seconds",
              "Parent DS TTL (seconds)",
              "The TTL of the DS record at the parent.",
              1,
              604800,
            )}
            {number(
              "zsk_lifetime_days",
              "ZSK lifetime (days)",
              "ZSKs roll automatically after this; 0 rolls them manually only.",
              0,
              3650,
            )}
          </fieldset>
          <div className="bg-muted/40 flex flex-wrap items-center justify-end gap-3 border-t px-5 py-3">
            {update.error ? (
              <ErrorAlert
                error={update.error}
                thing="The zone"
                className="mr-auto w-auto flex-1 py-2"
              />
            ) : (
              <span className="text-muted-foreground mr-auto text-sm">
                {zone.kind === "secondary" ? (
                  "Secondary zones are served as transferred from their primaries."
                ) : !canUpdate ? (
                  "Operators and administrators can change signing."
                ) : enabled && dirty ? (
                  "Unsaved changes"
                ) : (
                  <SavedNote show={enabled && update.isSuccess}>
                    Signing settings saved
                  </SavedNote>
                )}
              </span>
            )}
            {canUpdate && enabled && (
              <>
                <Button
                  type="button"
                  variant="outline"
                  className="hover:text-destructive"
                  onClick={() => setDisabling(true)}
                >
                  Disable signing
                </Button>
                <Button type="submit" disabled={!dirty || update.isPending}>
                  Save signing settings
                </Button>
              </>
            )}
            {canUpdate && !enabled && (
              <Button type="submit" disabled={update.isPending}>
                {update.isPending ? "Generating keys…" : "Enable signing"}
              </Button>
            )}
          </div>
        </Card>
      </form>
      {disabling && (
        <ConfirmDialog
          title="Disable signing"
          description={`Serve ${zone.name} unsigned? Validating resolvers treat the zone as bogus while the parent still publishes its DS: remove the DS at the parent first.`}
          confirmLabel="Disable signing"
          pendingLabel="Disabling…"
          thing="The zone"
          onConfirm={() => update.mutateAsync(body(false))}
          onClose={() => setDisabling(false)}
        />
      )}
    </section>
  );
}

function KeysSection({ zone, dnssec }: { zone: Zone; dnssec: Dnssec }) {
  const canRoll = useCan("startZoneKeyRollover") && dnssec.enabled;
  const roll = useStartKeyRollover(zone.id);
  const [confirmKsk, setConfirmKsk] = useState(false);
  const keys = dnssec.keys.filter((k) => k.state !== "removed");

  return (
    <section aria-label="Keys">
      <SectionHeader
        title="Keys"
        description="A ZSK rollover pre-publishes the new key before it signs; a KSK rollover signs with both KSKs until the parent publishes the new DS."
        action={
          canRoll && (
            <div className="flex gap-2">
              <Button
                variant="outline"
                disabled={roll.isPending}
                onClick={() => roll.mutate("zsk")}
              >
                <RotateCw className="mr-1.5 h-4 w-4" />
                Roll ZSK
              </Button>
              <Button
                variant="outline"
                disabled={roll.isPending}
                onClick={() => setConfirmKsk(true)}
              >
                <RotateCw className="mr-1.5 h-4 w-4" />
                Roll KSK
              </Button>
            </div>
          )
        }
      />
      <ErrorAlert error={roll.error} thing="The zone" className="mb-3" />
      <Card className="overflow-hidden">
        <Table aria-label="Signing keys">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Role</TableHead>
              <TableHead>Algorithm</TableHead>
              <TableHead className="text-right">Tag</TableHead>
              <TableHead>State</TableHead>
              <TableHead>DS at parent</TableHead>
              <TableHead>Storage</TableHead>
              <TableHead>Activated</TableHead>
              <TableHead>Retired</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((k) => (
              <TableRow key={k.id}>
                <TableCell className="py-2">
                  <Badge variant={k.role === "ksk" ? "default" : "secondary"}>
                    {k.role}
                  </Badge>
                </TableCell>
                <TableCell className="py-2 tabular-nums">
                  {k.algorithm}
                </TableCell>
                <TableCell className="py-2 text-right font-mono text-[13px] tabular-nums">
                  {k.key_tag}
                </TableCell>
                <TableCell className="py-2">
                  <KeyState k={k} />
                </TableCell>
                <TableCell className="py-2">
                  {k.role === "ksk" ? (
                    <StatusDot
                      tone={k.ds_state === "seen" ? "success" : "warning"}
                    >
                      {k.ds_state}
                    </StatusDot>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="py-2">{k.backend}</TableCell>
                <TableCell className="py-2 text-sm">
                  {formatDateTime(k.activated_at)}
                </TableCell>
                <TableCell className="py-2 text-sm">
                  {formatDateTime(k.retired_at)}
                </TableCell>
              </TableRow>
            ))}
            {keys.length === 0 && <MessageRow colSpan={8}>No keys.</MessageRow>}
          </TableBody>
        </Table>
      </Card>
      {confirmKsk && (
        <Dialog open onOpenChange={(open) => !open && setConfirmKsk(false)}>
          <DialogContent role="alertdialog">
            <DialogHeader>
              <DialogTitle>Roll the KSK</DialogTitle>
              <DialogDescription>
                A new KSK starts signing the DNSKEY set next to the current one
                and is advertised with CDS/CDNSKEY. Publish its DS at the parent
                and confirm it here; the old KSK is removed after the parent DS
                TTL.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter className="gap-2">
              <Button variant="outline" onClick={() => setConfirmKsk(false)}>
                Cancel
              </Button>
              <Button
                disabled={roll.isPending}
                onClick={() =>
                  roll.mutate("ksk", { onSettled: () => setConfirmKsk(false) })
                }
              >
                Confirm
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </section>
  );
}

function KeyState({ k }: { k: Key }) {
  const tone =
    k.state === "active"
      ? "success"
      : k.state === "published"
        ? "warning"
        : "muted";
  return <StatusDot tone={tone}>{k.state}</StatusDot>;
}

function DsSection({ zone, dnssec }: { zone: Zone; dnssec: Dnssec }) {
  const canConfirm = useCan("confirmZoneKskDs");
  const confirm = useConfirmKskDs(zone.id);
  const pending = dnssec.keys.filter(
    (k) => k.role === "ksk" && k.state === "active" && k.ds_state === "pending",
  );

  return (
    <section aria-label="DS records">
      <SectionHeader
        title="DS records"
        description="Publish these at the parent zone (your registrar or the parent's operator), then confirm that the parent serves them."
      />
      <div className="grid gap-3">
        {dnssec.ds.length === 0 && (
          <Card className="text-muted-foreground p-5 text-sm">
            No active KSK yet.
          </Card>
        )}
        {dnssec.ds.map((ds, i) => (
          <SecretValue
            key={ds}
            value={ds.replace(/\s+/g, " ")}
            testId={`zone-ds-${i}`}
          />
        ))}
        <ErrorAlert error={confirm.error} thing="The zone" />
        {pending.map((k) => (
          <Card
            key={k.id}
            className="flex flex-wrap items-center justify-between gap-3 px-5 py-3"
          >
            <span className="text-sm">
              The parent's DS for KSK{" "}
              <span className="font-mono">{k.key_tag}</span> is not confirmed;
              CDS and CDNSKEY advertise it until it is.
            </span>
            {canConfirm && (
              <Button
                variant="outline"
                disabled={confirm.isPending}
                onClick={() => confirm.mutate(k.id)}
              >
                Parent DS published
              </Button>
            )}
          </Card>
        ))}
        {dnssec.dnskeys.length > 0 && (
          <details className="text-sm">
            <summary className="text-muted-foreground cursor-pointer">
              DNSKEY records ({dnssec.dnskeys.length})
            </summary>
            <div className="mt-2 grid gap-2">
              {dnssec.dnskeys.map((k) => (
                <code
                  key={k}
                  className="bg-muted rounded-md border p-2 font-mono text-xs break-all"
                >
                  {k.replace(/\s+/g, " ")}
                </code>
              ))}
            </div>
          </details>
        )}
      </div>
    </section>
  );
}
