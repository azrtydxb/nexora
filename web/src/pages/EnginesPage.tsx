import { useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDown,
  ArrowUp,
  Ban,
  ChevronLeft,
  ChevronRight,
  KeyRound,
  Pause,
  Plus,
} from "lucide-react";
import { Link, useSearchParams } from "react-router";

import { api, unwrap, type Schemas } from "@/api/client";
import {
  type Engine,
  type EngineGroup,
  useCreateEngineGroup,
  useEngineGroups,
  useEngines,
  useFleetSummary,
} from "@/api/fleet";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatAgo,
  formatDateTime,
  MessageRow,
  SecretValue,
  StatusDot,
} from "@/components/common";
import {
  canarySize,
  EngineGroupSelect,
  engineStatuses,
  EngineStatusBadge,
  LabelsEditor,
  LinkButton,
  labelsFromRows,
  RolloutProgress,
  RolloutStateBadge,
  type LabelRow,
} from "@/components/fleet";
import { EngineModal, EngineModalOpenButton } from "@/components/EngineModal";
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
import { cn } from "@/lib/utils";

type JoinToken = Schemas["JoinToken"];

export function EnginesPage() {
  const canTokens = useCan("listJoinTokens");
  const canCreateGroup = useCan("createEngineGroup");
  const [creatingGroup, setCreatingGroup] = useState(false);

  return (
    <>
      <PageHeader
        title="Engines"
        description="The DNS engines enrolled with this management plane, the engine groups they belong to and how each group's configuration rolls out."
        actions={
          canCreateGroup && (
            <Button
              data-testid="enginegroup-add"
              onClick={() => setCreatingGroup(true)}
            >
              <Plus className="mr-1.5 h-4 w-4" />
              New engine group
            </Button>
          )
        }
      />
      <FleetSummary />
      <EngineGroupsTable />
      <EnginesTable canTokens={canTokens} />
      {canTokens && <JoinTokens />}
      {creatingGroup && (
        <EngineGroupDialog onClose={() => setCreatingGroup(false)} />
      )}
    </>
  );
}

const integer = new Intl.NumberFormat();

/** The most common value and how many items differ from it. */
function drift<T>(items: T[], key: (t: T) => string) {
  const counts = new Map<string, number>();
  for (const i of items) counts.set(key(i), (counts.get(key(i)) ?? 0) + 1);
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const [mode, n] = sorted[0] ?? ["", 0];
  return { mode, differing: items.length - n, values: sorted };
}

function FleetSummary() {
  const summary = useFleetSummary();
  const engines = useEngines();
  const s = summary.data;
  const software = drift(engines.data ?? [], (e) => e.engine_version || "—");

  return (
    <Card data-testid="fleet-summary" className="mb-8 overflow-hidden">
      <ErrorAlert
        error={summary.error}
        prefix="Could not load the fleet summary"
        className="m-4"
      />
      <div className="grid gap-px border-b sm:grid-cols-3">
        <SummaryStat label="Engines">
          <div className="text-2xl font-semibold tabular-nums">
            {s ? integer.format(s.engines_total) : "—"}
          </div>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {s &&
              engineStatuses
                .filter((st) => (s.engines_by_status[st] ?? 0) > 0)
                .map((st) => (
                  <Link
                    key={st}
                    to={`/engines?status=${st}`}
                    className="inline-flex items-center gap-1 text-xs"
                    title={`Show ${st} engines`}
                  >
                    <EngineStatusBadge status={st} />
                    <span className="tabular-nums">
                      {integer.format(s.engines_by_status[st])}
                    </span>
                  </Link>
                ))}
          </div>
        </SummaryStat>
        <SummaryStat label="Halted rollouts">
          <div
            className={cn(
              "text-2xl font-semibold tabular-nums",
              s && s.halted_rollouts > 0 && "text-destructive",
            )}
          >
            {s ? s.halted_rollouts : "—"}
          </div>
          <p className="text-muted-foreground mt-1.5 text-xs">
            {s && s.halted_rollouts > 0
              ? "Open the engine group to roll back or publish a fix."
              : "Every rollout is progressing or done."}
          </p>
        </SummaryStat>
        <SummaryStat label="Engine software">
          <div className="text-2xl font-semibold tabular-nums">
            {engines.data ? software.values.length : "—"}
            <span className="text-muted-foreground ml-1.5 text-sm font-normal">
              {software.values.length === 1 ? "version" : "versions"}
            </span>
          </div>
          <p className="text-muted-foreground mt-1.5 truncate text-xs">
            {software.differing > 0 ? (
              <span className="text-warning">
                {software.differing} not on {software.mode}
              </span>
            ) : software.mode ? (
              <>
                All on <span className="font-mono">{software.mode}</span>
              </>
            ) : (
              "No engines yet."
            )}
          </p>
        </SummaryStat>
      </div>
      <ul className="divide-y">
        {(s?.engine_groups ?? []).map((g) => (
          <li
            key={g.id}
            className="grid items-center gap-x-6 gap-y-2 px-5 py-3 text-sm sm:grid-cols-[minmax(8rem,1fr)_8rem_7rem_minmax(14rem,2fr)]"
          >
            <Link
              to={`/engines/groups/${g.id}`}
              className="text-primary truncate font-medium hover:underline"
            >
              {g.name}
            </Link>
            <span className="tabular-nums">
              <StatusDot
                tone={
                  g.engines === 0
                    ? "muted"
                    : g.connected === g.engines
                      ? "success"
                      : g.connected === 0
                        ? "destructive"
                        : "warning"
                }
              >
                {g.connected} / {g.engines} connected
              </StatusDot>
            </span>
            <span className="text-muted-foreground tabular-nums">
              stable {g.stable_version ? `v${g.stable_version}` : "—"}
            </span>
            <span className="flex flex-wrap items-center gap-3">
              {g.rollouts_paused && (
                <span className="text-warning inline-flex items-center gap-1 text-xs">
                  <Pause className="h-3.5 w-3.5" />
                  paused
                </span>
              )}
              {g.active_rollout ? (
                <Link
                  to={`/engines/rollouts/${g.active_rollout.id}`}
                  className="flex items-center gap-3"
                >
                  <RolloutStateBadge state={g.active_rollout.state} />
                  <RolloutProgress
                    rollout={g.active_rollout}
                    className="w-40"
                  />
                </Link>
              ) : (
                <span className="text-muted-foreground text-xs">
                  No rollout in progress
                </span>
              )}
            </span>
          </li>
        ))}
        {summary.isPending && (
          <li className="text-muted-foreground px-5 py-6 text-center text-sm">
            Loading…
          </li>
        )}
      </ul>
    </Card>
  );
}

function SummaryStat({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="bg-card px-5 py-4">
      <div className="text-muted-foreground mb-1 text-xs font-medium">
        {label}
      </div>
      {children}
    </div>
  );
}

function EngineGroupsTable() {
  const groups = useEngineGroups({ live: true });
  const rows = [...(groups.data ?? [])].sort((a, b) =>
    a.name.localeCompare(b.name),
  );
  return (
    <section aria-labelledby="engine-groups-heading" className="mb-10">
      <h2 id="engine-groups-heading" className="mb-1 text-sm font-semibold">
        Engine groups
      </h2>
      <p className="text-muted-foreground mb-3 text-sm">
        Each group gets its own configuration snapshot and rolls it out with its
        own strategy.
      </p>
      <ErrorAlert
        error={groups.error}
        prefix="Could not load engine groups"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Name</TableHead>
              <TableHead className="h-10">Strategy</TableHead>
              <TableHead className="h-10">Upstreams</TableHead>
              <TableHead className="h-10 text-right">Engines</TableHead>
              <TableHead className="h-10 text-right">Stable version</TableHead>
              <TableHead className="h-10">Rollout</TableHead>
              <TableHead className="h-10 w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((g) => (
              <TableRow key={g.id} data-testid={`enginegroup-row-${g.name}`}>
                <TableCell className="py-2.5">
                  <div className="font-medium">{g.name}</div>
                  {g.description && (
                    <div className="text-muted-foreground max-w-xs truncate text-xs">
                      {g.description}
                    </div>
                  )}
                </TableCell>
                <TableCell className="py-2.5 font-mono text-[13px]">
                  {g.rollout_strategy}
                  {g.rollout_strategy === "canary" && (
                    <span className="text-muted-foreground ml-1.5 font-sans text-xs">
                      {canarySize(g)}
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-2.5 text-sm">
                  {g.upstream_mode}
                </TableCell>
                <TableCell className="py-2.5 text-right tabular-nums">
                  {g.engine_count}
                </TableCell>
                <TableCell className="py-2.5 text-right tabular-nums">
                  {g.stable_version ? `v${g.stable_version}` : "—"}
                </TableCell>
                <TableCell className="py-2.5">
                  <span className="flex items-center gap-2">
                    {g.active_rollout ? (
                      <RolloutStateBadge state={g.active_rollout.state} />
                    ) : (
                      <span className="text-muted-foreground text-sm">—</span>
                    )}
                    {g.rollouts_paused && (
                      <span className="text-warning text-xs">paused</span>
                    )}
                  </span>
                </TableCell>
                <TableCell className="py-2 text-right">
                  <LinkButton
                    to={`/engines/groups/${g.id}`}
                    testId={`enginegroup-open-${g.name}`}
                  >
                    Open
                  </LinkButton>
                </TableCell>
              </TableRow>
            ))}
            {groups.isPending && <MessageRow colSpan={7}>Loading…</MessageRow>}
          </TableBody>
        </Table>
      </Card>
    </section>
  );
}

type SortKey = "node" | "group" | "status" | "config" | "software" | "seen";

const statusRank: Record<Engine["status"], number> = {
  rejected: 0,
  revoked: 1,
  disconnected: 2,
  behind: 3,
  ahead: 4,
  current: 5,
};

const sorters: Record<SortKey, (a: Engine, b: Engine) => number> = {
  node: (a, b) => a.node_name.localeCompare(b.node_name),
  group: (a, b) => a.engine_group_name.localeCompare(b.engine_group_name),
  status: (a, b) => statusRank[a.status] - statusRank[b.status],
  config: (a, b) =>
    a.target_version -
    a.applied_version -
    (b.target_version - b.applied_version),
  software: (a, b) =>
    a.engine_version.localeCompare(b.engine_version, undefined, {
      numeric: true,
    }),
  seen: (a, b) =>
    new Date(a.last_seen_at ?? 0).getTime() -
    new Date(b.last_seen_at ?? 0).getTime(),
};

const pageSize = 50;

function matches(e: Engine, q: string): boolean {
  if (q === "") return true;
  return (
    e.node_name.toLowerCase().includes(q) ||
    e.id.startsWith(q) ||
    e.engine_version.toLowerCase().includes(q) ||
    Object.entries(e.labels).some(([k, v]) =>
      `${k}=${v}`.toLowerCase().includes(q),
    )
  );
}

function EnginesTable({ canTokens }: { canTokens: boolean }) {
  const engines = useEngines();
  // Filters live in the URL so the fleet view survives opening an engine and going back.
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const group = params.get("group");
  const status = params.get("status") ?? "all";
  const sort = (params.get("sort") as SortKey | null) ?? "status";
  const desc = params.get("dir") === "desc";
  const page = Math.max(0, Number(params.get("page") ?? 0) || 0);

  function update(patch: Record<string, string | null>, keepPage = false) {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v === null || v === "") next.delete(k);
      else next.set(k, v);
    }
    if (!keepPage) next.delete("page");
    setParams(next, { replace: true });
  }

  const all = engines.data ?? [];
  const software = drift(all, (e) => e.engine_version);
  const needle = q.trim().toLowerCase();
  const filtered = all
    .filter(
      (e) =>
        (group === null || e.engine_group_id === group) &&
        (status === "all" || e.status === status) &&
        matches(e, needle),
    )
    .sort((a, b) => {
      const c = sorters[sort](a, b) || a.node_name.localeCompare(b.node_name);
      return desc ? -c : c;
    });
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const current = Math.min(page, pages - 1);
  const rows = filtered.slice(current * pageSize, (current + 1) * pageSize);

  function header(
    key: SortKey,
    label: string,
    className?: string,
    tip?: ReactNode,
  ) {
    const active = sort === key;
    return (
      <TableHead
        className={cn("h-10", className)}
        aria-sort={active ? (desc ? "descending" : "ascending") : "none"}
      >
        <button
          type="button"
          className="hover:text-foreground inline-flex items-center gap-1"
          onClick={() =>
            update({ sort: key, dir: active && !desc ? "desc" : null })
          }
        >
          {label}
          {active &&
            (desc ? (
              <ArrowDown className="h-3.5 w-3.5" />
            ) : (
              <ArrowUp className="h-3.5 w-3.5" />
            ))}
        </button>
        {tip && <span className="ml-1 align-middle">{tip}</span>}
      </TableHead>
    );
  }

  return (
    <section aria-labelledby="engines-heading">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="engines-heading" className="text-sm font-semibold">
            Engines
          </h2>
          <p className="text-muted-foreground mt-0.5 text-sm">
            {engines.data
              ? `${integer.format(filtered.length)} of ${integer.format(all.length)} engines`
              : "Loading…"}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="search"
            aria-label="Search engines"
            data-testid="engine-search"
            placeholder="Node, label or version"
            className="h-9 w-56"
            value={q}
            onChange={(e) => update({ q: e.target.value })}
          />
          <EngineGroupSelect
            testId="engine-filter-group"
            className="w-48"
            value={group}
            onChange={(v) => update({ group: v })}
          />
          <Select value={status} onValueChange={(v) => update({ status: v })}>
            <SelectTrigger
              aria-label="Status"
              data-testid="engine-filter-status"
              className="h-9 w-40"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All statuses</SelectItem>
              {engineStatuses.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <ErrorAlert
        error={engines.error}
        prefix="Could not load engines"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {header("node", "Node")}
              {header("group", "Engine group")}
              {header(
                "status",
                "Status",
                undefined,
                <HelpTip id="engines-col-status" label="Status" />,
              )}
              {header("config", "Config version", "text-right")}
              {header("software", "Engine version")}
              {header("seen", "Last seen")}
              <TableHead className="h-10">Problem</TableHead>
              <TableHead className="h-10 w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((e) => {
              const lag = e.target_version - e.applied_version;
              return (
                <TableRow key={e.id} data-testid={`engine-row-${e.node_name}`}>
                  <TableCell className="py-1.5 whitespace-nowrap">
                    <EngineModalOpenButton engine={e} />
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    {e.engine_group_name}
                  </TableCell>
                  <TableCell className="py-2.5">
                    <EngineStatusBadge status={e.status} />
                  </TableCell>
                  <TableCell
                    className="py-2.5 text-right whitespace-nowrap tabular-nums"
                    title={`Applied v${e.applied_version}, target v${e.target_version}`}
                  >
                    v{e.applied_version || "—"}
                    {lag > 0 && e.status !== "revoked" && (
                      <span className="text-warning ml-1.5 text-xs">
                        {lag} behind v{e.target_version}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="py-2.5 font-mono text-[13px] whitespace-nowrap">
                    <span
                      className={cn(
                        software.values.length > 1 &&
                          e.engine_version !== software.mode
                          ? "text-warning"
                          : "text-muted-foreground",
                      )}
                      title={
                        e.engine_version !== software.mode
                          ? `Most engines run ${software.mode}`
                          : undefined
                      }
                    >
                      {e.engine_version || "—"}
                    </span>
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    <span title={formatDateTime(e.last_seen_at)}>
                      {e.connected ? "now" : formatAgo(e.last_seen_at)}
                    </span>
                  </TableCell>
                  <TableCell
                    className="text-destructive max-w-[16rem] truncate py-2.5 text-sm"
                    title={problem(e)}
                  >
                    {problem(e)}
                  </TableCell>
                  <TableCell className="py-2 text-right">
                    <LinkButton
                      to={`/engines/nodes/${e.id}`}
                      testId={`engine-open-${e.node_name}`}
                    >
                      Details
                    </LinkButton>
                  </TableCell>
                </TableRow>
              );
            })}
            {engines.isPending && <MessageRow colSpan={8}>Loading…</MessageRow>}
            {engines.isSuccess && all.length === 0 && (
              <MessageRow colSpan={8}>
                No engines enrolled yet.{" "}
                {canTokens
                  ? "Create a join token below and start an engine with it."
                  : "An administrator can create a join token to enrol one."}
              </MessageRow>
            )}
            {engines.isSuccess && all.length > 0 && filtered.length === 0 && (
              <MessageRow colSpan={8}>No engines match the filters.</MessageRow>
            )}
          </TableBody>
        </Table>
        {pages > 1 && (
          <div className="flex items-center justify-between gap-3 border-t px-4 py-2 text-sm">
            <span className="text-muted-foreground tabular-nums">
              {current * pageSize + 1}–
              {Math.min((current + 1) * pageSize, filtered.length)} of{" "}
              {integer.format(filtered.length)}
            </span>
            <span className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                aria-label="Previous page"
                disabled={current === 0}
                onClick={() => update({ page: String(current - 1) }, true)}
              >
                <ChevronLeft className="h-4 w-4" />
              </Button>
              <span className="tabular-nums">
                Page {current + 1} of {pages}
              </span>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                aria-label="Next page"
                disabled={current >= pages - 1}
                onClick={() => update({ page: String(current + 1) }, true)}
              >
                <ChevronRight className="h-4 w-4" />
              </Button>
            </span>
          </div>
        )}
      </Card>
      <EngineModal engineIds={rows.map((e) => e.id)} />
    </section>
  );
}

export function problem(e: Engine): string {
  if (e.status === "rejected" && e.rejected_reason)
    return `v${e.rejected_version}: ${e.rejected_reason}`;
  return e.persist_error;
}

type Strategy = EngineGroup["rollout_strategy"];
type UpstreamMode = EngineGroup["upstream_mode"];

function EngineGroupDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateEngineGroup();
  const [form, setForm] = useState({
    name: "",
    description: "",
    upstream_mode: "inherit" as UpstreamMode,
    rollout_strategy: "all_at_once" as Strategy,
    canary_count: "1",
    canary_percent: "0",
  });
  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const canary = form.rollout_strategy === "canary";

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate(
      {
        name: form.name.trim(),
        description: form.description.trim(),
        upstream_mode: form.upstream_mode,
        rollout_strategy: form.rollout_strategy,
        ...(canary && {
          canary_count: Number(form.canary_count),
          canary_percent: Number(form.canary_percent),
        }),
      },
      { onSuccess: onClose },
    );
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New engine group</DialogTitle>
          <DialogDescription>
            A new group starts with a copy of the configuration. Move engines
            into it or enrol them with a join token bound to it.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <ErrorAlert error={create.error} thing="This engine group" />
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="enginegroup-name">Name</Label>
              <HelpTip id="enginegroup-name" label="Name" />
            </div>
            <Input
              id="enginegroup-name"
              data-testid="enginegroup-name"
              className="font-mono"
              required
              maxLength={63}
              placeholder="edge-eu"
              aria-describedby="enginegroup-name-hint"
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
            <p
              id="enginegroup-name-hint"
              className="text-muted-foreground text-xs"
            >
              Lowercase letters, digits and dashes.
            </p>
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="enginegroup-new-description">Description</Label>
              <HelpTip id="enginegroup-new-description" label="Description" />
            </div>
            <Input
              id="enginegroup-new-description"
              maxLength={1024}
              value={form.description}
              onChange={(e) => set("description", e.target.value)}
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-upstream-mode">Upstreams</Label>
                <HelpTip id="enginegroup-upstream-mode" label="Upstreams" />
              </div>
              <Select
                value={form.upstream_mode}
                onValueChange={(v) => set("upstream_mode", v as UpstreamMode)}
              >
                <SelectTrigger id="enginegroup-upstream-mode" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="inherit">
                    inherit · group, then global
                  </SelectItem>
                  <SelectItem value="override">
                    override · group only
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="enginegroup-strategy">Rollout strategy</Label>
                <HelpTip id="enginegroup-strategy" label="Rollout strategy" />
              </div>
              <Select
                value={form.rollout_strategy}
                onValueChange={(v) => set("rollout_strategy", v as Strategy)}
              >
                <SelectTrigger
                  id="enginegroup-strategy"
                  data-testid="enginegroup-strategy"
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
          </div>
          {canary && (
            <div className="grid grid-cols-2 gap-4">
              <div className="grid gap-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="enginegroup-canary-count">
                    Canary engines
                  </Label>
                  <HelpTip
                    id="enginegroup-canary-count"
                    label="Canary engines"
                  />
                </div>
                <Input
                  id="enginegroup-canary-count"
                  type="number"
                  min={0}
                  value={form.canary_count}
                  onChange={(e) => set("canary_count", e.target.value)}
                />
              </div>
              <div className="grid gap-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="enginegroup-canary-percent">
                    or percent of the group
                  </Label>
                  <HelpTip
                    id="enginegroup-canary-percent"
                    label="Canary percent"
                  />
                </div>
                <Input
                  id="enginegroup-canary-percent"
                  type="number"
                  min={0}
                  max={100}
                  value={form.canary_percent}
                  onChange={(e) => set("canary_percent", e.target.value)}
                />
              </div>
            </div>
          )}
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="enginegroup-save"
              disabled={create.isPending}
            >
              {create.isPending ? "Creating…" : "Create group"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const tokenTone: Record<
  JoinToken["state"],
  "success" | "destructive" | "muted"
> = {
  active: "success",
  revoked: "destructive",
  expired: "muted",
  exhausted: "muted",
};

function JoinTokens() {
  const qc = useQueryClient();
  const canCreate = useCan("createJoinToken");
  const canRevoke = useCan("revokeJoinToken");
  const tokens = useQuery({
    queryKey: ["join-tokens"],
    queryFn: async () => unwrap(await api.GET("/join-tokens")),
  });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<JoinToken | null>(null);
  const rows = tokens.data ?? [];
  const cols = 6 + (canRevoke ? 1 : 0);

  return (
    <section aria-labelledby="join-tokens-heading" className="mt-10">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="join-tokens-heading" className="text-sm font-semibold">
            Join tokens
          </h2>
          <p className="text-muted-foreground mt-0.5 text-sm">
            An engine presents a join token to enrol and receive its
            certificate; it joins the token's engine group with its labels.
          </p>
        </div>
        {canCreate && (
          <Button
            variant="outline"
            data-testid="jointoken-add"
            onClick={() => setCreating(true)}
          >
            <KeyRound className="mr-1.5 h-4 w-4" />
            Create join token
          </Button>
        )}
      </div>
      <ErrorAlert
        error={tokens.error}
        prefix="Could not load join tokens"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Name</TableHead>
              <TableHead className="h-10">Engine group</TableHead>
              <TableHead className="h-10">State</TableHead>
              <TableHead className="h-10 text-right">Uses</TableHead>
              <TableHead className="h-10">Created</TableHead>
              <TableHead className="h-10">Expires</TableHead>
              {canRevoke && <TableHead className="h-10 w-16" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((t) => (
              <TableRow key={t.id} data-testid={`jointoken-row-${t.name}`}>
                <TableCell className="py-2.5">
                  <div className="font-medium">{t.name}</div>
                  {Object.keys(t.labels).length > 0 && (
                    <div className="text-muted-foreground font-mono text-xs">
                      {Object.entries(t.labels)
                        .map(([k, v]) => `${k}=${v}`)
                        .join(", ")}
                    </div>
                  )}
                </TableCell>
                <TableCell className="py-2.5">{t.engine_group_name}</TableCell>
                <TableCell className="py-2.5">
                  <StatusDot tone={tokenTone[t.state]}>{t.state}</StatusDot>
                </TableCell>
                <TableCell className="py-2.5 text-right tabular-nums">
                  {t.uses}
                  {t.max_uses != null && (
                    <span className="text-muted-foreground">
                      {" "}
                      / {t.max_uses}
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-2.5 whitespace-nowrap">
                  {formatDateTime(t.created_at)}
                  <span className="text-muted-foreground">
                    {" "}
                    by {t.created_by}
                  </span>
                </TableCell>
                <TableCell className="py-2.5 whitespace-nowrap">
                  <span title={formatDateTime(t.expires_at)}>
                    {formatAgo(t.expires_at)}
                  </span>
                </TableCell>
                {canRevoke && (
                  <TableCell className="py-1.5 text-right">
                    {t.state === "active" && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="hover:text-destructive h-8 w-8"
                        data-testid={`jointoken-revoke-${t.name}`}
                        aria-label={`Revoke ${t.name}`}
                        title="Revoke"
                        onClick={() => setRevoking(t)}
                      >
                        <Ban className="h-4 w-4" />
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {tokens.isPending && (
              <MessageRow colSpan={cols}>Loading…</MessageRow>
            )}
            {tokens.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>No join tokens.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {creating && <JoinTokenDialog onClose={() => setCreating(false)} />}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          description="Engines can no longer enrol with this token. Engines already enrolled keep working."
          confirmLabel="Revoke token"
          pendingLabel="Revoking…"
          onConfirm={async () => {
            unwrap(
              await api.DELETE("/join-tokens/{id}", {
                params: { path: { id: revoking.id } },
              }),
            );
            await qc.invalidateQueries({ queryKey: ["join-tokens"] });
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </section>
  );
}

const ttlOptions = [
  { value: "3600", label: "1 hour" },
  { value: "86400", label: "24 hours" },
  { value: "604800", label: "7 days" },
  { value: "2592000", label: "30 days" },
];

function JoinTokenDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const groups = useEngineGroups();
  const [name, setName] = useState("");
  const [ttl, setTtl] = useState("86400");
  const [group, setGroup] = useState<string | null>(null);
  const [maxUses, setMaxUses] = useState("");
  const [labels, setLabels] = useState<LabelRow[]>([]);
  const defaultGroup = groups.data?.find((g) => g.name === "default")?.id;
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/join-tokens", {
          body: {
            name: name.trim(),
            ttl_seconds: Number(ttl),
            ...(group !== null && { engine_group_id: group }),
            ...(maxUses.trim() !== "" && { max_uses: Number(maxUses) }),
            ...(labels.length > 0 && { labels: labelsFromRows(labels) }),
          },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["join-tokens"] }),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        {create.data ? (
          <>
            <DialogHeader>
              <DialogTitle>Join token created</DialogTitle>
              <DialogDescription>
                Copy it now: it is shown only once. Put it in the engine's{" "}
                <code className="font-mono text-xs">join_token_file</code>.
              </DialogDescription>
            </DialogHeader>
            <SecretValue value={create.data.token} testId="jointoken-value" />
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create join token</DialogTitle>
              <DialogDescription>
                A token enrols engines into its engine group until it expires,
                reaches its use limit or is revoked.
              </DialogDescription>
            </DialogHeader>
            <form onSubmit={submit} className="grid gap-4">
              <ErrorAlert error={create.error} />
              <div className="grid grid-cols-[1fr_10rem] gap-4">
                <div className="grid gap-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label htmlFor="jointoken-name">Name</Label>
                    <HelpTip id="jointoken-name" label="Name" />
                  </div>
                  <Input
                    id="jointoken-name"
                    data-testid="jointoken-name"
                    required
                    maxLength={64}
                    placeholder="rack-3 engines"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label htmlFor="jointoken-ttl">Valid for</Label>
                    <HelpTip id="jointoken-ttl" label="Valid for" />
                  </div>
                  <Select value={ttl} onValueChange={setTtl}>
                    <SelectTrigger
                      id="jointoken-ttl"
                      data-testid="jointoken-ttl"
                      className="h-9"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {ttlOptions.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div className="grid grid-cols-[1fr_10rem] gap-4">
                <div className="grid gap-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label htmlFor="jointoken-engine-group">Engine group</Label>
                    <HelpTip id="jointoken-engine-group" label="Engine group" />
                  </div>
                  <EngineGroupSelect
                    id="jointoken-engine-group"
                    testId="jointoken-engine-group"
                    allowAll={false}
                    value={group ?? defaultGroup ?? null}
                    onChange={setGroup}
                  />
                </div>
                <div className="grid gap-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label htmlFor="jointoken-max-uses">Max uses</Label>
                    <HelpTip id="jointoken-max-uses" label="Max uses" />
                  </div>
                  <Input
                    id="jointoken-max-uses"
                    data-testid="jointoken-max-uses"
                    type="number"
                    min={1}
                    max={100000}
                    placeholder="unlimited"
                    value={maxUses}
                    onChange={(e) => setMaxUses(e.target.value)}
                  />
                </div>
              </div>
              <div className="grid gap-1.5">
                <div
                  className="flex items-center gap-1.5 text-sm font-medium"
                  data-help="jointoken-labels"
                >
                  Labels
                  <HelpTip id="jointoken-labels" label="Labels" />
                </div>
                <LabelsEditor
                  rows={labels}
                  onChange={setLabels}
                  testIdPrefix="jointoken-label"
                />
              </div>
              <DialogFooter className="gap-2 pt-2">
                <Button type="button" variant="outline" onClick={onClose}>
                  Cancel
                </Button>
                <Button
                  type="submit"
                  data-testid="jointoken-save"
                  disabled={create.isPending}
                >
                  <Plus className="mr-1.5 h-4 w-4" />
                  {create.isPending ? "Creating…" : "Create token"}
                </Button>
              </DialogFooter>
            </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
