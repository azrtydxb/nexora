import { useState, type FormEvent, type ReactNode } from "react";
import { History, Pause, Play, Trash2 } from "lucide-react";
import { useNavigate, useParams } from "react-router";

import {
  type EngineGroup,
  useDeleteEngineGroup,
  useEngineGroup,
  useEngines,
  useResumeRollouts,
  useRollbackEngineGroup,
  useRollouts,
  useUpdateEngineGroup,
} from "@/api/fleet";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatAgo,
  formatDateTime,
  MessageRow,
  SavedNote,
} from "@/components/common";
import {
  BackLink,
  canarySize,
  EngineStatusBadge,
  LinkButton,
  RolloutProgress,
  RolloutStages,
  RolloutStateBadge,
} from "@/components/fleet";
import { EngineModal, EngineModalOpenButton } from "@/components/EngineModal";
import { HelpTip } from "@/components/HelpTip";
import { PageHeader } from "@/components/layout/AppShell";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Textarea } from "@/components/ui/textarea";

export function EngineGroupPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const q = useEngineGroup(id);
  const canRollback = useCan("rollbackEngineGroup");
  const canResume = useCan("resumeEngineGroupRollouts");
  const canDelete = useCan("deleteEngineGroup");
  const resume = useResumeRollouts();
  const del = useDeleteEngineGroup();
  const [rollingBack, setRollingBack] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const g = q.data;

  return (
    <div data-testid="enginegroup-detail">
      <BackLink to="/engines">Engines</BackLink>
      <PageHeader
        title={g?.name ?? "Engine group"}
        description={g?.description || undefined}
        actions={
          g && (
            <>
              {canRollback && (
                <Button
                  variant="outline"
                  data-testid="enginegroup-rollback"
                  onClick={() => setRollingBack(true)}
                >
                  <History className="mr-1.5 h-4 w-4" />
                  Roll back
                </Button>
              )}
              {canDelete && g.name !== "default" && (
                <Button
                  variant="outline"
                  className="hover:text-destructive"
                  data-testid="enginegroup-delete"
                  onClick={() => setDeleting(true)}
                >
                  <Trash2 className="mr-1.5 h-4 w-4" />
                  Delete
                </Button>
              )}
            </>
          )
        }
      />
      <ErrorAlert
        error={q.error}
        prefix="Could not load the engine group"
        className="mb-4"
      />
      {q.isPending && <p className="text-muted-foreground text-sm">Loading…</p>}
      {g && (
        <div className="grid gap-6">
          {g.rollouts_paused && (
            <Alert
              data-testid="enginegroup-paused"
              className="border-warning/50"
            >
              <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
                <span className="flex items-center gap-2">
                  <Pause className="text-warning h-4 w-4 shrink-0" />
                  Rollouts are paused after a rollback: configuration changes
                  are held back from this group until you resume.
                </span>
                {canResume && (
                  <Button
                    size="sm"
                    data-testid="enginegroup-resume"
                    disabled={resume.isPending}
                    onClick={() => resume.mutate(g.id)}
                  >
                    <Play className="mr-1.5 h-4 w-4" />
                    {resume.isPending ? "Resuming…" : "Resume rollouts"}
                  </Button>
                )}
              </AlertDescription>
            </Alert>
          )}
          <ErrorAlert error={resume.error} prefix="Could not resume rollouts" />
          <ActiveRollout group={g} />
          <div className="grid gap-6 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
            <RolloutsTable groupId={g.id} />
            <GroupSettings key={g.id} group={g} />
          </div>
          <GroupEngines groupId={g.id} />
        </div>
      )}
      {rollingBack && g && (
        <RollbackDialog group={g} onClose={() => setRollingBack(false)} />
      )}
      {deleting && g && (
        <ConfirmDialog
          title={`Delete ${g.name}?`}
          description="Only an empty engine group can be deleted: move its engines and delete its join tokens and scoped configuration first."
          confirmLabel="Delete engine group"
          pendingLabel="Deleting…"
          thing="This engine group"
          onConfirm={async () => {
            await del.mutateAsync({ id: g.id, revision: g.revision });
            void navigate("/engines");
          }}
          onClose={() => setDeleting(false)}
        />
      )}
    </div>
  );
}

function ActiveRollout({ group: g }: { group: EngineGroup }) {
  const r = g.active_rollout;
  return (
    <Card className="px-5 py-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="grid gap-2">
          <h2 className="text-sm font-semibold">Current rollout</h2>
          {r ? (
            <>
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="font-medium tabular-nums">v{r.version}</span>
                <RolloutStateBadge state={r.state} />
                <span className="text-muted-foreground">
                  {r.kind} · {r.strategy} · started{" "}
                  {formatAgo(r.phase_started_at ?? r.created_at)}
                </span>
              </div>
              <RolloutStages rollout={r} />
            </>
          ) : (
            <p className="text-muted-foreground text-sm">
              No rollout in progress.{" "}
              {g.stable_version
                ? `Every engine should run v${g.stable_version}.`
                : ""}
            </p>
          )}
        </div>
        <div className="flex items-center gap-6">
          <div className="text-sm">
            <div className="text-muted-foreground text-xs">Stable version</div>
            <div className="font-medium tabular-nums">
              {g.stable_version ? `v${g.stable_version}` : "—"}
            </div>
          </div>
          <div className="text-sm">
            <div className="text-muted-foreground text-xs">Strategy</div>
            <div className="font-mono text-[13px]">
              {g.rollout_strategy}
              {g.rollout_strategy === "canary" && (
                <span className="text-muted-foreground ml-1 font-sans text-xs">
                  {canarySize(g)}
                </span>
              )}
            </div>
          </div>
          {r && (
            <>
              <RolloutProgress rollout={r} className="w-48" />
              <LinkButton to={`/engines/rollouts/${r.id}`}>Details</LinkButton>
            </>
          )}
        </div>
      </div>
    </Card>
  );
}

function RolloutsTable({ groupId }: { groupId: string }) {
  const rollouts = useRollouts({ engineGroupId: groupId, limit: 20 });
  const rows = rollouts.data ?? [];
  return (
    <section aria-labelledby="rollouts-heading">
      <h2 id="rollouts-heading" className="mb-3 text-sm font-semibold">
        Rollouts
      </h2>
      <ErrorAlert
        error={rollouts.error}
        prefix="Could not load rollouts"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Version</TableHead>
              <TableHead className="h-10">State</TableHead>
              <TableHead className="h-10">Progress</TableHead>
              <TableHead className="h-10">Created</TableHead>
              <TableHead className="h-10 w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.id}>
                <TableCell className="py-2.5 align-top whitespace-nowrap">
                  <div className="font-medium tabular-nums">v{r.version}</div>
                  <div className="text-muted-foreground text-xs">
                    {r.kind}
                    {r.from_version ? ` of v${r.from_version}` : ""}
                  </div>
                </TableCell>
                <TableCell className="py-2.5 align-top">
                  <RolloutStateBadge state={r.state} />
                </TableCell>
                <TableCell className="py-2.5 align-top">
                  <RolloutProgress rollout={r} className="w-40" />
                </TableCell>
                <TableCell className="py-2.5 align-top text-sm whitespace-nowrap">
                  <span title={formatDateTime(r.created_at)}>
                    {formatAgo(r.created_at)}
                  </span>
                  <div className="text-muted-foreground text-xs">
                    by {r.created_by}
                  </div>
                </TableCell>
                <TableCell className="py-2 text-right align-top">
                  <LinkButton
                    to={`/engines/rollouts/${r.id}`}
                    testId="rollout-open"
                  >
                    Open
                  </LinkButton>
                </TableCell>
              </TableRow>
            ))}
            {rollouts.isPending && (
              <MessageRow colSpan={5}>Loading…</MessageRow>
            )}
            {rollouts.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={5}>No rollouts yet.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
    </section>
  );
}

type UpstreamMode = EngineGroup["upstream_mode"];
type Strategy = EngineGroup["rollout_strategy"];

type SettingsForm = {
  name: string;
  description: string;
  upstream_mode: UpstreamMode;
  extra_acl_cidrs: string;
  otlp_endpoint: string;
  rollout_strategy: Strategy;
  canary_count: string;
  canary_percent: string;
  ack_timeout_seconds: string;
  health_window_seconds: string;
  max_servfail_percent: string;
  min_health_queries: string;
  revision: number;
};

function toSettings(g: EngineGroup): SettingsForm {
  return {
    name: g.name,
    description: g.description,
    upstream_mode: g.upstream_mode,
    extra_acl_cidrs: g.extra_acl_cidrs.join("\n"),
    otlp_endpoint: g.otlp_endpoint,
    rollout_strategy: g.rollout_strategy,
    canary_count: String(g.canary_count),
    canary_percent: String(g.canary_percent),
    ack_timeout_seconds: String(g.ack_timeout_seconds),
    health_window_seconds: String(g.health_window_seconds),
    // Shown as a percentage; rounded so float32 noise (0.05 → 5.000000074505806) stays hidden.
    max_servfail_percent: String(
      Math.round(g.max_servfail_ratio * 10000) / 100,
    ),
    min_health_queries: String(g.min_health_queries),
    revision: g.revision,
  };
}

function GroupSettings({ group }: { group: EngineGroup }) {
  const canUpdate = useCan("updateEngineGroup");
  const update = useUpdateEngineGroup();
  const [form, setForm] = useState<SettingsForm>(() => toSettings(group));
  const set = <K extends keyof SettingsForm>(
    key: K,
    value: SettingsForm[K],
  ) => {
    setForm((f) => ({ ...f, [key]: value }));
    update.reset();
  };
  const disabled = !canUpdate || update.isPending;
  const canary = form.rollout_strategy === "canary";

  function submit(e: FormEvent) {
    e.preventDefault();
    update.mutate(
      {
        id: group.id,
        body: {
          revision: form.revision,
          name: form.name.trim(),
          description: form.description.trim(),
          upstream_mode: form.upstream_mode,
          extra_acl_cidrs: form.extra_acl_cidrs
            .split("\n")
            .map((l) => l.trim())
            .filter((l) => l !== ""),
          otlp_endpoint: form.otlp_endpoint.trim(),
          rollout_strategy: form.rollout_strategy,
          canary_count: Number(form.canary_count),
          canary_percent: Number(form.canary_percent),
          ack_timeout_seconds: Number(form.ack_timeout_seconds),
          health_window_seconds: Number(form.health_window_seconds),
          max_servfail_ratio: Number(form.max_servfail_percent) / 100,
          min_health_queries: Number(form.min_health_queries),
        },
      },
      { onSuccess: (saved) => setForm(toSettings(saved)) },
    );
  }

  return (
    <section aria-labelledby="settings-heading">
      <h2 id="settings-heading" className="mb-3 text-sm font-semibold">
        Settings
      </h2>
      <Card className="p-5">
        <form onSubmit={submit} className="grid gap-4">
          <fieldset disabled={disabled} className="grid gap-4">
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-settings-name">Name</Label>
                <HelpTip id="enginegroup-settings-name" label="Name" />
              </div>
              <Input
                id="enginegroup-settings-name"
                className="font-mono"
                maxLength={63}
                disabled={group.name === "default"}
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-description">Description</Label>
                <HelpTip id="enginegroup-description" label="Description" />
              </div>
              <Input
                id="enginegroup-description"
                data-testid="enginegroup-description"
                maxLength={1024}
                value={form.description}
                onChange={(e) => set("description", e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-settings-upstreams">
                  Upstreams
                </Label>
                <HelpTip
                  id="enginegroup-settings-upstreams"
                  label="Upstreams"
                />
              </div>
              <Select
                value={form.upstream_mode}
                disabled={disabled}
                onValueChange={(v) => set("upstream_mode", v as UpstreamMode)}
              >
                <SelectTrigger
                  id="enginegroup-settings-upstreams"
                  className="h-9"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="inherit">
                    inherit · group upstreams, then global
                  </SelectItem>
                  <SelectItem value="override">
                    override · group upstreams only
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-acl">
                  Extra allowed client CIDRs
                </Label>
                <HelpTip
                  id="enginegroup-acl"
                  label="Extra allowed client CIDRs"
                />
              </div>
              <Textarea
                id="enginegroup-acl"
                className="min-h-16 font-mono text-[13px]"
                placeholder="10.20.0.0/16"
                value={form.extra_acl_cidrs}
                onChange={(e) => set("extra_acl_cidrs", e.target.value)}
              />
              <p className="text-muted-foreground text-xs">
                One prefix per line, added to the global access control list.
              </p>
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-otlp">OTLP endpoint</Label>
                <HelpTip id="enginegroup-otlp" label="OTLP endpoint" />
              </div>
              <Input
                id="enginegroup-otlp"
                className="font-mono"
                placeholder="uses the global endpoint when empty"
                maxLength={512}
                value={form.otlp_endpoint}
                onChange={(e) => set("otlp_endpoint", e.target.value)}
              />
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div className="grid gap-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="enginegroup-settings-strategy">
                    Rollout strategy
                  </Label>
                  <HelpTip
                    id="enginegroup-settings-strategy"
                    label="Rollout strategy"
                  />
                </div>
                <Select
                  value={form.rollout_strategy}
                  disabled={disabled}
                  onValueChange={(v) => set("rollout_strategy", v as Strategy)}
                >
                  <SelectTrigger
                    id="enginegroup-settings-strategy"
                    className="h-9"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all_at_once">all_at_once</SelectItem>
                    <SelectItem value="canary">canary</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <NumberField
                id="enginegroup-ack-timeout"
                data-help="enginegroup-ack-timeout"
                help={
                  <HelpTip id="enginegroup-ack-timeout" label="Apply timeout" />
                }
                label="Apply timeout (s)"
                min={5}
                max={3600}
                value={form.ack_timeout_seconds}
                onChange={(v) => set("ack_timeout_seconds", v)}
              />
            </div>
            {canary && (
              <div className="grid grid-cols-2 gap-4">
                <NumberField
                  id="enginegroup-settings-canary-count"
                  data-help="enginegroup-settings-canary-count"
                  help={
                    <HelpTip
                      id="enginegroup-settings-canary-count"
                      label="Canary engines"
                    />
                  }
                  label="Canary engines"
                  min={0}
                  value={form.canary_count}
                  onChange={(v) => set("canary_count", v)}
                />
                <NumberField
                  id="enginegroup-settings-canary-percent"
                  data-help="enginegroup-settings-canary-percent"
                  help={
                    <HelpTip
                      id="enginegroup-settings-canary-percent"
                      label="Canary percent"
                    />
                  }
                  label="or percent"
                  min={0}
                  max={100}
                  value={form.canary_percent}
                  onChange={(v) => set("canary_percent", v)}
                />
                <NumberField
                  id="enginegroup-health-window"
                  data-help="enginegroup-health-window"
                  help={
                    <HelpTip
                      id="enginegroup-health-window"
                      label="Health window"
                    />
                  }
                  label="Health window (s)"
                  min={20}
                  max={3600}
                  value={form.health_window_seconds}
                  onChange={(v) => set("health_window_seconds", v)}
                />
                <NumberField
                  id="enginegroup-max-servfail"
                  data-help="enginegroup-max-servfail"
                  help={
                    <HelpTip
                      id="enginegroup-max-servfail"
                      label="Max SERVFAIL"
                    />
                  }
                  label="Max SERVFAIL (%)"
                  min={0}
                  max={100}
                  step="any"
                  value={form.max_servfail_percent}
                  onChange={(v) => set("max_servfail_percent", v)}
                />
                <NumberField
                  id="enginegroup-min-queries"
                  data-help="enginegroup-min-queries"
                  help={
                    <HelpTip
                      id="enginegroup-min-queries"
                      label="Minimum queries"
                    />
                  }
                  label="Min. queries for the gate"
                  min={0}
                  value={form.min_health_queries}
                  onChange={(v) => set("min_health_queries", v)}
                />
              </div>
            )}
          </fieldset>
          <ErrorAlert error={update.error} thing="This engine group" />
          {canUpdate && (
            <div className="flex flex-wrap items-center gap-3">
              <Button
                type="submit"
                data-testid="enginegroup-save-settings"
                disabled={disabled}
              >
                {update.isPending ? "Saving…" : "Save settings"}
              </Button>
              <SavedNote show={update.isSuccess}>Saved</SavedNote>
            </div>
          )}
        </form>
      </Card>
    </section>
  );
}

function NumberField({
  id,
  "data-help": dataHelp,
  help,
  label,
  value,
  onChange,
  min,
  max,
  step,
}: {
  id: string;
  "data-help": string;
  help: ReactNode;
  label: string;
  value: string;
  onChange: (v: string) => void;
  min?: number;
  max?: number;
  step?: string;
}) {
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center gap-1.5">
        <Label htmlFor={id}>{label}</Label>
        {help}
      </div>
      <Input
        id={id}
        data-help={dataHelp}
        type="number"
        className="h-9"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}

const groupEnginesShown = 50;

function GroupEngines({ groupId }: { groupId: string }) {
  const engines = useEngines();
  const all = (engines.data ?? [])
    .filter((e) => e.engine_group_id === groupId)
    .sort((a, b) => a.node_name.localeCompare(b.node_name));
  const rows = all.slice(0, groupEnginesShown);
  return (
    <section aria-labelledby="group-engines-heading">
      <div className="mb-3 flex items-end justify-between gap-3">
        <h2 id="group-engines-heading" className="text-sm font-semibold">
          Engines
          <span className="text-muted-foreground ml-2 font-normal tabular-nums">
            {engines.data ? all.length : ""}
          </span>
        </h2>
        {all.length > groupEnginesShown && (
          <LinkButton to={`/engines?group=${groupId}`}>
            All {all.length} in the fleet view
          </LinkButton>
        )}
      </div>
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Node</TableHead>
              <TableHead className="h-10">Status</TableHead>
              <TableHead className="h-10 text-right">
                Applied / target
              </TableHead>
              <TableHead className="h-10">Engine version</TableHead>
              <TableHead className="h-10">Last seen</TableHead>
              <TableHead className="h-10 w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((e) => (
              <TableRow key={e.id}>
                <TableCell className="py-1.5">
                  <EngineModalOpenButton engine={e} />
                </TableCell>
                <TableCell className="py-2.5">
                  <EngineStatusBadge status={e.status} />
                </TableCell>
                <TableCell className="py-2.5 text-right tabular-nums">
                  v{e.applied_version} / v{e.target_version}
                </TableCell>
                <TableCell className="text-muted-foreground py-2.5 font-mono text-[13px]">
                  {e.engine_version || "—"}
                </TableCell>
                <TableCell className="py-2.5 whitespace-nowrap">
                  {e.connected ? "now" : formatAgo(e.last_seen_at)}
                </TableCell>
                <TableCell className="py-2 text-right">
                  <LinkButton to={`/engines/nodes/${e.id}`}>Details</LinkButton>
                </TableCell>
              </TableRow>
            ))}
            {engines.isPending && <MessageRow colSpan={6}>Loading…</MessageRow>}
            {engines.isSuccess && all.length === 0 && (
              <MessageRow colSpan={6}>
                No engines in this group. Move one here from its engine page or
                enrol one with a join token bound to this group.
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      <EngineModal engineIds={rows.map((e) => e.id)} />
    </section>
  );
}

function RollbackDialog({
  group,
  onClose,
}: {
  group: EngineGroup;
  onClose: () => void;
}) {
  const rollouts = useRollouts({ engineGroupId: group.id, limit: 200 });
  const rollback = useRollbackEngineGroup();
  const [version, setVersion] = useState<string>("");
  const all = rollouts.data ?? [];
  const newest = all.reduce((m, r) => Math.max(m, r.version), 0);
  // One option per older version; the list is newest first, so the first rollout of a version is its latest.
  const options = all.filter(
    (r, i) =>
      r.version < newest && all.findIndex((o) => o.version === r.version) === i,
  );

  function submit(e: FormEvent) {
    e.preventDefault();
    rollback.mutate(
      { id: group.id, toVersion: Number(version) },
      { onSuccess: onClose },
    );
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Roll back {group.name}</DialogTitle>
          <DialogDescription>
            The chosen version's configuration is republished to every engine in
            the group at once, and rollouts of later changes pause until you
            resume them.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <ErrorAlert error={rollouts.error} prefix="Could not load versions" />
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="rollback-version">Version</Label>
              <HelpTip id="rollback-version" label="Version" />
            </div>
            <Select value={version} onValueChange={setVersion}>
              <SelectTrigger
                id="rollback-version"
                data-testid="rollback-version"
                className="h-9"
              >
                <SelectValue placeholder="Select a version" />
              </SelectTrigger>
              <SelectContent>
                {options.map((r) => (
                  <SelectItem key={r.id} value={String(r.version)}>
                    v{r.version} · {r.kind} · {r.state.replace("_", " ")} ·{" "}
                    {formatAgo(r.created_at)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {rollouts.isSuccess && options.length === 0 && (
              <p className="text-muted-foreground text-xs">
                This group has no older version to roll back to.
              </p>
            )}
          </div>
          <ErrorAlert error={rollback.error} thing="This engine group" />
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="destructive"
              data-testid="rollback-confirm"
              disabled={version === "" || rollback.isPending}
            >
              {rollback.isPending
                ? "Rolling back…"
                : version
                  ? `Roll back to v${version}`
                  : "Roll back"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
