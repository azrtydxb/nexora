import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  ExternalLink,
  Pause,
  Play,
} from "lucide-react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
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

import { ApiError, api, unwrap, type Schemas } from "@/api/client";
import {
  type Engine,
  type LogLevel,
  type StatsWindow,
  useEngine,
  useEngineLogs,
  useEngineMetrics,
} from "@/api/fleet";
import {
  ErrorAlert,
  Fact,
  formatAgo,
  formatDateTime,
  MessageRow,
} from "@/components/common";
import { EngineStatusBadge } from "@/components/fleet";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

// History state marking an entry pushed by opening the modal, so closing it can go back instead of
// leaving a second entry for the same list.
const openedHere = "engineModal";

/** The node-name button in an engine list row that opens the engine modal on that engine. */
export function EngineModalOpenButton({ engine }: { engine: Engine }) {
  const [params, setParams] = useSearchParams();
  return (
    <Button
      variant="ghost"
      size="sm"
      className="-ml-3 h-8 font-medium"
      data-testid={`engine-modal-open-${engine.node_name}`}
      title={`Show ${engine.node_name}'s metrics, logs and queries`}
      onClick={() => {
        const next = new URLSearchParams(params);
        next.set("engine", engine.id);
        setParams(next, { state: { [openedHere]: true } });
      }}
    >
      {engine.node_name}
    </Button>
  );
}

type Tab = "metrics" | "logs" | "queries";

/**
 * The engine modal: open while `?engine=<id>` is in the URL, so it deep-links and back closes it.
 * Next and previous step through `engineIds`, the engines the list currently shows.
 */
export function EngineModal({ engineIds }: { engineIds: string[] }) {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const [tab, setTab] = useState<Tab>("metrics");
  const id = params.get("engine");
  if (!id) return null;

  const pushed =
    (location.state as Record<string, unknown> | null)?.[openedHere] === true;
  const index = engineIds.indexOf(id);

  function show(engineId: string) {
    const next = new URLSearchParams(params);
    next.set("engine", engineId);
    setParams(next, { replace: true, state: location.state });
  }

  function close() {
    if (pushed) {
      void navigate(-1);
      return;
    }
    const next = new URLSearchParams(params);
    next.delete("engine");
    setParams(next, { replace: true });
  }

  return (
    <Dialog open onOpenChange={(open) => !open && close()}>
      <DialogContent
        data-testid="engine-modal"
        className="flex h-dvh max-h-dvh w-full max-w-full flex-col gap-4 overflow-y-auto p-4 sm:h-auto sm:max-h-[90vh] sm:max-w-5xl sm:p-6"
      >
        <EngineHeader
          id={id}
          canPrev={index > 0}
          canNext={index >= 0 && index < engineIds.length - 1}
          onPrev={() => show(engineIds[index - 1])}
          onNext={() => show(engineIds[index + 1])}
        />
        <Tabs
          value={tab}
          onValueChange={(v) => setTab(v as Tab)}
          className="min-w-0"
        >
          <TabsList>
            <TabsTrigger value="metrics" data-testid="engine-tab-metrics">
              Metrics
            </TabsTrigger>
            <TabsTrigger value="logs" data-testid="engine-tab-logs">
              Logs
            </TabsTrigger>
            <TabsTrigger value="queries" data-testid="engine-tab-queries">
              Queries
            </TabsTrigger>
          </TabsList>
          <TabsContent value="metrics">
            <MetricsTab id={id} />
          </TabsContent>
          <TabsContent value="logs">
            <LogsTab id={id} />
          </TabsContent>
          <TabsContent value="queries">
            <QueriesTab id={id} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function EngineHeader({
  id,
  canPrev,
  canNext,
  onPrev,
  onNext,
}: {
  id: string;
  canPrev: boolean;
  canNext: boolean;
  onPrev: () => void;
  onNext: () => void;
}) {
  const engine = useEngine(id);
  const e = engine.data;
  return (
    <DialogHeader className="gap-2 pr-8 text-left">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <DialogTitle className="truncate">
          {e?.node_name ?? "Engine"}
        </DialogTitle>
        {e && <EngineStatusBadge status={e.status} />}
        <span className="ml-auto flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label="Previous engine"
            data-testid="engine-modal-prev"
            disabled={!canPrev}
            onClick={onPrev}
          >
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label="Next engine"
            data-testid="engine-modal-next"
            disabled={!canNext}
            onClick={onNext}
          >
            <ChevronRight className="h-4 w-4" />
          </Button>
          <Link
            to={`/engines/nodes/${id}`}
            data-testid="engine-modal-full-page"
            className="text-primary ml-2 inline-flex items-center gap-1 text-sm hover:underline"
          >
            Full page
            <ExternalLink className="h-3.5 w-3.5" />
          </Link>
        </span>
      </div>
      <DialogDescription asChild>
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {e ? (
            <>
              <span>Group {e.engine_group_name}</span>
              <span className="font-mono text-xs leading-5">
                {e.engine_version || "—"}
              </span>
              <span className="tabular-nums">
                Config v{e.applied_version} / target v{e.target_version}
              </span>
              <span title={formatDateTime(e.last_seen_at)}>
                Last seen {e.connected ? "now" : formatAgo(e.last_seen_at)}
              </span>
            </>
          ) : (
            <span>{engine.isPending ? "Loading…" : ""}</span>
          )}
        </div>
      </DialogDescription>
      <ErrorAlert error={engine.error} prefix="Could not load the engine" />
    </DialogHeader>
  );
}

const windows: StatsWindow[] = ["5m", "1h", "24h"];

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
});
const clockSeconds = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
const axisTick = { fill: "var(--muted-foreground)", fontSize: 11 };
const palette = [
  "var(--primary)",
  "var(--warning)",
  "var(--success)",
  "var(--destructive)",
  "var(--muted-foreground)",
];

type Row = { t: number } & Record<string, number>;
type Series = {
  key: string;
  label: string;
  right?: boolean;
  dashed?: boolean;
  format: (v: number) => string;
};

function MetricsTab({ id }: { id: string }) {
  const [win, setWin] = useState<StatsWindow>("1h");
  const metrics = useEngineMetrics(id, win);
  const m = metrics.data;
  const samples = m?.samples ?? [];
  const at = (s: { at: string }) => new Date(s.at).getTime();
  const pct = (v: number) => `${(v * 100).toFixed(1)} %`;
  const perSecond = (v: number) => `${v.toFixed(1)} /s`;
  const ms = (v: number) => `${v.toFixed(1)} ms`;
  const mib = (v: number) => `${v.toFixed(0)} MiB`;

  const transports = [
    ...new Set(samples.flatMap((s) => Object.keys(s.connections))),
  ].sort();
  const upstreams = m?.upstreams ?? [];
  const upstreamRows = new Map<number, Row>();
  upstreams.forEach((u, i) => {
    for (const s of u.samples) {
      const row = upstreamRows.get(at(s)) ?? { t: at(s) };
      row[`rtt${i}`] = s.rtt_ms;
      row[`fail${i}`] = s.failures_per_second;
      upstreamRows.set(row.t, row);
    }
  });
  const hasLimit = samples.some((s) => s.memory_limit_bytes > 0);
  const loading = metrics.isPending;
  const empty =
    "No samples in this window. Connected engines report every few seconds.";

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <dl className="flex flex-wrap gap-6 text-sm">
          <Fact label="Uptime">
            <span data-testid="engine-uptime">
              {m?.started_at ? formatUptime(m.started_at) : "—"}
            </span>
          </Fact>
          <Fact label="Restarts in window">
            <span data-testid="engine-restarts">{m ? m.restarts : "—"}</span>
          </Fact>
        </dl>
        <Select value={win} onValueChange={(v) => setWin(v as StatsWindow)}>
          <SelectTrigger
            aria-label="Time window"
            data-testid="engine-metrics-window"
            className="h-8 w-28"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {windows.map((w) => (
              <SelectItem key={w} value={w}>
                {w}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <ErrorAlert error={metrics.error} prefix="Could not load metrics" />
      <div className="grid gap-4 lg:grid-cols-2">
        <SeriesChart
          testId="engine-chart-qps"
          title="Queries and blocked queries per second"
          loading={loading}
          empty={empty}
          rows={samples.map((s) => ({
            t: at(s),
            qps: s.qps,
            blocked: s.blocked_qps,
          }))}
          series={[
            { key: "qps", label: "Queries", format: perSecond },
            { key: "blocked", label: "Blocked", format: perSecond },
          ]}
        />
        <SeriesChart
          testId="engine-chart-latency"
          title="Latency"
          loading={loading}
          empty={empty}
          rows={samples.map((s) => ({
            t: at(s),
            p50: s.p50_ms,
            p99: s.p99_ms,
          }))}
          series={[
            { key: "p50", label: "p50", format: ms },
            { key: "p99", label: "p99", format: ms },
          ]}
        />
        <SeriesChart
          testId="engine-chart-ratios"
          title="Cache hits and error answers"
          loading={loading}
          empty={empty}
          rows={samples.map((s) => ({
            t: at(s),
            cache: s.cache_hit_ratio,
            servfail: s.servfail_ratio,
            nxdomain: s.nxdomain_ratio,
            refused: s.refused_ratio,
          }))}
          series={[
            { key: "cache", label: "Cache hit", format: pct },
            { key: "servfail", label: "SERVFAIL", format: pct },
            { key: "nxdomain", label: "NXDOMAIN", format: pct },
            { key: "refused", label: "REFUSED", format: pct },
          ]}
        />
        <SeriesChart
          testId="engine-chart-upstreams"
          title="Upstream round-trip time and failures"
          loading={loading}
          empty="No upstream samples in this window."
          rows={[...upstreamRows.values()].sort((a, b) => a.t - b.t)}
          series={upstreams.flatMap((u, i) => [
            { key: `rtt${i}`, label: `${u.name} RTT`, format: ms },
            {
              key: `fail${i}`,
              label: `${u.name} failures`,
              right: true,
              dashed: true,
              format: perSecond,
            },
          ])}
        />
        <SeriesChart
          testId="engine-chart-resources"
          title={
            hasLimit
              ? "CPU and resident memory against the limit"
              : "CPU and resident memory"
          }
          loading={loading}
          empty={empty}
          rows={samples.map((s) => ({
            t: at(s),
            cpu: s.cpu_cores,
            rss: s.resident_bytes / 1048576,
            ...(hasLimit && { limit: s.memory_limit_bytes / 1048576 }),
          }))}
          series={[
            {
              key: "cpu",
              label: "CPU cores",
              format: (v) => v.toFixed(2),
            },
            { key: "rss", label: "Resident", right: true, format: mib },
            ...(hasLimit
              ? [
                  {
                    key: "limit",
                    label: "Limit",
                    right: true,
                    dashed: true,
                    format: mib,
                  },
                ]
              : []),
          ]}
        />
        <SeriesChart
          testId="engine-chart-connections"
          title="Open connections per transport"
          loading={loading}
          empty={empty}
          rows={samples.map((s) => ({ t: at(s), ...s.connections }))}
          series={transports.map((tr) => ({
            key: tr,
            label: tr,
            format: (v) => v.toFixed(0),
          }))}
        />
      </div>
      {m?.filter_index && <FilterIndex fi={m.filter_index} />}
    </div>
  );
}

function FilterIndex({
  fi,
}: {
  fi: NonNullable<Schemas["EngineMetrics"]["filter_index"]>;
}) {
  return (
    <Card className="p-4">
      <h3 className="mb-3 text-sm font-semibold">Filter index</h3>
      <dl className="grid grid-cols-2 gap-4 text-sm sm:grid-cols-4">
        <Fact label="Memory">
          {formatBytes(fi.bytes)} of {formatBytes(fi.max_bytes)}
        </Fact>
        <Fact label="Names">{fi.entries.toLocaleString()}</Fact>
        <Fact label="Last build">{fi.build_seconds.toFixed(2)} s</Fact>
        <Fact label="Decision time">
          {fi.decision_ns_blocked.toFixed(0)} ns blocked,{" "}
          {fi.decision_ns_clean.toFixed(0)} ns clean
        </Fact>
      </dl>
    </Card>
  );
}

function SeriesChart({
  testId,
  title,
  rows,
  series,
  loading,
  empty,
}: {
  testId: string;
  title: string;
  rows: Row[];
  series: Series[];
  loading: boolean;
  empty: string;
}) {
  const byKey = new Map(series.map((s) => [s.key, s]));
  const left = series.find((s) => !s.right);
  const right = series.find((s) => s.right);
  return (
    <Card className="min-w-0 p-4">
      <h3 className="mb-2 text-sm font-semibold">{title}</h3>
      <div
        data-testid={testId}
        className="text-muted-foreground relative h-52"
        role="img"
        aria-label={title}
      >
        {rows.length > 0 && series.length > 0 ? (
          <ResponsiveContainer width="100%" height="100%">
            <LineChart
              data={rows}
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
                yAxisId="left"
                width={52}
                tick={axisTick}
                tickLine={false}
                axisLine={false}
                tickFormatter={(v: number) => left?.format(v) ?? String(v)}
              />
              {right && (
                <YAxis
                  yAxisId="right"
                  orientation="right"
                  width={52}
                  tick={axisTick}
                  tickLine={false}
                  axisLine={false}
                  tickFormatter={(v: number) => right.format(v)}
                />
              )}
              <Tooltip
                contentStyle={{
                  background: "var(--popover)",
                  border: "1px solid var(--border)",
                  borderRadius: 6,
                  color: "var(--popover-foreground)",
                  fontSize: 12,
                }}
                labelFormatter={(t) => clockSeconds.format(Number(t))}
                formatter={(v, key) => {
                  const s = byKey.get(String(key));
                  return s ? [s.format(Number(v)), s.label] : [String(v), key];
                }}
              />
              <Legend
                wrapperStyle={{ fontSize: 12 }}
                formatter={(key) => byKey.get(String(key))?.label ?? key}
              />
              {series.map((s, i) => (
                <Line
                  key={s.key}
                  yAxisId={s.right ? "right" : "left"}
                  type="monotone"
                  dataKey={s.key}
                  stroke={palette[i % palette.length]}
                  strokeWidth={1.5}
                  strokeDasharray={s.dashed ? "4 3" : undefined}
                  dot={false}
                  isAnimationActive={false}
                />
              ))}
            </LineChart>
          </ResponsiveContainer>
        ) : (
          <div className="flex h-full items-center justify-center rounded-md border border-dashed px-4 text-center text-sm">
            {loading ? "Loading…" : empty}
          </div>
        )}
      </div>
    </Card>
  );
}

const levels: LogLevel[] = ["error", "warn", "info", "debug"];

const levelClass: Record<LogLevel, string> = {
  error: "text-destructive",
  warn: "text-warning",
  info: "text-foreground",
  debug: "text-muted-foreground",
};

function logsError(err: unknown): string | null {
  if (!err) return null;
  if (err instanceof ApiError) {
    if (err.status === 409) return "Engine is not connected";
    if (err.status === 504) return "Engine did not answer";
  }
  return err instanceof Error ? err.message : String(err);
}

function LogsTab({ id }: { id: string }) {
  const [level, setLevel] = useState<LogLevel>("debug");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [paused, setPaused] = useState(false);
  const [follow, setFollow] = useState(true);
  const logs = useEngineLogs(id, { level, q, paused });
  const list = useRef<HTMLOListElement>(null);

  // The search is sent as q once typing pauses for 300 ms.
  useEffect(() => {
    const t = setTimeout(() => setQ(search.trim()), 300);
    return () => clearTimeout(t);
  }, [search]);

  const newest = logs.lines[logs.lines.length - 1]?.seq;
  useEffect(() => {
    if (follow && list.current)
      list.current.scrollTop = list.current.scrollHeight;
  }, [follow, newest]);

  const unsupported =
    logs.error instanceof ApiError && logs.error.status === 501;
  const message = unsupported ? null : logsError(logs.error);

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={level} onValueChange={(v) => setLevel(v as LogLevel)}>
          <SelectTrigger
            aria-label="Minimum level"
            data-testid="engine-logs-level"
            className="h-8 w-28"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {levels.map((l) => (
              <SelectItem key={l} value={l}>
                {l}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          type="search"
          aria-label="Search log lines"
          data-testid="engine-logs-search"
          placeholder="Search"
          className="h-8 w-full min-w-0 sm:w-56"
          maxLength={128}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Button
          variant="outline"
          size="sm"
          className="h-8"
          data-testid="engine-logs-pause"
          aria-pressed={paused}
          onClick={() => setPaused((p) => !p)}
        >
          {paused ? (
            <Play className="mr-1.5 h-3.5 w-3.5" />
          ) : (
            <Pause className="mr-1.5 h-3.5 w-3.5" />
          )}
          {paused ? "Resume" : "Pause"}
        </Button>
        <Button
          variant={follow ? "secondary" : "outline"}
          size="sm"
          className="h-8"
          data-testid="engine-logs-follow"
          aria-pressed={follow}
          onClick={() => setFollow((f) => !f)}
        >
          Follow
        </Button>
      </div>
      {unsupported && (
        <p
          data-testid="engine-logs-unsupported"
          className="text-muted-foreground rounded-md border border-dashed px-4 py-6 text-center text-sm"
        >
          This engine version cannot send logs
        </p>
      )}
      {message && <ErrorAlert error={new Error(message)} />}
      {logs.droppedOlder && (
        <p className="text-muted-foreground text-xs">
          Older lines are not shown: the engine keeps its latest 2,000 lines and
          this view keeps 2,000.
        </p>
      )}
      {!unsupported && (
        <ol
          ref={list}
          className="bg-muted/40 h-80 overflow-auto rounded-md border p-2 font-mono text-xs leading-5"
          onWheel={() => follow && setFollow(false)}
        >
          {logs.lines.map((l) => (
            <li
              key={l.seq}
              data-testid="engine-log-line"
              className="break-all whitespace-pre-wrap"
            >
              <span className="text-muted-foreground mr-2">
                {clockSeconds.format(new Date(l.time))}
              </span>
              <span
                className={cn(
                  "mr-2 inline-block w-11 uppercase",
                  levelClass[l.level],
                )}
              >
                {l.level}
              </span>
              <span className={levelClass[l.level]}>{l.message}</span>
            </li>
          ))}
          {logs.lines.length === 0 && (
            <li className="text-muted-foreground px-2 py-4 text-center font-sans">
              {logs.error
                ? "No lines."
                : paused
                  ? "Paused."
                  : "Waiting for lines…"}
            </li>
          )}
        </ol>
      )}
    </div>
  );
}

const queriesShown = 20;

function QueriesTab({ id }: { id: string }) {
  const queries = useQuery({
    queryKey: ["query-log", "engine", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/query-log", {
          params: { query: { engine_id: [id], limit: queriesShown } },
        }),
      ),
    refetchInterval: 5_000,
    retry: false,
  });
  const rows = queries.data?.records ?? [];
  return (
    <div className="grid gap-3">
      <div className="flex items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm">
          The latest {queriesShown} queries this engine answered.
        </p>
        <Link
          to={`/query-log?engine_id=${id}`}
          data-testid="engine-queries-open-log"
          className="text-primary shrink-0 text-sm hover:underline"
        >
          Open in the query log
        </Link>
      </div>
      <ErrorAlert error={queries.error} prefix="Could not load queries" />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-9">Time</TableHead>
              <TableHead className="h-9">Client</TableHead>
              <TableHead className="h-9">Name</TableHead>
              <TableHead className="h-9">Type</TableHead>
              <TableHead className="h-9">Answer</TableHead>
              <TableHead className="h-9 text-right">Duration</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r, i) => (
              <TableRow
                key={`${r.time}-${r.name}-${i}`}
                data-testid="engine-queries-row"
              >
                <TableCell className="py-2 whitespace-nowrap tabular-nums">
                  {clockSeconds.format(new Date(r.time))}
                </TableCell>
                <TableCell className="py-2 font-mono text-xs">
                  {r.client}
                </TableCell>
                <TableCell className="max-w-[18rem] truncate py-2 font-mono text-xs">
                  {r.name}
                </TableCell>
                <TableCell className="py-2 text-xs">{r.qtype}</TableCell>
                <TableCell className="py-2 text-xs whitespace-nowrap">
                  {r.rcode}
                  {r.filter !== "none" && (
                    <span className="text-muted-foreground ml-1.5">
                      {r.filter}
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-2 text-right text-xs tabular-nums">
                  {(r.duration_us / 1000).toFixed(1)} ms
                </TableCell>
              </TableRow>
            ))}
            {queries.isPending && <MessageRow colSpan={6}>Loading…</MessageRow>}
            {queries.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={6}>
                No queries from this engine in the query log.
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}

function formatUptime(startedAt: string): string {
  const s = Math.max(0, (Date.now() - new Date(startedAt).getTime()) / 1000);
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86400)
    return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  return `${Math.floor(s / 86400)} d ${Math.floor((s % 86400) / 3600)} h`;
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
