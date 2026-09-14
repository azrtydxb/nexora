import { useState, type FormEvent } from "react";
import { Ban, RotateCw, Trash2 } from "lucide-react";
import { Link, useNavigate, useParams } from "react-router";
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { type Schemas } from "@/api/client";
import {
  type Engine,
  type StatsWindow,
  useDeleteEngine,
  useEngine,
  useEngineStats,
  useRevokeEngine,
  useRotateEngineCertificate,
  useUpdateEngine,
} from "@/api/fleet";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  Fact,
  formatAgo,
  formatDateTime,
  SavedNote,
  StatusDot,
} from "@/components/common";
import {
  BackLink,
  EngineGroupSelect,
  engineStatusHelp,
  EngineStatusBadge,
  LabelsEditor,
  labelRows,
  labelsFromRows,
  type LabelRow,
} from "@/components/fleet";
import { PageHeader } from "@/components/layout/AppShell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export function EngineDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const q = useEngine(id);
  const canAdmin = {
    rotate: useCan("rotateEngineCertificate"),
    revoke: useCan("revokeEngine"),
    remove: useCan("deleteEngine"),
  };
  const rotate = useRotateEngineCertificate();
  const revoke = useRevokeEngine();
  const remove = useDeleteEngine();
  const [confirm, setConfirm] = useState<"rotate" | "revoke" | "delete" | null>(
    null,
  );
  const e = q.data;
  const revoked = e?.status === "revoked";

  return (
    <div data-testid="engine-detail">
      <BackLink to="/engines">Engines</BackLink>
      <PageHeader
        title={e?.node_name ?? "Engine"}
        description={e ? engineStatusHelp[e.status] : undefined}
        actions={
          e && (
            <>
              {canAdmin.rotate && !revoked && (
                <Button
                  variant="outline"
                  data-testid="engine-rotate"
                  onClick={() => setConfirm("rotate")}
                >
                  <RotateCw className="mr-1.5 h-4 w-4" />
                  Rotate certificate
                </Button>
              )}
              {canAdmin.revoke && !revoked && (
                <Button
                  variant="outline"
                  className="hover:text-destructive"
                  data-testid="engine-revoke"
                  onClick={() => setConfirm("revoke")}
                >
                  <Ban className="mr-1.5 h-4 w-4" />
                  Revoke
                </Button>
              )}
              {canAdmin.remove && (
                <Button
                  variant="outline"
                  className="hover:text-destructive"
                  data-testid="engine-delete"
                  onClick={() => setConfirm("delete")}
                >
                  <Trash2 className="mr-1.5 h-4 w-4" />
                  Remove engine
                </Button>
              )}
            </>
          )
        }
      />
      <ErrorAlert
        error={q.error}
        prefix="Could not load the engine"
        className="mb-4"
      />
      <div className="mb-2 h-5">
        <SavedNote show={rotate.isSuccess}>Rotation requested</SavedNote>
      </div>
      {q.isPending && <p className="text-muted-foreground text-sm">Loading…</p>}
      {e && (
        <div className="grid gap-6">
          <EngineFacts engine={e} />
          <div className="grid gap-6 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
            <EngineStatsCard id={e.id} />
            <EngineAssignment key={e.id} engine={e} />
          </div>
        </div>
      )}
      {confirm === "rotate" && e && (
        <ConfirmDialog
          title={`Rotate the certificate of ${e.node_name}?`}
          description="The engine is asked over its control stream to request a new certificate. It keeps serving; the old certificate stops being accepted once the new one is confirmed."
          confirmLabel="Rotate certificate"
          pendingLabel="Requesting…"
          testId="confirm-rotate"
          destructive={false}
          thing="This engine"
          onConfirm={() => rotate.mutateAsync(e.id)}
          onClose={() => setConfirm(null)}
        />
      )}
      {confirm === "revoke" && e && (
        <ConfirmDialog
          title={`Revoke ${e.node_name}?`}
          description="Every certificate of this engine is revoked and its control streams close at once. It keeps serving its last configuration but gets no further changes. Enrol it again with a new join token to bring it back."
          confirmLabel="Revoke engine"
          pendingLabel="Revoking…"
          testId="confirm-revoke"
          thing="This engine"
          onConfirm={() => revoke.mutateAsync(e.id)}
          onClose={() => setConfirm(null)}
        />
      )}
      {confirm === "delete" && e && (
        <ConfirmDialog
          title={`Remove ${e.node_name}?`}
          description="Its certificate stops being accepted, so the engine loses its control connection. Enrol it again with a new join token to bring it back."
          confirmLabel="Remove engine"
          pendingLabel="Removing…"
          onConfirm={async () => {
            await remove.mutateAsync(e.id);
            void navigate("/engines");
          }}
          onClose={() => setConfirm(null)}
        />
      )}
    </div>
  );
}

function EngineFacts({ engine: e }: { engine: Engine }) {
  const lag = e.target_version - e.applied_version;
  return (
    <Card className="px-5 py-4">
      <dl className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <Fact label="Status">
          <EngineStatusBadge status={e.status} testId="engine-status" />
        </Fact>
        <Fact label="Engine group">
          <Link
            to={`/engines/groups/${e.engine_group_id}`}
            data-testid="engine-group-name"
            className="text-primary font-medium hover:underline"
          >
            {e.engine_group_name}
          </Link>
        </Fact>
        <Fact label="Connection">
          {e.connected ? (
            <StatusDot tone="success">connected</StatusDot>
          ) : (
            <StatusDot tone="muted">
              last seen {formatAgo(e.last_seen_at)}
            </StatusDot>
          )}
        </Fact>
        <Fact label="Last seen">{formatDateTime(e.last_seen_at)}</Fact>
        <Fact label="Applied config version">
          v{e.applied_version}
          {lag > 0 && !e.revoked_at && (
            <span className="text-warning ml-1.5 text-xs">{lag} behind</span>
          )}
        </Fact>
        <Fact label="Target config version">v{e.target_version}</Fact>
        <Fact label="Rejected config version">
          {e.rejected_version ? `v${e.rejected_version}` : "—"}
        </Fact>
        <Fact label="Engine version">
          <span className="font-mono text-xs">{e.engine_version || "—"}</span>
        </Fact>
        <Fact label="Enrolled">{formatDateTime(e.enrolled_at)}</Fact>
        <Fact label="Certificate expires">
          <span title={formatDateTime(e.certificate_not_after)}>
            {e.certificate_not_after ? formatAgo(e.certificate_not_after) : "—"}
          </span>
        </Fact>
        <Fact label="Certificate serial" className="sm:col-span-2">
          <span className="font-mono text-xs break-all">
            {e.certificate_serial || "—"}
          </span>
        </Fact>
        <Fact label="Engine ID" className="sm:col-span-2">
          <span className="font-mono text-xs break-all">{e.id}</span>
        </Fact>
        {e.cert_rotate_requested_at && (
          <Fact label="Certificate rotation asked">
            {formatDateTime(e.cert_rotate_requested_at)}
          </Fact>
        )}
        {e.revoked_at && (
          <Fact label="Revoked">
            <span className="text-destructive">
              {formatDateTime(e.revoked_at)}
            </span>
          </Fact>
        )}
        {e.rejected_reason && (
          <Fact
            label="Rejection reason"
            className="sm:col-span-2 lg:col-span-4"
          >
            <span className="text-destructive">{e.rejected_reason}</span>
          </Fact>
        )}
        {e.persist_error && (
          <Fact label="Persist error" className="sm:col-span-2 lg:col-span-4">
            <span className="text-destructive">{e.persist_error}</span>
          </Fact>
        )}
      </dl>
    </Card>
  );
}

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
});
const clockSeconds = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

const windows: { value: StatsWindow; label: string }[] = [
  { value: "5m", label: "Last 5 minutes" },
  { value: "1h", label: "Last hour" },
  { value: "24h", label: "Last 24 hours" },
];

const axisTick = { fill: "var(--muted-foreground)", fontSize: 11 };

function EngineStatsCard({ id }: { id: string }) {
  const [win, setWin] = useState<StatsWindow>("1h");
  const stats = useEngineStats(id, win);
  const series = (stats.data?.samples ?? []).map((s) => ({
    t: new Date(s.at).getTime(),
    qps: s.qps,
    p99: s.p99_ms,
  }));
  return (
    <Card className="p-5">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Queries per second and p99</h2>
        <Select value={win} onValueChange={(v) => setWin(v as StatsWindow)}>
          <SelectTrigger
            aria-label="Time window"
            data-testid="engine-stats-window"
            className="h-8 w-40"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {windows.map((w) => (
              <SelectItem key={w.value} value={w.value}>
                {w.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <ErrorAlert error={stats.error} prefix="Could not load statistics" />
      <div
        data-testid="engine-stats-chart"
        className="text-muted-foreground relative h-64"
        role="img"
        aria-label="Engine queries per second and p99 latency over time"
      >
        {series.length > 0 ? (
          <ResponsiveContainer width="100%" height="100%">
            <LineChart
              data={series}
              margin={{ top: 4, right: 0, bottom: 0, left: -8 }}
            >
              <CartesianGrid vertical={false} stroke="var(--border)" />
              <XAxis
                dataKey="t"
                type="number"
                scale="time"
                domain={["dataMin", "dataMax"]}
                tickFormatter={(t: number) => clock.format(t)}
                tick={axisTick}
                tickLine={false}
                axisLine={{ stroke: "var(--border)" }}
                minTickGap={48}
              />
              <YAxis
                yAxisId="qps"
                width={48}
                tick={axisTick}
                tickLine={false}
                axisLine={false}
              />
              <YAxis
                yAxisId="p99"
                orientation="right"
                width={48}
                tick={axisTick}
                tickLine={false}
                axisLine={false}
                unit=" ms"
              />
              <Tooltip
                contentStyle={{
                  background: "var(--popover)",
                  border: "1px solid var(--border)",
                  borderRadius: 6,
                  color: "var(--popover-foreground)",
                  fontSize: 12,
                }}
                labelFormatter={(t) => clockSeconds.format(Number(t))}
                formatter={(v, name) =>
                  name === "p99"
                    ? [`${Number(v).toFixed(1)} ms`, "p99 latency"]
                    : [`${Number(v).toFixed(1)} q/s`, "Queries"]
                }
              />
              <Legend
                wrapperStyle={{ fontSize: 12 }}
                formatter={(name) =>
                  name === "p99" ? "p99 latency (ms)" : "Queries per second"
                }
              />
              <Line
                yAxisId="qps"
                type="monotone"
                dataKey="qps"
                stroke="var(--primary)"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
              <Line
                yAxisId="p99"
                type="monotone"
                dataKey="p99"
                stroke="var(--warning)"
                strokeWidth={1.5}
                dot={false}
                isAnimationActive={false}
              />
            </LineChart>
          </ResponsiveContainer>
        ) : (
          <div className="flex h-full items-center justify-center rounded-md border border-dashed text-sm">
            {stats.isPending
              ? "Loading…"
              : "No samples in this window. Connected engines report every few seconds."}
          </div>
        )}
      </div>
      {stats.data?.filter_index && (
        <FilterIndexFacts fi={stats.data.filter_index} />
      )}
    </Card>
  );
}

function FilterIndexFacts({
  fi,
}: {
  fi: NonNullable<Schemas["EngineStats"]["filter_index"]>;
}) {
  return (
    <dl
      data-testid="engine-filter-index"
      className="mt-4 grid grid-cols-2 gap-4 sm:grid-cols-4"
    >
      <Fact label="Filter index memory">
        {formatBytes(fi.bytes)} of {formatBytes(fi.max_bytes)}
      </Fact>
      <Fact label="Filter index names">{fi.entries.toLocaleString()}</Fact>
      <Fact label="Decision time">
        {fi.decision_ns_blocked.toFixed(0)} ns blocked,{" "}
        {fi.decision_ns_clean.toFixed(0)} ns clean ({fi.cpu})
      </Fact>
      <Fact label="Last index build">{fi.build_seconds.toFixed(2)} s</Fact>
    </dl>
  );
}

function formatBytes(n: number): string {
  const units = ["B", "KiB", "MiB", "GiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

type AssignmentForm = { group: string; labels: LabelRow[]; revision: number };

function toForm(e: Engine): AssignmentForm {
  return {
    group: e.engine_group_id,
    labels: labelRows(e.labels),
    revision: e.revision,
  };
}

function EngineAssignment({ engine }: { engine: Engine }) {
  const canUpdate = useCan("updateEngine");
  const update = useUpdateEngine();
  const [form, setForm] = useState<AssignmentForm>(() => toForm(engine));
  const disabled = !canUpdate || !!engine.revoked_at || update.isPending;

  function submit(ev: FormEvent) {
    ev.preventDefault();
    update.mutate(
      {
        id: engine.id,
        body: {
          revision: form.revision,
          engine_group_id: form.group,
          labels: labelsFromRows(form.labels),
        },
      },
      { onSuccess: (saved) => setForm(toForm(saved)) },
    );
  }

  return (
    <Card className="p-5">
      <h2 className="mb-1 text-sm font-semibold">Group and labels</h2>
      <p className="text-muted-foreground mb-4 text-sm">
        Moving the engine hands it its new group's configuration at once.
      </p>
      <form onSubmit={submit} className="grid gap-4">
        <div className="grid gap-1.5">
          <Label htmlFor="engine-group-select">Engine group</Label>
          <EngineGroupSelect
            id="engine-group-select"
            testId="engine-group-select"
            allowAll={false}
            disabled={disabled}
            value={form.group}
            onChange={(v) => {
              if (v !== null) setForm((f) => ({ ...f, group: v }));
              update.reset();
            }}
          />
        </div>
        <div className="grid gap-1.5">
          <div className="text-sm font-medium">Labels</div>
          <LabelsEditor
            rows={form.labels}
            disabled={disabled}
            onChange={(labels) => {
              setForm((f) => ({ ...f, labels }));
              update.reset();
            }}
          />
        </div>
        <ErrorAlert error={update.error} thing="This engine" />
        {canUpdate && (
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" data-testid="engine-save" disabled={disabled}>
              {update.isPending ? "Saving…" : "Save"}
            </Button>
            <SavedNote show={update.isSuccess}>Engine saved</SavedNote>
          </div>
        )}
      </form>
    </Card>
  );
}
