import { useCallback, useState, type FormEvent, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  FilterX,
  RefreshCw,
  Search,
} from "lucide-react";

import { api, ApiError, unwrap, type Schemas } from "@/api/client";
import type { paths } from "@/api/schema";
import { useFilterCategories } from "@/api/filterCategories";
import { useEngines } from "@/api/fleet";
import { usePolicyGroups } from "@/api/policies";
import { AnomalyBanner } from "@/components/ai/AnomalyBanner";
import { QueryLogAsk } from "@/components/ai/QueryLogAsk";
import { ThreatBadge } from "@/components/ai/ThreatBadge";
import { ErrorAlert, MessageRow } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { MultiSelect } from "@/components/MultiSelect";
import { PageHeader } from "@/components/layout/AppShell";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatTimestamp, usePreferences } from "@/lib/preferences";
import { cn } from "@/lib/utils";

type QueryLogRecord = Schemas["QueryLogRecord"];
type SearchQuery = NonNullable<
  paths["/query-log"]["get"]["parameters"]["query"]
>;

const pageSize = 100;
const columns = 11;

const opts = (values: string[]) => values.map((value) => ({ value }));
const qtypes = opts([
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
]);
const rcodes = opts(["NOERROR", "NXDOMAIN", "SERVFAIL", "REFUSED", "FORMERR"]);
const cacheStates = opts(["hit", "miss", "stale", "none", "auth"]);
const filterStates = opts(["none", "blocked", "allowed", "rewritten"]);
const sourceLabels: Record<string, string> = {
  blocklist: "Blocklist",
  category: "Category",
  allowlist: "Allowlist",
  rpz: "RPZ",
  rewrite: "Rewrite",
  acl: "Refused",
};
const sources = Object.entries(sourceLabels).map(([value, label]) => ({
  value,
  label,
}));

// URL parameters mirror the API's: the text fields once, the list fields repeated.
const textKeys = ["name", "client"] as const;
const listKeys = [
  "qtype",
  "rcode",
  "cache",
  "filter",
  "category",
  "source",
  "list_id",
  "policy_group",
  "engine_id",
] as const;
type ListKey = (typeof listKeys)[number];
type Filters = Record<(typeof textKeys)[number], string> &
  Record<ListKey, string[]>;

function readFilters(params: URLSearchParams): Filters {
  const f = {} as Filters;
  for (const k of textKeys) f[k] = params.get(k) ?? "";
  for (const k of listKeys) f[k] = params.getAll(k);
  return f;
}

function writeFilters(f: Filters): URLSearchParams {
  const p = new URLSearchParams();
  for (const k of textKeys) if (f[k].trim() !== "") p.set(k, f[k].trim());
  for (const k of listKeys) for (const v of f[k]) p.append(k, v);
  return p;
}

const liveIntervalMs = 5000;

export function QueryLogPage() {
  const [params, setParams] = useSearchParams();
  const urlKey = params.toString();
  const [form, setForm] = useState<Filters>(() => readFilters(params));
  // Back, forward and a pasted link change the URL: show its filters from the first page. The
  // state is adjusted while rendering (not in an effect) so the stale form never paints.
  const [shownUrl, setShownUrl] = useState(urlKey);
  // Cursors of the pages before the current one; the current page's cursor is last.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors[cursors.length - 1];
  const categories = useFilterCategories();
  const groups = usePolicyGroups();
  const engines = useEngines();
  const prefs = usePreferences();
  // Live mode reloads the newest page every liveIntervalMs; older pages stay put while browsing.
  const [live, setLive] = useState(prefs.querylog_live);

  if (shownUrl !== urlKey) {
    setShownUrl(urlKey);
    setForm(readFilters(params));
    setCursors([]);
  }

  const q = useQuery({
    queryKey: ["query-log", urlKey, cursor],
    queryFn: async () => {
      const p = new URLSearchParams(urlKey);
      const f = readFilters(p);
      const list = (k: ListKey) => (f[k].length > 0 ? f[k] : undefined);
      const query: SearchQuery = {
        // from/to have no form control: only an AI search writes them into the URL.
        from: p.get("from") ?? undefined,
        to: p.get("to") ?? undefined,
        name: f.name || undefined,
        client: f.client || undefined,
        qtype: list("qtype"),
        rcode: list("rcode"),
        cache: list("cache") as SearchQuery["cache"],
        filter: list("filter") as SearchQuery["filter"],
        category: list("category"),
        source: list("source") as SearchQuery["source"],
        list_id: list("list_id"),
        policy_group: list("policy_group"),
        engine_id: list("engine_id"),
        limit: pageSize,
        cursor,
      };
      return unwrap(await api.GET("/query-log", { params: { query } }));
    },
    placeholderData: keepPreviousData,
    retry: false,
    refetchInterval: live && !cursor ? liveIntervalMs : false,
    refetchIntervalInBackground: false,
  });

  const set = <K extends keyof Filters>(key: K, value: Filters[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  function search(e: FormEvent) {
    e.preventDefault();
    const next = writeFilters(form);
    setCursors([]);
    if (next.toString() === urlKey) {
      if (!cursor) void q.refetch();
    } else {
      setParams(next);
    }
  }

  function reset() {
    setForm(readFilters(new URLSearchParams()));
    setCursors([]);
    setParams(new URLSearchParams());
  }

  // An AI search answers with the same filters the API takes: they become the URL, so the table
  // reloads through the normal search and the link can be shared.
  const applyAiFilters = useCallback(
    (f: Schemas["AiQueryLogFilters"]) => {
      const p = new URLSearchParams();
      for (const [key, value] of Object.entries(f)) {
        if (typeof value === "string") {
          if (value !== "") p.set(key, value);
        } else if (Array.isArray(value)) {
          for (const v of value) p.append(key, v);
        }
      }
      setCursors([]);
      setParams(p);
    },
    [setParams],
  );

  // Refresh reloads the page on screen now (a no-op filter change would not trigger a request).
  function refresh() {
    void q.refetch();
  }

  const unavailable =
    q.error instanceof ApiError && q.error.code === "querylog_unavailable";
  const records = q.data?.records ?? [];

  // One filter per list parameter. Each is written out so check-help sees its id.
  const multi = (
    key: Exclude<ListKey, "list_id">,
    id: string,
    label: string,
    options: { value: string; label?: string }[],
  ) => (
    <MultiSelect
      id={id}
      label={label}
      options={options}
      value={form[key]}
      onChange={(v) => set(key, v)}
    />
  );
  const categoryOptions = (categories.data ?? []).map((c) => ({
    value: c.key,
  }));
  const groupOptions = [
    { value: "global", label: "Global" },
    ...(groups.data ?? []).map((g) => ({ value: g.id, label: g.name })),
  ];
  const engineOptions = (engines.data ?? []).map((e) => ({
    value: e.id,
    label: e.node_name,
  }));

  return (
    <>
      <PageHeader
        title="Query log"
        description="Every DNS query Nexora answered, newest first, with why it was blocked, allowed, rewritten or refused."
        actions={
          <div className="flex flex-wrap items-center gap-3 text-sm">
            {q.dataUpdatedAt > 0 && (
              <span
                className="text-muted-foreground"
                data-testid="querylog-updated"
                aria-live="polite"
              >
                Updated {formatTimestamp(new Date(q.dataUpdatedAt), prefs)}
              </span>
            )}
            <span className="flex items-center gap-1.5">
              <label className="text-muted-foreground flex cursor-pointer items-center gap-1.5">
                <input
                  type="checkbox"
                  id="querylog-live"
                  data-testid="querylog-live"
                  className="accent-primary h-4 w-4"
                  checked={live}
                  onChange={(e) => setLive(e.target.checked)}
                />
                Live{cursor ? " (paused on older pages)" : ""}
              </label>
              <HelpTip id="querylog-live" label="Live" />
            </span>
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

      <AnomalyBanner />
      <QueryLogAsk onFilters={applyAiFilters} />

      <Card className="mb-4 p-4">
        <form
          onSubmit={search}
          className="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-6"
          role="search"
        >
          <Field
            label="Name"
            htmlFor="querylog-name"
            tip={<HelpTip id="querylog-name" label="Name" />}
            className="col-span-2"
          >
            <Input
              id="querylog-name"
              data-testid="querylog-name"
              className="font-mono"
              placeholder="Part of a name, e.g. youtube"
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </Field>
          <Field
            label="Client"
            htmlFor="querylog-client"
            tip={<HelpTip id="querylog-client" label="Client" />}
            className="col-span-2"
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
          <Field
            label="Type"
            htmlFor="querylog-qtype"
            tip={<HelpTip id="querylog-qtype" label="Type" />}
          >
            {multi("qtype", "querylog-qtype", "Type", qtypes)}
          </Field>
          <Field
            label="Response"
            htmlFor="querylog-rcode"
            tip={<HelpTip id="querylog-rcode" label="Response" />}
          >
            {multi("rcode", "querylog-rcode", "Response", rcodes)}
          </Field>
          <Field
            label="Cache"
            htmlFor="querylog-cache"
            tip={<HelpTip id="querylog-cache" label="Cache" />}
          >
            {multi("cache", "querylog-cache", "Cache", cacheStates)}
          </Field>
          <Field
            label="Filter"
            htmlFor="querylog-filter"
            tip={<HelpTip id="querylog-filter" label="Filter" />}
          >
            {multi("filter", "querylog-filter", "Filter", filterStates)}
          </Field>
          <Field
            label="Category"
            htmlFor="querylog-category"
            tip={<HelpTip id="querylog-category" label="Category" />}
          >
            {multi(
              "category",
              "querylog-category",
              "Category",
              categoryOptions,
            )}
          </Field>
          <Field
            label="Source"
            htmlFor="querylog-source"
            tip={<HelpTip id="querylog-source" label="Source" />}
          >
            {multi("source", "querylog-source", "Source", sources)}
          </Field>
          <Field
            label="Policy group"
            htmlFor="querylog-policy-group"
            tip={<HelpTip id="querylog-policy-group" label="Policy group" />}
          >
            {multi(
              "policy_group",
              "querylog-policy-group",
              "Policy group",
              groupOptions,
            )}
          </Field>
          <Field
            label="Engine"
            htmlFor="querylog-engine"
            tip={<HelpTip id="querylog-engine" label="Engine" />}
          >
            {multi("engine_id", "querylog-engine", "Engine", engineOptions)}
          </Field>
          <div className="col-span-full flex items-end justify-end gap-2">
            <Button
              type="submit"
              data-testid="querylog-search"
              className="h-9 flex-1 sm:flex-none"
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
              <TableHead className="h-10 w-8">
                <span className="sr-only">Details</span>
              </TableHead>
              <TableHead className="h-10">Time</TableHead>
              <TableHead className="h-10">Client</TableHead>
              <TableHead className="h-10">Name</TableHead>
              <TableHead className="h-10">Type</TableHead>
              <TableHead className="h-10">Response</TableHead>
              <TableHead className="h-10">Cache</TableHead>
              <TableHead className="h-10">Filter</TableHead>
              <TableHead className="h-10">
                <span className="inline-flex items-center gap-1.5">
                  Reason
                  <HelpTip id="querylog-col-reason" label="Reason" />
                </span>
              </TableHead>
              <TableHead className="h-10">Upstream</TableHead>
              <TableHead className="h-10 text-right">Duration</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {!unavailable &&
              records.map((r, i) => <RecordRow key={`${r.time}-${i}`} r={r} />)}
            {q.isPending && <MessageRow colSpan={columns}>Loading…</MessageRow>}
            {q.isSuccess && records.length === 0 && (
              <MessageRow colSpan={columns}>
                No queries match these filters.
              </MessageRow>
            )}
            {unavailable && (
              <MessageRow colSpan={columns}>
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

/** The decision reason: source label, then what matched, e.g. "Category · HaGeZi TIF · malware". */
function reasonText(r: QueryLogRecord): string {
  const parts = (...p: string[]) => p.filter((x) => x !== "").join(" · ");
  switch (r.source) {
    case "blocklist":
    case "category":
      return parts(sourceLabels[r.source], r.list_name || r.rule, r.category);
    case "allowlist":
      return parts("Allowlist", r.list_name, r.rule);
    case "rpz":
      return parts("RPZ", r.rpz_zone_name, r.rpz_action);
    case "rewrite":
      return parts(
        "Rewrite",
        r.rewrite_answer ? `${r.rule} → ${r.rewrite_answer}` : r.rule,
      );
    case "acl":
      return `Refused · ${r.rule} access`;
    default:
      return "—";
  }
}

function RecordRow({ r }: { r: QueryLogRecord }) {
  const [open, setOpen] = useState(false);
  const prefs = usePreferences();
  const failed = r.rcode === "SERVFAIL" || r.rcode === "REFUSED";
  const reason = reasonText(r);
  return (
    <>
      <TableRow data-testid="querylog-row">
        <TableCell className="w-8 py-2 pr-0">
          <button
            type="button"
            data-testid="querylog-row-toggle"
            aria-expanded={open}
            aria-label={open ? "Hide details" : "Show details"}
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring flex h-6 w-6 items-center justify-center rounded-sm focus-visible:ring-1 focus-visible:outline-none"
            onClick={() => setOpen((o) => !o)}
          >
            <ChevronDown
              className={cn(
                "h-4 w-4 transition-transform",
                !open && "-rotate-90",
              )}
            />
          </button>
        </TableCell>
        <TableCell className="py-2 whitespace-nowrap tabular-nums">
          <time dateTime={r.time}>
            {formatTimestamp(new Date(r.time), prefs)}
          </time>
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
          className={cn(
            "max-w-[24rem] truncate py-2 whitespace-nowrap",
            reason === "—" && "text-muted-foreground",
          )}
          data-testid="querylog-reason"
          title={reason === "—" ? undefined : reason}
        >
          {r.threat?.is_threat ? (
            <span className="flex items-center gap-2">
              <ThreatBadge threat={r.threat} />
              {reason !== "—" && <span className="truncate">{reason}</span>}
            </span>
          ) : (
            reason
          )}
        </TableCell>
        <TableCell className="py-2 whitespace-nowrap">
          {r.upstream || <span className="text-muted-foreground">—</span>}
        </TableCell>
        <TableCell className="py-2 text-right whitespace-nowrap tabular-nums">
          {formatDuration(r.duration_us)}
        </TableCell>
      </TableRow>
      {open && (
        <TableRow className="bg-muted/40 hover:bg-muted/40">
          <TableCell colSpan={columns} className="py-3">
            <dl
              data-testid="querylog-detail"
              className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[13px] sm:grid-cols-[auto_1fr_auto_1fr]"
            >
              <Detail label="Rule">{r.rule}</Detail>
              <Detail label="List">
                {[r.list_name, r.list_id && `(${r.list_id})`]
                  .filter(Boolean)
                  .join(" ")}
              </Detail>
              <Detail label="Policy group">
                {r.policy_group_id
                  ? r.policy_group_name || r.policy_group_id
                  : "Global"}
              </Detail>
              <Detail label="RPZ">
                {[r.rpz_zone_name, r.rpz_action].filter(Boolean).join(" · ")}
              </Detail>
              <Detail label="Upstream">
                {r.upstream &&
                  `${r.upstream}${r.upstreams_raced > 1 ? ` (raced ${r.upstreams_raced})` : ""}`}
              </Detail>
              <Detail label="Engine">{r.engine_id}</Detail>
            </dl>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function Detail({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="font-mono break-all">
        {children || <span className="text-muted-foreground">—</span>}
      </dd>
    </>
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
  tip,
  className,
  children,
}: {
  label: string;
  htmlFor: string;
  tip: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("grid gap-1.5", className)}>
      <div className="flex items-center gap-1.5">
        <Label htmlFor={htmlFor} className="text-muted-foreground text-xs">
          {label}
        </Label>
        {tip}
      </div>
      {children}
    </div>
  );
}
