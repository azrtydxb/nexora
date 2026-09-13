import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus, Trash2, TriangleAlert } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import {
  useCreateNegativeTrustAnchor,
  useCreateTrustAnchor,
  useDeleteNegativeTrustAnchor,
  useDeleteTrustAnchor,
  useDnssecSettings,
  useDnssecStatus,
  useNegativeTrustAnchors,
  useTrustAnchors,
  useUpdateDnssecSettings,
} from "@/api/resolution";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatAgo,
  formatDateTime,
  MessageRow,
  SavedNote,
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

type Settings = Schemas["DnssecSettings"];
type TrustAnchor = Schemas["TrustAnchor"];
type NTA = Schemas["NegativeTrustAnchor"];
type EngineStatus = Schemas["DnssecStatus"]["engines"][number];

const hourMs = 3_600_000;

export function DnssecPage() {
  const settings = useDnssecSettings();
  return (
    <>
      <PageHeader
        title="DNSSEC"
        description="Validation of signed answers for recursion and validating forward zones, the trust anchors it starts from, and domains exempted from it."
      />
      <RefreshWarning />
      <section aria-label="Validation settings" className="mb-10">
        <h2 className="mb-3 text-sm font-semibold">Validation settings</h2>
        <ErrorAlert
          error={settings.error}
          prefix="Could not load DNSSEC settings"
          className="mb-3"
        />
        {settings.isPending && (
          <Card className="text-muted-foreground p-5 text-sm">Loading…</Card>
        )}
        {settings.data && <SettingsForm settings={settings.data} />}
      </section>
      <TrustAnchorsSection />
      <NegativeTrustAnchorsSection />
      <EnginesSection />
    </>
  );
}

/** The red banner shown when root trust anchor maintenance is failing on any engine. */
function RefreshWarning() {
  const status = useDnssecStatus();
  const failing = (status.data?.engines ?? []).filter((e) => {
    const root = e.trust_anchors.filter((a) => a.zone === ".");
    if (root.length === 0) return false;
    const stuck = root.some(
      (a) =>
        a.last_error !== "" &&
        (!a.last_refresh_success ||
          Date.now() - new Date(a.last_refresh_success).getTime() >
            72 * hourMs),
    );
    const noUsable = !root.some(
      (a) => a.state === "valid" || a.state === "configured",
    );
    return stuck || noUsable;
  });
  if (failing.length === 0) return null;
  return (
    <Alert variant="destructive" className="mb-6">
      <TriangleAlert className="h-4 w-4" />
      <AlertDescription>
        <span className="font-semibold">Trust anchor refresh failing</span> on{" "}
        {failing.map((e) => e.engine_name).join(", ")}. Validation stops once
        the root key is no longer trusted; check the engines' reachability of
        the root servers.
      </AlertDescription>
    </Alert>
  );
}

function SettingsForm({ settings }: { settings: Settings }) {
  const canUpdate = useCan("updateDnssecSettings");
  const toForm = (s: Settings) => ({
    validation: s.validation,
    validate_forwarded: s.validate_forwarded,
    rfc5011: s.rfc5011,
  });
  const [form, setForm] = useState(() => toForm(settings));
  const save = useUpdateDnssecSettings();
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(settings));

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(
      { ...form, revision: settings.revision },
      { onSuccess: (saved) => setForm(toForm(saved)) },
    );
  }

  const toggle = (
    id: string,
    key: keyof typeof form,
    label: string,
    help: string,
    disabled = false,
  ) => (
    <div className="flex items-start gap-3">
      <Switch
        id={id}
        className="mt-0.5"
        checked={form[key]}
        disabled={disabled}
        onCheckedChange={(v) => setForm((f) => ({ ...f, [key]: v }))}
      />
      <div>
        <Label htmlFor={id}>{label}</Label>
        <p className="text-muted-foreground mt-1 text-sm">{help}</p>
      </div>
    </div>
  );

  return (
    <form onSubmit={submit}>
      <Card className="overflow-hidden">
        <fieldset
          disabled={!canUpdate || save.isPending}
          className="grid gap-5 px-5 py-5 lg:grid-cols-3"
        >
          {toggle(
            "dnssec-validation",
            "validation",
            "Validate answers",
            "Check signatures on recursive answers and validating forward zones; bogus answers become SERVFAIL.",
          )}
          {toggle(
            "dnssec-validate-forwarded",
            "validate_forwarded",
            "Validate forwarded answers",
            "Also validate answers from the global upstreams in forward mode, up to the root trust anchor.",
            !form.validation,
          )}
          {toggle(
            "dnssec-rfc5011",
            "rfc5011",
            "Automated trust anchor updates (RFC 5011)",
            "Engines track root key rollovers and trust new keys after the hold-down period.",
          )}
        </fieldset>
        <div className="bg-muted/40 flex flex-wrap items-center justify-end gap-3 border-t px-5 py-3">
          {save.error ? (
            <ErrorAlert
              error={save.error}
              thing="The DNSSEC settings"
              className="mr-auto w-auto flex-1 py-2"
            />
          ) : (
            <span className="text-muted-foreground mr-auto text-sm">
              {!canUpdate ? (
                "Operators and administrators can change these settings."
              ) : dirty ? (
                "Unsaved changes"
              ) : (
                <SavedNote show={save.isSuccess}>Settings saved</SavedNote>
              )}
            </span>
          )}
          {canUpdate && (
            <Button type="submit" disabled={!dirty || save.isPending}>
              {save.isPending ? "Saving…" : "Save settings"}
            </Button>
          )}
        </div>
      </Card>
    </form>
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

function TrustAnchorsSection() {
  const canCreate = useCan("createTrustAnchor");
  const canDelete = useCan("deleteTrustAnchor");
  const anchors = useTrustAnchors();
  const del = useDeleteTrustAnchor();
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<TrustAnchor | null>(null);
  const rows = anchors.data ?? [];
  const cols = 3 + (canDelete ? 1 : 0);
  const keyTag = (a: TrustAnchor) => a.ds.split(" ")[0];

  return (
    <section aria-label="Trust anchors" className="mb-10">
      <SectionHeader
        title="Trust anchors"
        description="DS records validation chains end at. The IANA root keys ship built in; add a zone's DS to validate a private signed hierarchy."
        action={
          canCreate && (
            <Button variant="outline" onClick={() => setAdding(true)}>
              <Plus className="mr-1.5 h-4 w-4" />
              Add trust anchor
            </Button>
          )
        }
      />
      <ErrorAlert
        error={anchors.error}
        prefix="Could not load trust anchors"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Zone</TableHead>
              <TableHead>DS</TableHead>
              <TableHead>Source</TableHead>
              {canDelete && (
                <TableHead className="w-16 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((a) => (
              <TableRow key={a.id}>
                <TableCell className="py-3 font-mono text-[13px]">
                  {a.zone}
                </TableCell>
                <TableCell className="max-w-md py-3 font-mono text-[13px] break-all">
                  {a.ds}
                </TableCell>
                <TableCell className="py-3">
                  {a.source === "iana" ? (
                    <Badge variant="secondary">IANA</Badge>
                  ) : (
                    <Badge variant="outline">Operator</Badge>
                  )}
                </TableCell>
                {canDelete && (
                  <TableCell className="py-2 text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="hover:text-destructive h-8 w-8"
                      aria-label={`Delete trust anchor ${keyTag(a)} for ${a.zone}`}
                      onClick={() => setDeleting(a)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {anchors.isPending && (
              <MessageRow colSpan={cols}>Loading…</MessageRow>
            )}
            {anchors.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>
                No trust anchors: validation cannot succeed.
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {adding && <TrustAnchorDialog onClose={() => setAdding(false)} />}
      {deleting && (
        <ConfirmDialog
          title="Delete trust anchor"
          description={`Delete the trust anchor ${keyTag(deleting)} for ${deleting.zone}? Answers under it stop validating unless another anchor covers them.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This trust anchor"
          onConfirm={() => del.mutateAsync(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

function TrustAnchorDialog({ onClose }: { onClose: () => void }) {
  const [zone, setZone] = useState("");
  const [ds, setDs] = useState("");
  const create = useCreateTrustAnchor();

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate({ zone: zone.trim(), ds: ds.trim() }, { onSuccess: onClose });
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add trust anchor</DialogTitle>
          <DialogDescription>
            A DS record in presentation format: key tag, algorithm, digest type
            and hex digest.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-1.5">
            <Label htmlFor="trust-anchor-zone">Zone</Label>
            <Input
              id="trust-anchor-zone"
              className="font-mono"
              placeholder="example."
              value={zone}
              onChange={(e) => setZone(e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="trust-anchor-ds">DS record</Label>
            <Input
              id="trust-anchor-ds"
              className="font-mono"
              placeholder="12345 13 2 49FD46E6C4B45C55D4AC…"
              value={ds}
              onChange={(e) => setDs(e.target.value)}
            />
          </div>
          <ErrorAlert error={create.error} />
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function NegativeTrustAnchorsSection() {
  const canCreate = useCan("createNegativeTrustAnchor");
  const canDelete = useCan("deleteNegativeTrustAnchor");
  const ntas = useNegativeTrustAnchors();
  const del = useDeleteNegativeTrustAnchor();
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<NTA | null>(null);
  const rows = ntas.data ?? [];
  const cols = 4 + (canDelete ? 1 : 0);

  return (
    <section aria-label="Negative trust anchors" className="mb-10">
      <SectionHeader
        title="Negative trust anchors"
        description="Domains whose answers are served without validation until the anchor expires — for a zone with broken signatures you cannot fix."
        action={
          canCreate && (
            <Button variant="outline" onClick={() => setAdding(true)}>
              <Plus className="mr-1.5 h-4 w-4" />
              Add negative trust anchor
            </Button>
          )
        }
      />
      <ErrorAlert
        error={ntas.error}
        prefix="Could not load negative trust anchors"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Domain</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead>Created by</TableHead>
              {canDelete && (
                <TableHead className="w-16 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((n) => (
              <TableRow key={n.id}>
                <TableCell className="py-3 font-mono text-[13px]">
                  {n.domain}
                </TableCell>
                <TableCell className="py-3">
                  {n.reason || <span className="text-muted-foreground">—</span>}
                </TableCell>
                <TableCell
                  className="py-3 whitespace-nowrap"
                  title={formatDateTime(n.expires_at)}
                >
                  {formatAgo(n.expires_at)}
                </TableCell>
                <TableCell className="py-3">{n.created_by}</TableCell>
                {canDelete && (
                  <TableCell className="py-2 text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="hover:text-destructive h-8 w-8"
                      aria-label={`Delete negative trust anchor ${n.domain}`}
                      onClick={() => setDeleting(n)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {ntas.isPending && <MessageRow colSpan={cols}>Loading…</MessageRow>}
            {ntas.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>No negative trust anchors.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {adding && <NtaDialog onClose={() => setAdding(false)} />}
      {deleting && (
        <ConfirmDialog
          title="Delete negative trust anchor"
          description={`Delete the negative trust anchor for ${deleting.domain}? Its answers are validated again.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This negative trust anchor"
          onConfirm={() => del.mutateAsync(deleting.id)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

const lifetimes: { value: string; label: string; hours: number }[] = [
  { value: "1h", label: "1 hour", hours: 1 },
  { value: "1d", label: "1 day", hours: 24 },
  { value: "7d", label: "7 days", hours: 24 * 7 },
  { value: "30d", label: "30 days", hours: 24 * 30 },
];

function NtaDialog({ onClose }: { onClose: () => void }) {
  const [form, setForm] = useState({ domain: "", reason: "", lifetime: "1d" });
  const set = <K extends keyof typeof form>(key: K, value: string) =>
    setForm((f) => ({ ...f, [key]: value }));
  const create = useCreateNegativeTrustAnchor();

  function submit(e: FormEvent) {
    e.preventDefault();
    const hours = lifetimes.find((l) => l.value === form.lifetime)!.hours;
    create.mutate(
      {
        domain: form.domain.trim(),
        reason: form.reason.trim(),
        // A minute short of the full lifetime keeps "30 days" within the server's 30-day bound.
        expires_at: new Date(
          Date.now() + hours * hourMs - 60_000,
        ).toISOString(),
      },
      { onSuccess: onClose },
    );
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add negative trust anchor</DialogTitle>
          <DialogDescription>
            Answers for the domain and its subdomains skip validation until it
            expires.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-1.5">
            <Label htmlFor="nta-domain">Domain</Label>
            <Input
              id="nta-domain"
              className="font-mono"
              placeholder="broken.example"
              value={form.domain}
              onChange={(e) => set("domain", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="nta-reason">Reason</Label>
            <Input
              id="nta-reason"
              maxLength={500}
              value={form.reason}
              onChange={(e) => set("reason", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="nta-lifetime">Expires in</Label>
            <Select
              value={form.lifetime}
              onValueChange={(v) => set("lifetime", v)}
            >
              <SelectTrigger id="nta-lifetime" className="h-9">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {lifetimes.map((l) => (
                  <SelectItem key={l.value} value={l.value}>
                    {l.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <ErrorAlert error={create.error} />
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EnginesSection() {
  const status = useDnssecStatus();
  // Engines that have not reported DNSSEC statistics yet are listed too.
  const engines = useQuery({
    queryKey: ["engines"],
    queryFn: async () => unwrap(await api.GET("/engines")),
  });
  const reports = new Map(
    (status.data?.engines ?? []).map((e) => [e.engine_id, e]),
  );
  const rows: { id: string; name: string; report?: EngineStatus }[] = (
    engines.data ?? []
  ).map((e) => ({ id: e.id, name: e.node_name, report: reports.get(e.id) }));
  for (const r of status.data?.engines ?? []) {
    if (!rows.some((row) => row.id === r.engine_id)) {
      rows.push({ id: r.engine_id, name: r.engine_name, report: r });
    }
  }
  const loading = status.isPending || engines.isPending;

  return (
    <section aria-label="Validation by engine">
      <SectionHeader
        title="Validation by engine"
        description="Validation outcomes since each engine started, and the state of its trust anchor keys. Refreshed every 10 seconds."
      />
      <ErrorAlert
        error={status.error ?? engines.error}
        prefix="Could not load validation status"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Engine</TableHead>
              <TableHead className="text-right">Secure</TableHead>
              <TableHead className="text-right">Insecure</TableHead>
              <TableHead className="text-right">Bogus</TableHead>
              <TableHead className="text-right">Indeterminate</TableHead>
              <TableHead>Trust anchor keys</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map(({ id, name, report }) => (
              <TableRow key={id}>
                <TableCell className="py-3 font-medium whitespace-nowrap">
                  {name}
                  {report && (
                    <div className="text-muted-foreground text-xs font-normal">
                      reported {formatAgo(report.reported_at)}
                    </div>
                  )}
                </TableCell>
                {report ? (
                  <>
                    <Count n={report.secure} />
                    <Count n={report.insecure} />
                    <Count n={report.bogus} />
                    <Count n={report.indeterminate} />
                    <TableCell className="py-3">
                      <div className="flex flex-col gap-1">
                        {report.trust_anchors.map((a) => (
                          <span
                            key={`${a.zone} ${a.key_tag}`}
                            title={a.last_error || undefined}
                          >
                            <StatusDot
                              tone={
                                a.state === "valid" || a.state === "configured"
                                  ? a.last_error
                                    ? "warning"
                                    : "success"
                                  : a.state === "add_pend"
                                    ? "muted"
                                    : "destructive"
                              }
                            >
                              <span className="font-mono text-[13px]">
                                {a.zone} {a.key_tag}
                              </span>
                              <span className="text-muted-foreground">
                                {a.state.replace("_", " ")}
                                {a.hold_down_until &&
                                  ` until ${formatDateTime(a.hold_down_until)}`}
                              </span>
                            </StatusDot>
                          </span>
                        ))}
                        {report.active_negative_trust_anchors > 0 && (
                          <span className="text-muted-foreground text-xs">
                            {report.active_negative_trust_anchors} active
                            negative trust anchors
                          </span>
                        )}
                      </div>
                    </TableCell>
                  </>
                ) : (
                  <TableCell
                    colSpan={5}
                    className="text-muted-foreground py-3 text-sm"
                  >
                    No validation report yet
                  </TableCell>
                )}
              </TableRow>
            ))}
            {loading && <MessageRow colSpan={6}>Loading…</MessageRow>}
            {!loading && rows.length === 0 && (
              <MessageRow colSpan={6}>No engines have joined yet.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
    </section>
  );
}

function Count({ n }: { n: number }) {
  return (
    <TableCell className="py-3 text-right tabular-nums">
      {n.toLocaleString()}
    </TableCell>
  );
}
