import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { api, unwrap, type Schemas } from "@/api/client";
import {
  type DashboardRange,
  dashboardRanges,
  useDashboardHealth,
  useDashboardSeries,
  useDashboardTop,
} from "@/api/dashboard";
import type { Engine } from "@/api/fleet";
import { ErrorAlert, MessageRow, StatusDot } from "@/components/common";
import { EngineStatusBadge, engineStatuses } from "@/components/fleet";
import { PageHeader } from "@/components/layout/AppShell";
import { Card } from "@/components/ui/card";
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

type SeriesPoint = Schemas["DashboardSeries"]["points"][number];

const integer = new Intl.NumberFormat();
const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
});
const clockSeconds = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
const day = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
});
const dayClock = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

const rangeLabels: Record<DashboardRange, string> = {
  "15m": "Last 15 minutes",
  "1h": "Last hour",
  "6h": "Last 6 hours",
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
};

const rangeMs: Record<DashboardRange, number> = {
  "15m": 15 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
  "24h": 24 * 60 * 60_000,
  "7d": 7 * 24 * 60 * 60_000,
};

// The categorical series palette, in fixed slot order, with a light and a dark step per slot
// (validated for colour-vision deficiency on both surfaces). `light-dark()` follows the page's
// `color-scheme`, which the theme sets on <html>, so no chart re-renders on a theme switch.
const series = [
  "light-dark(#2a78d6, #3987e5)",
  "light-dark(#eb6834, #d95926)",
  "light-dark(#1baf7a, #199e70)",
  "light-dark(#eda100, #c98500)",
  "light-dark(#e87ba4, #d55181)",
  "light-dark(#008300, #008300)",
  "light-dark(#4a3aa7, #9085e9)",
  "light-dark(#e34948, #e66767)",
];
// Series beyond the palette fold into one neutral "Other" series.
const other = "var(--muted-foreground)";

const transports = ["udp", "tcp", "dot", "doh", "doq"];
const transportLabels: Record<string, string> = {
  udp: "UDP",
  tcp: "TCP",
  dot: "DoT",
  doh: "DoH",
  doq: "DoQ",
};
const rcodes = [
  "NOERROR",
  "NXDOMAIN",
  "SERVFAIL",
  "REFUSED",
  "FORMERR",
  "NOTIMP",
  "other",
];
const routes = [
  "cache",
  "forwarded",
  "recursive",
  "authoritative",
  "forward_zone",
  "blocked",
  "rewritten",
  "rpz",
];
const routeLabels: Record<string, string> = {
  cache: "Cache",
  forwarded: "Forwarded",
  recursive: "Recursive",
  authoritative: "Authoritative",
  forward_zone: "Forward zone",
  blocked: "Blocked",
  rewritten: "Rewritten",
  rpz: "RPZ",
};

export function DashboardPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("range");
  const range: DashboardRange = dashboardRanges.includes(raw as DashboardRange)
    ? (raw as DashboardRange)
    : "1h";
  // Auto refresh is a URL parameter too, so a paused view survives a reload or a shared link.
  const live = params.get("refresh") !== "off";

  function update(key: string, value: string | null) {
    const next = new URLSearchParams(params);
    if (value === null) next.delete(key);
    else next.set(key, value);
    setParams(next, { replace: true });
  }

  const q = useQuery({
    queryKey: ["dashboard"],
    queryFn: async () => unwrap(await api.GET("/dashboard")),
    refetchInterval: live ? 10_000 : false,
  });
  const seriesQ = useDashboardSeries(range, { live });
  const topQ = useDashboardTop(range, { live });
  const healthQ = useDashboardHealth({ live });

  const d = q.data;
  const recent = (d?.series ?? []).map((p) => ({
    t: new Date(p.at).getTime(),
    qps: p.qps,
  }));
  const upstreams = [...(d?.upstreams ?? [])].sort((a, b) =>
    a.name.localeCompare(b.name),
  );
  const dash = "—";

  return (
    <>
      <PageHeader
        title="Dashboard"
        description="Traffic, latency, filtering and health across all resolvers. Pick a time range; charts refresh automatically."
        actions={
          <div className="flex flex-wrap items-center gap-3">
            <Select value={range} onValueChange={(v) => update("range", v)}>
              <SelectTrigger
                id="dashboard-range"
                data-testid="dashboard-range"
                aria-label="Time range"
                className="h-9 w-44"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {dashboardRanges.map((r) => (
                  <SelectItem
                    key={r}
                    value={r}
                    data-testid={`dashboard-range-${r}`}
                  >
                    {rangeLabels[r]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <div className="flex items-center gap-2">
              <Switch
                id="dashboard-auto-refresh"
                data-testid="dashboard-auto-refresh"
                checked={live}
                onCheckedChange={(on) => update("refresh", on ? null : "off")}
              />
              <Label htmlFor="dashboard-auto-refresh" className="text-sm">
                Auto refresh
              </Label>
            </div>
          </div>
        }
      />
      <ErrorAlert
        error={q.error}
        prefix="Could not load the dashboard"
        className="mb-4"
      />
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat
          label="Queries per second"
          testId="dashboard-qps"
          value={d ? integer.format(Math.round(d.qps)) : dash}
        />
        <Stat
          label="Cache hit ratio"
          testId="dashboard-cache-hit-ratio"
          value={d ? `${(d.cache_hit_ratio * 100).toFixed(1)}%` : dash}
        />
        <Stat
          label="Engines connected"
          testId="dashboard-engines"
          value={d ? `${d.engines_connected} / ${d.engines_total}` : dash}
          note={
            d && d.engines_connected < d.engines_total ? (
              <StatusDot tone="warning">
                {d.engines_total - d.engines_connected} disconnected
              </StatusDot>
            ) : undefined
          }
        />
        <Stat
          label="Blocked"
          value={d ? integer.format(d.blocked_total) : dash}
          note={
            d && (
              <span className="text-muted-foreground text-xs">
                of {integer.format(d.queries_total)} queries
              </span>
            )
          }
        />
      </div>

      <div className="mb-4 grid gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <Card className="min-w-0 p-5">
          <div className="mb-4 flex items-baseline justify-between gap-2">
            <h2 className="text-sm font-semibold">Queries per second</h2>
            <span className="text-muted-foreground text-xs">
              Last 5 minutes
            </span>
          </div>
          <div
            data-testid="dashboard-chart"
            className="text-muted-foreground relative h-64"
            role="img"
            aria-label="Queries per second over the last 5 minutes"
          >
            {recent.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <LineChart
                  data={recent}
                  margin={{ top: 4, right: 8, bottom: 0, left: -8 }}
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
                    width={48}
                    allowDecimals={false}
                    tick={axisTick}
                    tickLine={false}
                    axisLine={false}
                  />
                  <Tooltip
                    cursor={cursor}
                    contentStyle={tooltipStyle}
                    labelFormatter={(t) => clockSeconds.format(Number(t))}
                    formatter={(v) => [
                      `${Number(v).toFixed(1)} q/s`,
                      "Queries",
                    ]}
                  />
                  <Line
                    type="monotone"
                    dataKey="qps"
                    stroke="var(--primary)"
                    strokeWidth={2}
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 2, stroke: "var(--card)" }}
                    isAnimationActive={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            ) : (
              <div className="flex h-full items-center justify-center rounded-md border border-dashed text-sm">
                {q.isPending
                  ? "Loading…"
                  : "No samples yet. Engines report every few seconds once connected."}
              </div>
            )}
          </div>
        </Card>

        <Card className="min-w-0 overflow-hidden">
          <div className="px-5 pt-5 pb-3">
            <h2 className="text-sm font-semibold">Upstream health</h2>
          </div>
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-9 pl-5">Upstream</TableHead>
                <TableHead className="h-9">Up on</TableHead>
                <TableHead className="h-9 pr-5 text-right">RTT</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {upstreams.map((u) => (
                <TableRow
                  key={u.name}
                  data-testid={`dashboard-upstream-${u.name}`}
                >
                  <TableCell className="py-2.5 pl-5 font-medium">
                    {u.name}
                  </TableCell>
                  <TableCell className="py-2.5">
                    <StatusDot
                      tone={
                        u.total_engines === 0
                          ? "muted"
                          : u.up_engines === u.total_engines
                            ? "success"
                            : u.up_engines === 0
                              ? "destructive"
                              : "warning"
                      }
                    >
                      {u.up_engines} / {u.total_engines} engines
                    </StatusDot>
                  </TableCell>
                  <TableCell className="py-2.5 pr-5 text-right tabular-nums">
                    {u.rtt_ms > 0 ? `${u.rtt_ms.toFixed(1)} ms` : "—"}
                  </TableCell>
                </TableRow>
              ))}
              {q.isPending && <MessageRow colSpan={3}>Loading…</MessageRow>}
              {q.isSuccess && upstreams.length === 0 && (
                <MessageRow colSpan={3}>No upstream reports yet.</MessageRow>
              )}
            </TableBody>
          </Table>
        </Card>
      </div>

      <SeriesSections q={seriesQ} range={range} />

      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <TopSection q={topQ} range={range} />
        <HealthSection q={healthQ} />
        <FleetSection q={healthQ} />
      </div>
    </>
  );
}

const axisTick = { fill: "var(--muted-foreground)", fontSize: 11 };
const cursor = { stroke: "var(--muted-foreground)", strokeWidth: 1 };
const tooltipStyle = {
  background: "var(--popover)",
  border: "1px solid var(--border)",
  borderRadius: 6,
  color: "var(--popover-foreground)",
  fontSize: 12,
};

function Stat({
  label,
  value,
  testId,
  note,
}: {
  label: string;
  value: string;
  testId?: string;
  note?: ReactNode;
}) {
  return (
    <Card className="p-4">
      <div className="text-muted-foreground text-xs font-medium">{label}</div>
      <div
        data-testid={testId}
        className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums"
      >
        {value}
      </div>
      {note && <div className="mt-1">{note}</div>}
    </Card>
  );
}

function Section({
  id,
  title,
  note,
  children,
  className,
}: {
  id: string;
  title: string;
  note?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Card
      data-testid={`dashboard-section-${id}`}
      className={`flex min-w-0 flex-col gap-4 p-5 ${className ?? ""}`}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold">{title}</h2>
        {note && <span className="text-muted-foreground text-xs">{note}</span>}
      </div>
      {children}
    </Card>
  );
}

type SeriesDef = {
  key: string;
  label: string;
  color: string;
  dashed?: boolean;
};

type Row = { t: number } & Record<string, number>;

/** Series for a map field in a fixed key order; keys never seen in the range are left out. */
function mapSeries(
  points: SeriesPoint[],
  pick: (p: SeriesPoint) => Record<string, number>,
  order: string[],
  labels: Record<string, string> = {},
): { defs: SeriesDef[]; rows: Row[] } {
  const seen = new Set<string>();
  for (const p of points)
    for (const [k, v] of Object.entries(pick(p))) if (v > 0) seen.add(k);
  const known = order.filter((k) => seen.has(k));
  const extra = [...seen].filter((k) => !order.includes(k)).sort();
  const keys = [...known, ...extra];
  const defs: SeriesDef[] = keys.slice(0, series.length).map((k, i) => ({
    key: k,
    label: labels[k] ?? k,
    color: series[order.indexOf(k) >= 0 ? order.indexOf(k) : i],
  }));
  const folded = keys.slice(series.length);
  if (folded.length > 0)
    defs.push({ key: "__other", label: "Other", color: other });
  const rows = points.map((p) => {
    const m = pick(p);
    const row: Row = { t: new Date(p.at).getTime() };
    for (const def of defs)
      row[def.key] =
        def.key === "__other"
          ? folded.reduce((s, k) => s + (m[k] ?? 0), 0)
          : (m[def.key] ?? 0);
    return row;
  });
  return { defs, rows };
}

/** Categories sorted by their total in the range; beyond the palette they fold into "Other". */
function rankedSeries(
  points: SeriesPoint[],
  pick: (p: SeriesPoint) => Record<string, number>,
) {
  const totals = new Map<string, number>();
  for (const p of points)
    for (const [k, v] of Object.entries(pick(p)))
      totals.set(k, (totals.get(k) ?? 0) + v);
  const order = [...totals.entries()]
    .filter(([, v]) => v > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([k]) => k);
  return mapSeries(points, pick, order);
}

function fieldRows(points: SeriesPoint[], fields: (keyof SeriesPoint)[]) {
  return points.map((p) => {
    const row: Row = { t: new Date(p.at).getTime() };
    for (const f of fields) row[f] = Number(p[f]);
    return row;
  });
}

const qps = (v: number) => `${v < 10 ? v.toFixed(2) : v.toFixed(1)} q/s`;
const ms = (v: number) => `${v.toFixed(1)} ms`;
const percent = (v: number) => `${(v * 100).toFixed(1)}%`;

function Chart({
  label,
  defs,
  rows,
  range,
  to,
  stacked = false,
  format,
  ticks,
  empty,
}: {
  label: string;
  defs: SeriesDef[];
  rows: Row[];
  range: DashboardRange;
  to: number;
  stacked?: boolean;
  format: (v: number) => string;
  ticks?: (v: number) => string;
  empty: ReactNode;
}) {
  const long = range === "24h" || range === "7d";
  const Kind = stacked ? AreaChart : LineChart;
  return (
    <figure className="min-w-0">
      <figcaption className="text-muted-foreground mb-2 text-xs font-medium">
        {label}
      </figcaption>
      <div className="relative" role="img" aria-label={label}>
        <ResponsiveContainer width="100%" height={220}>
          <Kind data={rows} margin={{ top: 4, right: 8, bottom: 0, left: -4 }}>
            <CartesianGrid vertical={false} stroke="var(--border)" />
            <XAxis
              dataKey="t"
              type="number"
              scale="time"
              domain={[to - rangeMs[range], to]}
              allowDataOverflow
              tickFormatter={(t: number) =>
                range === "7d" ? day.format(t) : clock.format(t)
              }
              tick={axisTick}
              tickLine={false}
              axisLine={{ stroke: "var(--border)" }}
              minTickGap={40}
            />
            <YAxis
              width={52}
              tick={axisTick}
              tickLine={false}
              axisLine={false}
              tickFormatter={ticks}
            />
            <Tooltip
              cursor={cursor}
              contentStyle={tooltipStyle}
              labelFormatter={(t) =>
                (long ? dayClock : clockSeconds).format(Number(t))
              }
              formatter={(v, name) => {
                const s = defs.find((d) => d.key === name);
                return [
                  format(Number(v)),
                  s ? `${s.label}${s.dashed ? " uncached" : ""}` : String(name),
                ];
              }}
            />
            {defs.map((s) =>
              stacked ? (
                <Area
                  key={s.key}
                  dataKey={s.key}
                  stackId="stack"
                  type="monotone"
                  stroke={s.color}
                  fill={s.color}
                  fillOpacity={0.35}
                  strokeWidth={1.5}
                  isAnimationActive={false}
                />
              ) : (
                <Line
                  key={s.key}
                  dataKey={s.key}
                  type="monotone"
                  stroke={s.color}
                  strokeWidth={2}
                  strokeDasharray={s.dashed ? "4 3" : undefined}
                  dot={false}
                  activeDot={{ r: 4, strokeWidth: 2, stroke: "var(--card)" }}
                  isAnimationActive={false}
                />
              ),
            )}
          </Kind>
        </ResponsiveContainer>
        {rows.length === 0 && (
          <div className="text-muted-foreground pointer-events-none absolute inset-0 flex items-center justify-center text-sm">
            <span className="bg-card rounded px-2">{empty}</span>
          </div>
        )}
      </div>
      {defs.length > 1 && (
        <ul className="text-muted-foreground mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs">
          {defs.map((s) => (
            <li key={s.key} className="inline-flex items-center gap-1.5">
              <span
                aria-hidden
                className="inline-block h-0.5 w-3 rounded"
                style={{ background: s.color }}
              />
              {s.label}
              {s.dashed && " (uncached)"}
            </li>
          ))}
        </ul>
      )}
    </figure>
  );
}

function SeriesSections({
  q,
  range,
}: {
  q: ReturnType<typeof useDashboardSeries>;
  range: DashboardRange;
}) {
  const points = q.data?.range === range ? q.data.points : [];
  const engines = q.data?.engines ?? [];
  const to = q.dataUpdatedAt || 0;
  const empty = q.isPending ? "Loading…" : "No samples in this range yet";
  const common = { range, to, empty };
  const step = q.data ? stepLabel(q.data.step_seconds) : undefined;

  const transport = mapSeries(
    points,
    (p) => p.qps_by_transport,
    transports,
    transportLabels,
  );
  const rcode = mapSeries(points, (p) => p.qps_by_rcode, rcodes);
  const route = mapSeries(
    points,
    (p) => p.answers_by_route,
    routes,
    routeLabels,
  );
  const category = rankedSeries(points, (p) => p.blocked_by_category);
  const lame = points.reduce((s, p) => s + p.lame_marked, 0);

  const latency: SeriesDef[] = [
    { key: "p50_ms", label: "p50", color: series[0] },
    { key: "p95_ms", label: "p95", color: series[1] },
    { key: "p99_ms", label: "p99", color: series[2] },
    { key: "miss_p50_ms", label: "p50", color: series[0], dashed: true },
    { key: "miss_p95_ms", label: "p95", color: series[1], dashed: true },
    { key: "miss_p99_ms", label: "p99", color: series[2], dashed: true },
  ];
  const cache: SeriesDef[] = [
    { key: "cache_hit_ratio", label: "Hit", color: series[0] },
    { key: "cache_miss_ratio", label: "Miss", color: series[1] },
    { key: "cache_stale_ratio", label: "Stale", color: series[2] },
  ];
  const recursion: SeriesDef[] = [
    {
      key: "recursion_upstream_qps",
      label: "Upstream queries",
      color: series[0],
    },
    { key: "recursion_timeouts_qps", label: "Timeouts", color: series[1] },
    {
      key: "resolution_failures_qps",
      label: "Resolution failures",
      color: series[2],
    },
  ];
  const filtering: SeriesDef[] = [
    { key: "blocked_qps", label: "Blocked", color: series[0] },
    { key: "rewritten_qps", label: "Rewritten", color: series[1] },
  ];
  // DNSSEC outcomes are states, so they wear the status colours.
  const dnssec: SeriesDef[] = [
    { key: "dnssec_secure_qps", label: "Secure", color: "var(--success)" },
    {
      key: "dnssec_insecure_qps",
      label: "Insecure",
      color: "var(--muted-foreground)",
    },
    { key: "dnssec_bogus_qps", label: "Bogus", color: "var(--destructive)" },
  ];
  const rows = (defs: SeriesDef[]) =>
    fieldRows(
      points,
      defs.map((s) => s.key as keyof SeriesPoint),
    );

  return (
    <>
      <ErrorAlert
        error={q.error}
        prefix="Could not load the charts"
        className="mb-4"
      />
      <div className="grid gap-4 lg:grid-cols-2">
        <Section id="traffic" title="Traffic" note={step}>
          <Chart
            label="Queries per second by transport"
            stacked
            format={qps}
            {...transport}
            {...common}
          />
          <Chart
            label="Queries per second by response code"
            format={qps}
            {...rcode}
            {...common}
          />
        </Section>
        <Section id="latency" title="Latency" note={step}>
          <Chart
            label="Response time percentiles; dashed lines are uncached answers"
            format={ms}
            defs={latency}
            rows={rows(latency)}
            {...common}
          />
        </Section>
        <Section id="cache" title="Cache" note={step}>
          <Chart
            label="Share of answers from the cache"
            format={percent}
            ticks={(v) => `${Math.round(v * 100)}%`}
            defs={cache}
            rows={rows(cache)}
            {...common}
          />
          <EngineTable
            engines={engines}
            pending={q.isPending}
            columns={[
              ["Entries", (e) => integer.format(e.cache_entries)],
              ["Memory", (e) => formatBytes(e.cache_bytes)],
            ]}
          />
        </Section>
        <Section id="resolution" title="Resolution" note={step}>
          <Chart
            label="Answers per second by route"
            stacked
            format={qps}
            {...route}
            {...common}
          />
          <Chart
            label="Recursion and failures per second"
            format={qps}
            defs={recursion}
            rows={rows(recursion)}
            {...common}
          />
          <p className="text-muted-foreground text-xs">
            Lame servers marked in this range:{" "}
            <span className="text-foreground tabular-nums">
              {integer.format(lame)}
            </span>
          </p>
        </Section>
        <Section id="filtering" title="Filtering" note={step}>
          <Chart
            label="Blocked and rewritten per second"
            format={qps}
            defs={filtering}
            rows={rows(filtering)}
            {...common}
          />
          <Chart
            label="Blocked per second by category"
            stacked
            format={qps}
            {...category}
            {...common}
          />
          <EngineTable
            engines={engines}
            pending={q.isPending}
            columns={[
              ["Filter index", (e) => formatBytes(e.filter_index_bytes)],
            ]}
          />
        </Section>
        <Section id="dnssec" title="DNSSEC" note={step}>
          <Chart
            label="Validated answers per second"
            format={qps}
            defs={dnssec}
            rows={rows(dnssec)}
            {...common}
          />
        </Section>
      </div>
    </>
  );
}

function stepLabel(seconds: number): string {
  return seconds >= 3600
    ? `${seconds / 3600} h steps`
    : seconds >= 60
      ? `${seconds / 60} min steps`
      : `${seconds} s steps`;
}

type EngineFigures = Schemas["DashboardSeries"]["engines"][number];

function EngineTable({
  engines,
  pending,
  columns,
}: {
  engines: EngineFigures[];
  pending: boolean;
  columns: [string, (e: EngineFigures) => string][];
}) {
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="h-8">Resolver</TableHead>
          {columns.map(([title]) => (
            <TableHead key={title} className="h-8 text-right">
              {title}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {engines.map((e) => (
          <TableRow key={e.engine_id}>
            <TableCell className="py-2 font-medium">{e.node_name}</TableCell>
            {columns.map(([title, value]) => (
              <TableCell key={title} className="py-2 text-right tabular-nums">
                {value(e)}
              </TableCell>
            ))}
          </TableRow>
        ))}
        {pending && (
          <MessageRow colSpan={columns.length + 1}>Loading…</MessageRow>
        )}
        {!pending && engines.length === 0 && (
          <MessageRow colSpan={columns.length + 1}>
            No samples in this range yet
          </MessageRow>
        )}
      </TableBody>
    </Table>
  );
}

function formatBytes(n: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

const topLists: {
  key: "domains" | "blocked_domains" | "clients" | "categories";
  title: string;
}[] = [
  { key: "domains", title: "Domains" },
  { key: "blocked_domains", title: "Blocked domains" },
  { key: "clients", title: "Clients" },
  { key: "categories", title: "Blocked categories" },
];

function TopSection({
  q,
  range,
}: {
  q: ReturnType<typeof useDashboardTop>;
  range: DashboardRange;
}) {
  const t = q.data?.range === range ? q.data : undefined;
  return (
    <Section id="top" title="Top lists" note={rangeLabels[range]}>
      <ErrorAlert error={q.error} prefix="Could not load the top lists" />
      {t && !t.available ? (
        <p
          data-testid="dashboard-top-unavailable"
          className="text-muted-foreground rounded-md border border-dashed p-4 text-sm"
        >
          The query log backend is unavailable
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {topLists.map(({ key, title }) => {
            const entries = t?.[key] ?? [];
            const max = Math.max(1, ...entries.map((e) => e.count));
            return (
              <div key={key} className="min-w-0">
                <h3 className="text-muted-foreground mb-2 text-xs font-medium">
                  {title}
                </h3>
                {entries.length === 0 ? (
                  <p className="text-muted-foreground text-sm">
                    {q.isPending ? "Loading…" : "No queries in this range yet"}
                  </p>
                ) : (
                  <ol className="space-y-1.5">
                    {entries.map((e) => (
                      <li key={e.key} className="text-sm">
                        <div className="flex items-baseline justify-between gap-2">
                          <span className="min-w-0 truncate" title={e.key}>
                            {e.key}
                          </span>
                          <span className="text-muted-foreground shrink-0 tabular-nums">
                            {integer.format(e.count)}
                          </span>
                        </div>
                        <div
                          aria-hidden
                          className="bg-primary/60 mt-0.5 h-1 rounded-full"
                          style={{ width: `${(e.count / max) * 100}%` }}
                        />
                      </li>
                    ))}
                  </ol>
                )}
              </div>
            );
          })}
        </div>
      )}
    </Section>
  );
}

function FleetSection({ q }: { q: ReturnType<typeof useDashboardHealth> }) {
  const h = q.data;
  const engines = [...(h?.engines ?? [])].sort((a, b) =>
    a.node_name.localeCompare(b.node_name),
  );
  return (
    <Section id="fleet" title="Fleet" className="lg:col-span-2">
      <ErrorAlert error={q.error} prefix="Could not load the fleet" />
      {h && h.groups.length > 0 && (
        <ul className="flex flex-wrap gap-2 text-sm">
          {h.groups.map((g) => (
            <li
              key={g.id}
              className="rounded-md border px-2.5 py-1"
              title={`${g.connected} of ${g.engines} connected`}
            >
              <span className="font-medium">{g.name}</span>{" "}
              <span className="text-muted-foreground tabular-nums">
                {g.connected} / {g.engines} connected
              </span>
            </li>
          ))}
        </ul>
      )}
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-9">Resolver</TableHead>
            <TableHead className="h-9">Group</TableHead>
            <TableHead className="h-9">Status</TableHead>
            <TableHead className="h-9 text-right">QPS</TableHead>
            <TableHead className="h-9 text-right">p99</TableHead>
            <TableHead className="h-9 text-right">Cache hits</TableHead>
            <TableHead className="h-9 text-right">Filter index</TableHead>
            <TableHead className="h-9 text-right">Config</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {engines.map((e) => (
            <TableRow
              key={e.id}
              data-testid={`dashboard-fleet-row-${e.node_name}`}
            >
              <TableCell className="py-2 font-medium">{e.node_name}</TableCell>
              <TableCell className="py-2">{e.engine_group_name}</TableCell>
              <TableCell className="py-2">
                {engineStatuses.includes(e.status as Engine["status"]) ? (
                  <EngineStatusBadge status={e.status as Engine["status"]} />
                ) : (
                  e.status
                )}
              </TableCell>
              <TableCell className="py-2 text-right tabular-nums">
                {e.qps.toFixed(1)}
              </TableCell>
              <TableCell className="py-2 text-right tabular-nums">
                {ms(e.p99_ms)}
              </TableCell>
              <TableCell className="py-2 text-right tabular-nums">
                {percent(e.cache_hit_ratio)}
              </TableCell>
              <TableCell className="py-2 text-right tabular-nums">
                {formatBytes(e.filter_index_bytes)}
              </TableCell>
              <TableCell className="py-2 text-right tabular-nums">
                {e.applied_version === e.target_version
                  ? `v${e.applied_version}`
                  : `v${e.applied_version} → v${e.target_version}`}
              </TableCell>
            </TableRow>
          ))}
          {q.isPending && <MessageRow colSpan={8}>Loading…</MessageRow>}
          {q.isSuccess && engines.length === 0 && (
            <MessageRow colSpan={8}>No resolvers have joined yet.</MessageRow>
          )}
        </TableBody>
      </Table>
    </Section>
  );
}

const severityRank = { critical: 0, warning: 1 } as const;

function HealthSection({ q }: { q: ReturnType<typeof useDashboardHealth> }) {
  const alerts = [...(q.data?.alerts ?? [])].sort(
    (a, b) =>
      severityRank[a.severity] - severityRank[b.severity] ||
      a.subject.localeCompare(b.subject),
  );
  return (
    <Section id="health" title="Health">
      <ErrorAlert error={q.error} prefix="Could not load the health checks" />
      {q.isPending ? (
        <p className="text-muted-foreground text-sm">Loading…</p>
      ) : q.isSuccess && alerts.length === 0 ? (
        <StatusDot tone="success">Everything looks healthy</StatusDot>
      ) : (
        <ul className="divide-y">
          {alerts.map((a, i) => (
            <li
              key={`${a.kind}-${a.subject}-${i}`}
              data-testid="dashboard-alert"
              className="flex flex-col gap-0.5 py-2 first:pt-0 last:pb-0"
            >
              <div className="flex flex-wrap items-center gap-x-2">
                <StatusDot
                  tone={a.severity === "critical" ? "destructive" : "warning"}
                >
                  <span className="font-medium">
                    {a.severity === "critical" ? "Critical" : "Warning"}
                  </span>
                </StatusDot>
                <span className="min-w-0 text-sm font-medium break-all">
                  {a.subject}
                </span>
              </div>
              <p className="text-muted-foreground text-sm">{a.message}</p>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
