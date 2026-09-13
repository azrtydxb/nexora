import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { api, unwrap } from "@/api/client";
import { ErrorAlert, MessageRow, StatusDot } from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

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

export function DashboardPage() {
  const q = useQuery({
    queryKey: ["dashboard"],
    queryFn: async () => unwrap(await api.GET("/dashboard")),
    refetchInterval: 10_000,
  });
  const d = q.data;
  const series = (d?.series ?? []).map((p) => ({
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
        description="Fleet-wide traffic and health, refreshed every 10 seconds."
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

      <div className="grid gap-6 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <Card className="p-5">
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
            aria-label="Fleet queries per second over time"
          >
            {series.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <LineChart
                  data={series}
                  margin={{ top: 4, right: 8, bottom: 0, left: -8 }}
                >
                  <CartesianGrid
                    vertical={false}
                    stroke="var(--border)"
                    strokeDasharray="0"
                  />
                  <XAxis
                    dataKey="t"
                    type="number"
                    scale="time"
                    domain={["dataMin", "dataMax"]}
                    tickFormatter={(t: number) => clock.format(t)}
                    tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
                    tickLine={false}
                    axisLine={{ stroke: "var(--border)" }}
                    minTickGap={48}
                  />
                  <YAxis
                    width={48}
                    allowDecimals={false}
                    tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
                    tickLine={false}
                    axisLine={false}
                  />
                  <Tooltip
                    cursor={{
                      stroke: "var(--muted-foreground)",
                      strokeWidth: 1,
                    }}
                    contentStyle={{
                      background: "var(--popover)",
                      border: "1px solid var(--border)",
                      borderRadius: 6,
                      color: "var(--popover-foreground)",
                      fontSize: 12,
                    }}
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

        <Card className="overflow-hidden">
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
    </>
  );
}

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
