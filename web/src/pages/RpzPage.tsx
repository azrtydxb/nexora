import { useState, type FormEvent, type ReactNode } from "react";
import {
  ArrowDown,
  ArrowUp,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  Upload,
} from "lucide-react";

import { type Schemas } from "@/api/client";
import {
  useCreateRpzZone,
  useDeleteRpzZone,
  useRefreshRpzZone,
  useReorderRpzZones,
  useRpzZone,
  useRpzZones,
  useUpdateRpzZone,
  useUploadRpzZoneFile,
} from "@/api/resolution";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatAgo,
  MessageRow,
  SavedNote,
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

type RpzZone = Schemas["RpzZone"];
type SourceType = Schemas["RpzZoneInput"]["source_type"];
type Override = Schemas["RpzZoneInput"]["policy_override"];
type Algorithm = NonNullable<Schemas["RpzZoneInput"]["tsig_algorithm"]>;

const overrides: { value: Override; label: string }[] = [
  { value: "given", label: "Given" },
  { value: "disabled", label: "Disabled" },
  { value: "nxdomain", label: "NXDOMAIN" },
  { value: "nodata", label: "NODATA" },
  { value: "passthru", label: "PASSTHRU" },
  { value: "drop", label: "DROP" },
  { value: "tcp_only", label: "TCP-only" },
];

const overrideLabel = (v: string) =>
  overrides.find((o) => o.value === v)?.label ?? v;

export function RpzPage() {
  const canCreate = useCan("createRpzZone");
  const canUpdate = useCan("updateRpzZone");
  const canDelete = useCan("deleteRpzZone");
  const canUpload = useCan("uploadRpzZoneFile");
  const canRefresh = useCan("refreshRpzZone");
  const canReorder = useCan("reorderRpzZones");
  const zones = useRpzZones();
  const reorder = useReorderRpzZones();
  const refresh = useRefreshRpzZone();
  const del = useDeleteRpzZone();
  const [editing, setEditing] = useState<RpzZone | "new" | null>(null);
  const [uploading, setUploading] = useState<RpzZone | null>(null);
  const [deleting, setDeleting] = useState<RpzZone | null>(null);
  const rows = [...(zones.data ?? [])].sort((a, b) => a.position - b.position);
  const actions =
    canUpdate || canDelete || canUpload || canRefresh || canReorder;
  const cols = 7 + (actions ? 1 : 0);

  function move(i: number, by: -1 | 1) {
    const ids = rows.map((z) => z.id);
    [ids[i], ids[i + by]] = [ids[i + by], ids[i]];
    reorder.mutate(ids);
  }

  return (
    <>
      <PageHeader
        title="Response policy zones"
        description="Policy zones rewrite or block answers by name, answer address or name server. The first zone in the list whose trigger matches decides."
        actions={
          canCreate && (
            <Button onClick={() => setEditing("new")}>
              <Plus className="mr-1.5 h-4 w-4" />
              New zone
            </Button>
          )
        }
      />
      <ErrorAlert
        error={zones.error}
        prefix="Could not load RPZ zones"
        className="mb-4"
      />
      <ErrorAlert
        error={reorder.error ?? refresh.error}
        thing="This zone"
        className="mb-4"
      />
      <div className="mb-2 h-5">
        <SavedNote show={refresh.isSuccess}>Refresh requested</SavedNote>
      </div>
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-12">#</TableHead>
              <TableHead>Zone</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Policy</TableHead>
              <TableHead className="text-right">Min. refresh</TableHead>
              <TableHead>Engine group</TableHead>
              <TableHead>Engines</TableHead>
              {actions && (
                <TableHead className="w-44 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((z, i) => (
              <TableRow key={z.id}>
                <TableCell className="text-muted-foreground py-3">
                  {i + 1}
                </TableCell>
                <TableCell className="py-3 font-mono text-[13px]">
                  {z.name}
                </TableCell>
                <TableCell className="py-3">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Badge variant="secondary">
                      {z.source_type === "file" ? "File" : "Zone transfer"}
                    </Badge>
                    {z.source_type === "file" ? (
                      <span className="text-muted-foreground text-sm">
                        {z.file_records === null
                          ? "no file"
                          : `${z.file_records} records`}
                      </span>
                    ) : (
                      <span className="font-mono text-[13px]">{z.primary}</span>
                    )}
                    {z.tsig_secret_set && (
                      <Badge variant="outline" title={z.tsig_key_name ?? ""}>
                        secret set
                      </Badge>
                    )}
                  </div>
                </TableCell>
                <TableCell className="py-3">
                  {overrideLabel(z.policy_override)}
                </TableCell>
                <TableCell className="py-3 text-right tabular-nums">
                  {z.min_refresh_seconds} s
                </TableCell>
                <TableCell className="py-3 whitespace-nowrap">
                  <EngineGroupName id={z.engine_group_id} />
                </TableCell>
                <TableCell className="py-3">
                  <EngineStatus zone={z} />
                </TableCell>
                {actions && (
                  <TableCell className="py-2 text-right whitespace-nowrap">
                    {canReorder && (
                      <>
                        <IconButton
                          label={`Move ${z.name} up`}
                          disabled={i === 0 || reorder.isPending}
                          onClick={() => move(i, -1)}
                        >
                          <ArrowUp className="h-4 w-4" />
                        </IconButton>
                        <IconButton
                          label={`Move ${z.name} down`}
                          disabled={i === rows.length - 1 || reorder.isPending}
                          onClick={() => move(i, 1)}
                        >
                          <ArrowDown className="h-4 w-4" />
                        </IconButton>
                      </>
                    )}
                    {canUpload && z.source_type === "file" && (
                      <IconButton
                        label={`Upload file for ${z.name}`}
                        onClick={() => setUploading(z)}
                      >
                        <Upload className="h-4 w-4" />
                      </IconButton>
                    )}
                    {canRefresh && z.source_type === "transfer" && (
                      <IconButton
                        label={`Refresh ${z.name}`}
                        disabled={refresh.isPending}
                        onClick={() => refresh.mutate(z.id)}
                      >
                        <RefreshCw className="h-4 w-4" />
                      </IconButton>
                    )}
                    {canUpdate && (
                      <IconButton
                        label={`Edit ${z.name}`}
                        onClick={() => setEditing(z)}
                      >
                        <Pencil className="h-4 w-4" />
                      </IconButton>
                    )}
                    {canDelete && (
                      <IconButton
                        label={`Delete ${z.name}`}
                        className="hover:text-destructive"
                        onClick={() => setDeleting(z)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </IconButton>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {zones.isPending && (
              <MessageRow colSpan={cols}>Loading…</MessageRow>
            )}
            {zones.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>No response policy zones.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>

      {editing === "new" && <RpzZoneDialog onClose={() => setEditing(null)} />}
      {editing !== null && editing !== "new" && (
        <EditRpzZoneDialog id={editing.id} onClose={() => setEditing(null)} />
      )}
      {uploading && (
        <UploadDialog zone={uploading} onClose={() => setUploading(null)} />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete RPZ zone"
          description={`Delete the policy zone ${deleting.name}? Engines stop applying its policies once they apply the new configuration.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This zone"
          onConfirm={() =>
            del.mutateAsync({ id: deleting.id, revision: deleting.revision })
          }
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

function IconButton({
  label,
  className,
  disabled,
  onClick,
  children,
}: {
  label: string;
  className?: string;
  disabled?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <Button
      variant="ghost"
      size="icon"
      className={`h-8 w-8 ${className ?? ""}`}
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </Button>
  );
}

/** Per-engine serial and freshness; red when an engine serves stale data, amber on a refresh error. */
function EngineStatus({ zone }: { zone: RpzZone }) {
  if (zone.status.length === 0) {
    return <span className="text-muted-foreground text-sm">not loaded</span>;
  }
  return (
    <div className="flex flex-col gap-1">
      {zone.status.map((s) => (
        <span
          key={s.engine_id}
          title={s.last_error || undefined}
          className="inline-flex flex-wrap items-center gap-1.5"
        >
          <StatusDot
            tone={
              s.stale ? "destructive" : s.last_error ? "warning" : "success"
            }
          >
            {s.engine_name}
          </StatusDot>
          <span className="text-muted-foreground text-xs tabular-nums">
            serial {s.serial} · {s.records} records ·{" "}
            {formatAgo(s.last_success)}
          </span>
          {s.stale ? (
            <Badge variant="destructive">stale</Badge>
          ) : (
            s.last_error && (
              <Badge
                variant="outline"
                className="border-warning/40 text-warning"
              >
                error
              </Badge>
            )
          )}
        </span>
      ))}
    </div>
  );
}

type ZoneForm = {
  name: string;
  source_type: SourceType;
  primary: string;
  tsig_algorithm: Algorithm | "none";
  tsig_key_name: string;
  tsig_secret: string;
  policy_override: Override;
  min_refresh_seconds: string;
  engine_group_id: string | null;
};

function EditRpzZoneDialog({
  id,
  onClose,
}: {
  id: string;
  onClose: () => void;
}) {
  const zone = useRpzZone(id);
  if (zone.data) return <RpzZoneDialog zone={zone.data} onClose={onClose} />;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit RPZ zone</DialogTitle>
          <DialogDescription>Loading the current zone…</DialogDescription>
        </DialogHeader>
        <ErrorAlert error={zone.error} thing="This zone" />
      </DialogContent>
    </Dialog>
  );
}

function RpzZoneDialog({
  zone,
  onClose,
}: {
  zone?: RpzZone;
  onClose: () => void;
}) {
  const [form, setForm] = useState<ZoneForm>({
    name: zone?.name ?? "",
    source_type: zone?.source_type ?? "file",
    primary: zone?.primary ?? "",
    tsig_algorithm: (zone?.tsig_algorithm as Algorithm | null) ?? "none",
    tsig_key_name: zone?.tsig_key_name ?? "",
    // The stored secret is never sent back; an empty field keeps it.
    tsig_secret: "",
    policy_override: (zone?.policy_override as Override) ?? "given",
    min_refresh_seconds: String(zone?.min_refresh_seconds ?? 60),
    engine_group_id: zone?.engine_group_id ?? null,
  });
  const set = <K extends keyof ZoneForm>(key: K, value: ZoneForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const create = useCreateRpzZone();
  const update = useUpdateRpzZone();
  const save = zone ? update : create;
  const transfer = form.source_type === "transfer";
  const tsig = transfer && form.tsig_algorithm !== "none";

  function submit(e: FormEvent) {
    e.preventDefault();
    const fields = {
      primary: transfer ? form.primary.trim() : null,
      tsig_algorithm: tsig ? (form.tsig_algorithm as Algorithm) : null,
      tsig_key_name: tsig ? form.tsig_key_name.trim() : null,
      tsig_secret: tsig && form.tsig_secret !== "" ? form.tsig_secret : null,
      min_refresh_seconds: Number(form.min_refresh_seconds),
      policy_override: form.policy_override,
    };
    const done = { onSuccess: onClose };
    if (zone) {
      update.mutate(
        { id: zone.id, body: { ...fields, revision: zone.revision } },
        done,
      );
    } else {
      create.mutate(
        {
          ...fields,
          name: form.name.trim(),
          source_type: form.source_type,
          engine_group_id: form.engine_group_id,
        },
        done,
      );
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{zone ? "Edit RPZ zone" : "New RPZ zone"}</DialogTitle>
          <DialogDescription>
            Changes publish a new configuration version that every engine
            applies.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid grid-cols-[1fr_11rem] gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="rpz-name">Zone name</Label>
              <Input
                id="rpz-name"
                className="font-mono"
                placeholder="rpz.example."
                maxLength={255}
                disabled={!!zone}
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
              />
            </div>
            {!zone && (
              <div className="grid gap-1.5">
                <Label htmlFor="rpz-source">Source</Label>
                <Select
                  value={form.source_type}
                  onValueChange={(v) => set("source_type", v as SourceType)}
                >
                  <SelectTrigger id="rpz-source" className="h-9">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="file">File</SelectItem>
                    <SelectItem value="transfer">Zone transfer</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="rpz-engine-group">Engine group</Label>
            {/* The scope is fixed at creation; the update body has no engine group. */}
            <EngineGroupSelect
              id="rpz-engine-group"
              testId="rpz-engine-group"
              disabled={!!zone}
              value={form.engine_group_id}
              onChange={(v) => set("engine_group_id", v)}
            />
          </div>
          {transfer && (
            <>
              <div className="grid gap-1.5">
                <Label htmlFor="rpz-primary">Primary</Label>
                <Input
                  id="rpz-primary"
                  className="font-mono"
                  placeholder="192.0.2.53:53"
                  value={form.primary}
                  onChange={(e) => set("primary", e.target.value)}
                />
              </div>
              <div className="grid grid-cols-[11rem_1fr] gap-4">
                <div className="grid gap-1.5">
                  <Label htmlFor="rpz-tsig-algorithm">TSIG algorithm</Label>
                  <Select
                    value={form.tsig_algorithm}
                    onValueChange={(v) =>
                      set("tsig_algorithm", v as ZoneForm["tsig_algorithm"])
                    }
                  >
                    <SelectTrigger id="rpz-tsig-algorithm" className="h-9">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">None</SelectItem>
                      <SelectItem value="hmac-sha256">hmac-sha256</SelectItem>
                      <SelectItem value="hmac-sha512">hmac-sha512</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {tsig && (
                  <div className="grid gap-1.5">
                    <Label htmlFor="rpz-tsig-key-name">TSIG key name</Label>
                    <Input
                      id="rpz-tsig-key-name"
                      className="font-mono"
                      placeholder="transfer-key."
                      value={form.tsig_key_name}
                      onChange={(e) => set("tsig_key_name", e.target.value)}
                    />
                  </div>
                )}
              </div>
              {tsig && (
                <div className="grid gap-1.5">
                  <Label htmlFor="rpz-tsig-secret">TSIG secret (base64)</Label>
                  <Input
                    id="rpz-tsig-secret"
                    type="password"
                    className="font-mono"
                    autoComplete="off"
                    placeholder={
                      zone?.tsig_secret_set
                        ? "leave empty to keep the stored secret"
                        : ""
                    }
                    value={form.tsig_secret}
                    onChange={(e) => set("tsig_secret", e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    Stored encrypted and never shown again.
                  </p>
                </div>
              )}
            </>
          )}
          <div className="grid grid-cols-2 gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="rpz-override">Policy override</Label>
              <Select
                value={form.policy_override}
                onValueChange={(v) => set("policy_override", v as Override)}
              >
                <SelectTrigger id="rpz-override" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {overrides.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="rpz-min-refresh">Minimum refresh (seconds)</Label>
              <Input
                id="rpz-min-refresh"
                type="number"
                min={1}
                max={86400}
                value={form.min_refresh_seconds}
                onChange={(e) => set("min_refresh_seconds", e.target.value)}
              />
            </div>
          </div>
          <ErrorAlert error={save.error} thing="This zone" />
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

function UploadDialog({
  zone,
  onClose,
}: {
  zone: RpzZone;
  onClose: () => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const upload = useUploadRpzZoneFile();

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    const content = await file.text();
    upload.mutate(
      { id: zone.id, body: { content, revision: zone.revision } },
      { onSuccess: onClose },
    );
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Upload zone file</DialogTitle>
          <DialogDescription>
            A master-file RPZ zone for{" "}
            <span className="font-mono">{zone.name}</span>. It replaces the
            current file; include directives are rejected.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="rpz-file">Zone file</Label>
            <Input
              id="rpz-file"
              type="file"
              accept=".zone,.rpz,.txt,text/plain"
              onChange={(e) => {
                setFile(e.target.files?.[0] ?? null);
                upload.reset();
              }}
            />
          </div>
          <ErrorAlert error={upload.error} thing="This zone" />
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!file || upload.isPending}>
              Upload
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
