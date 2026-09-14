import { useState, type FormEvent, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  FilterX,
  RefreshCw,
  Search,
} from "lucide-react";

import { api, ApiError, unwrap, type Schemas } from "@/api/client";
import { useFilterCategories } from "@/api/filterCategories";
import { ErrorAlert, MessageRow } from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
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

type QueryLogRecord = Schemas["QueryLogRecord"];

const pageSize = 100;
const any = "any";

const qtypes = [
  "A",
  "AAAA",
  "CNAME",
  "HTTPS",
  "MX",
  "NS",
  "PTR",
  "SOA",
  "SRV",
  "TXT",
];
const rcodes = ["NOERROR", "NXDOMAIN", "SERVFAIL", "REFUSED", "FORMERR"];
const cacheStates = ["hit", "miss", "stale", "none"];
const filterStates = ["none", "blocked", "allowed", "rewritten"];

type Filters = {
  name: string;
  client: string;
  qtype: string;
  rcode: string;
  cache: string;
  filter: string;
  category: string;
};

const emptyFilters: Filters = {
  name: "",
  client: "",
  qtype: any,
  rcode: any,
  cache: any,
  filter: any,
  category: any,
};

const timeFormat = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  fractionalSecondDigits: 3,
});

const liveIntervalMs = 5000;

function param(v: string): string | undefined {
  const t = v.trim();
  return t === "" || t === any ? undefined : t;
}

export function QueryLogPage() {
  const [form, setForm] = useState<Filters>(emptyFilters);
  const [applied, setApplied] = useState<Filters>(emptyFilters);
  // Cursors of the pages before the current one; the current page's cursor is last.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors[cursors.length - 1];
  const categories = useFilterCategories();
  // Live mode reloads the newest page every liveIntervalMs; older pages stay put while browsing.
  const [live, setLive] = useState(true);

  const q = useQuery({
    queryKey: ["query-log", applied, cursor],
    queryFn: async () =>
      unwrap(
        await api.GET("/query-log", {
          params: {
            query: {
              name: param(applied.name),
              client: param(applied.client),
              qtype: param(applied.qtype),
              rcode: param(applied.rcode),
              cache: param(applied.cache),
              filter: param(applied.filter),
              category: param(applied.category),
              limit: pageSize,
              cursor,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
    retry: false,
    refetchInterval: live && !cursor ? liveIntervalMs : false,
    refetchIntervalInBackground: false,
  });

  const set = <K extends keyof Filters>(key: K, value: Filters[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  function search(e: FormEvent) {
    e.preventDefault();
    setCursors([]);
    if (JSON.stringify(form) === JSON.stringify(applied) && !cursor) {
      void q.refetch();
    } else {
      setApplied(form);
    }
  }

  function reset() {
    setForm(emptyFilters);
    setApplied(emptyFilters);
    setCursors([]);
  }

  // Refresh reloads the page on screen now (a no-op filter change would not trigger a request).
  function refresh() {
    void q.refetch();
  }

  const unavailable =
    q.error instanceof ApiError && q.error.code === "querylog_unavailable";
  const records = q.data?.records ?? [];

  return (
    <>
      <PageHeader
        title="Query log"
        description="Queries answered by the engines, newest first."
        actions={
          <div className="flex flex-wrap items-center gap-3 text-sm">
            {q.dataUpdatedAt > 0 && (
              <span
                className="text-muted-foreground"
                data-testid="querylog-updated"
                aria-live="polite"
              >
                Updated {new Date(q.dataUpdatedAt).toLocaleTimeString()}
              </span>
            )}
            <label className="text-muted-foreground flex cursor-pointer items-center gap-1.5">
              <input
                type="checkbox"
                data-testid="querylog-live"
                className="accent-primary h-4 w-4"
                checked={live}
                onChange={(e) => setLive(e.target.checked)}
              />
              Live{cursor ? " (paused on older pages)" : ""}
            </label>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="querylog-refresh"
              onClick={refresh}
              disabled={q.isFetching}
              aria-label="Refresh"
            >
              <RefreshCw
                className={`mr-1.5 h-4 w-4 ${q.isFetching ? "animate-spin" : ""}`}
              />
              Refresh
            </Button>
            {q.data && (
              <span className="text-muted-foreground flex items-center gap-2">
                Backend
                <Badge
                  variant="secondary"
                  className="font-mono font-medium"
                  data-testid="querylog-backend"
                >
                  {q.data.backend}
                </Badge>
              </span>
            )}
          </div>
        }
      />

      <Card className="mb-4 p-4">
        <form
          onSubmit={search}
          className="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1.3fr)_repeat(5,minmax(0,1fr))_auto]"
          role="search"
        >
          <Field
            label="Name"
            htmlFor="querylog-name"
            className="col-span-2 md:col-span-2 xl:col-span-1"
          >
            <Input
              id="querylog-name"
              data-testid="querylog-name"
              className="font-mono"
              placeholder="example.com"
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </Field>
          <Field
            label="Client"
            htmlFor="querylog-client"
            className="col-span-2 md:col-span-2 xl:col-span-1"
          >
            <Input
              id="querylog-client"
              data-testid="querylog-client"
              className="font-mono"
              placeholder="192.0.2.10"
              value={form.client}
              onChange={(e) => set("client", e.target.value)}
            />
          </Field>
          <FilterSelect
            label="Type"
            id="querylog-qtype"
            value={form.qtype}
            options={qtypes}
            onChange={(v) => set("qtype", v)}
          />
          <FilterSelect
            label="Response"
            id="querylog-rcode"
            value={form.rcode}
            options={rcodes}
            onChange={(v) => set("rcode", v)}
          />
          <FilterSelect
            label="Cache"
            id="querylog-cache"
            value={form.cache}
            options={cacheStates}
            onChange={(v) => set("cache", v)}
          />
          <FilterSelect
            label="Filter"
            id="querylog-filter"
            value={form.filter}
            options={filterStates}
            onChange={(v) => set("filter", v)}
          />
          <FilterSelect
            label="Category"
            id="querylog-category"
            value={form.category}
            options={(categories.data ?? []).map((c) => c.key)}
            onChange={(v) => set("category", v)}
          />
          <div className="col-span-1 flex items-end gap-2 md:col-span-3 xl:col-span-1">
            <Button
              type="submit"
              data-testid="querylog-search"
              className="h-9 flex-1 xl:flex-none"
            >
              <Search className="mr-1.5 h-4 w-4" />
              Search
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-9 w-9"
              aria-label="Clear filters"
              title="Clear filters"
              onClick={reset}
            >
              <FilterX className="h-4 w-4" />
            </Button>
          </div>
        </form>
      </Card>

      {unavailable ? (
        <Alert
          variant="destructive"
          data-testid="querylog-unavailable"
          className="mb-4"
        >
          <AlertTitle>Query log backend unavailable</AlertTitle>
          <AlertDescription>
            The configured query log store is not answering. Everything else
            keeps working; try again shortly.
          </AlertDescription>
        </Alert>
      ) : (
        <ErrorAlert
          error={q.error}
          prefix="Could not search the query log"
          className="mb-4"
        />
      )}

      <Card className="overflow-hidden">
        <Table
          className={cn(q.isFetching && q.isPlaceholderData && "opacity-60")}
        >
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Time</TableHead>
              <TableHead className="h-10">Client</TableHead>
              <TableHead className="h-10">Name</TableHead>
              <TableHead className="h-10">Type</TableHead>
              <TableHead className="h-10">Response</TableHead>
              <TableHead className="h-10">Cache</TableHead>
              <TableHead className="h-10">Filter</TableHead>
              <TableHead className="h-10">Category</TableHead>
              <TableHead className="h-10">Upstream</TableHead>
              <TableHead className="h-10 text-right">Duration</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {!unavailable &&
              records.map((r, i) => <RecordRow key={`${r.time}-${i}`} r={r} />)}
            {q.isPending && <MessageRow colSpan={10}>Loading…</MessageRow>}
            {q.isSuccess && records.length === 0 && (
              <MessageRow colSpan={10}>
                No queries match these filters.
              </MessageRow>
            )}
            {unavailable && (
              <MessageRow colSpan={10}>
                No results while the backend is down.
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>

      {(cursors.length > 0 || q.data?.next_cursor) && (
        <div className="mt-4 flex items-center justify-end gap-2">
          <span className="text-muted-foreground mr-2 text-sm">
            Page {cursors.length + 1}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={cursors.length === 0 || q.isFetching}
            onClick={() => setCursors((c) => c.slice(0, -1))}
          >
            <ChevronLeft className="mr-1 h-4 w-4" />
            Newer
          </Button>
          <Button
            variant="outline"
            size="sm"
            data-testid="querylog-next"
            disabled={!q.data?.next_cursor || q.isFetching}
            onClick={() =>
              q.data?.next_cursor &&
              setCursors((c) => [...c, q.data.next_cursor])
            }
          >
            Next page
            <ChevronRight className="ml-1 h-4 w-4" />
          </Button>
        </div>
      )}
    </>
  );
}

function RecordRow({ r }: { r: QueryLogRecord }) {
  const failed = r.rcode === "SERVFAIL" || r.rcode === "REFUSED";
  return (
    <TableRow data-testid="querylog-row">
      <TableCell className="py-2 whitespace-nowrap tabular-nums">
        <time dateTime={r.time}>{timeFormat.format(new Date(r.time))}</time>
      </TableCell>
      <TableCell className="py-2 font-mono text-[13px]">{r.client}</TableCell>
      <TableCell
        className="max-w-[28rem] truncate py-2 font-mono text-[13px]"
        title={r.name}
      >
        {r.name}
      </TableCell>
      <TableCell className="py-2 font-mono text-[13px]">{r.qtype}</TableCell>
      <TableCell
        className={cn(
          "py-2 font-mono text-[13px]",
          failed && "text-destructive",
          r.rcode === "NXDOMAIN" && "text-muted-foreground",
        )}
      >
        {r.rcode}
      </TableCell>
      <TableCell className="text-muted-foreground py-2">
        {r.cache === "none" ? "—" : r.cache}
      </TableCell>
      <TableCell className="py-2">
        {r.filter === "blocked" ? (
          <Badge variant="destructive">blocked</Badge>
        ) : r.filter === "allowed" ? (
          <Badge variant="secondary">allowed</Badge>
        ) : r.filter === "rewritten" ? (
          <Badge variant="outline">rewritten</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>
      <TableCell
        className="py-2 whitespace-nowrap"
        title={r.list_id || undefined}
      >
        {r.category || <span className="text-muted-foreground">—</span>}
      </TableCell>
      <TableCell className="py-2 whitespace-nowrap">
        {r.upstream || <span className="text-muted-foreground">—</span>}
      </TableCell>
      <TableCell className="py-2 text-right whitespace-nowrap tabular-nums">
        {formatDuration(r.duration_us)}
      </TableCell>
    </TableRow>
  );
}

function formatDuration(us: number): string {
  if (us < 1000) return `${us} µs`;
  if (us < 100_000) return `${(us / 1000).toFixed(1)} ms`;
  return `${Math.round(us / 1000)} ms`;
}

function Field({
  label,
  htmlFor,
  className,
  children,
}: {
  label: string;
  htmlFor: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("grid gap-1.5", className)}>
      <Label htmlFor={htmlFor} className="text-muted-foreground text-xs">
        {label}
      </Label>
      {children}
    </div>
  );
}

function FilterSelect({
  label,
  id,
  value,
  options,
  onChange,
}: {
  label: string;
  id: string;
  value: string;
  options: string[];
  onChange: (v: string) => void;
}) {
  return (
    <Field label={label} htmlFor={id}>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger id={id} data-testid={id} className="h-9">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={any}>Any</SelectItem>
          {options.map((o) => (
            <SelectItem key={o} value={o}>
              {o}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  );
}
