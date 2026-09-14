import { useEffect, useState } from "react";
import { useLocation } from "react-router";
import { ChevronRight, ExternalLink } from "lucide-react";

import { type Schemas } from "@/api/client";
import {
  licenseNoticesFromError,
  needsAcknowledgement,
  useFilterCategories,
  useUpdateFilterCategory,
  type LicenseNotice,
} from "@/api/filterCategories";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert, formatAgo, StatusDot } from "@/components/common";
import { LicenseNoticeDialog } from "@/components/categories";
import { PageHeader } from "@/components/layout/AppShell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

type Category = Schemas["FilterCategory"];
type Source = Schemas["FilterCategorySource"];
type Update = Schemas["FilterCategoryUpdate"];

type Pending = { key: string; body: Update; notices: LicenseNotice[] };

const EXPANDED_KEY = "nexora-categories-expanded";

// The stored expanded set is a convenience: unreadable, corrupt or non-array values start collapsed.
function readExpanded(): Set<string> {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(EXPANDED_KEY) ?? "[]");
    return new Set(
      Array.isArray(v)
        ? v.filter((k): k is string => typeof k === "string")
        : [],
    );
  } catch {
    return new Set();
  }
}

function writeExpanded(keys: Set<string>) {
  try {
    localStorage.setItem(EXPANDED_KEY, JSON.stringify([...keys]));
  } catch {
    // Storage unavailable (private mode, quota): the state lasts for this visit only.
  }
}

function matches(c: Category, query: string): boolean {
  const q = query.trim().toLowerCase();
  const hit = (v: string) => v.toLowerCase().includes(q);
  return (
    hit(c.name) ||
    hit(c.key) ||
    c.sources.some((s) => hit(s.name) || hit(s.key))
  );
}

export function FilterCategoriesPage() {
  const canUpdate = useCan("updateFilterCategory");
  const categories = useFilterCategories();
  const update = useUpdateFilterCategory();
  const [pending, setPending] = useState<Pending | null>(null);
  const [expanded, setExpanded] = useState(readExpanded);
  const [query, setQuery] = useState("");
  // Categories the operator collapsed while a search forces its matches open; reset per query.
  const [searchClosed, setSearchClosed] = useState<Set<string>>(
    () => new Set(),
  );
  const { hash } = useLocation();
  const linked = hash.startsWith("#category-")
    ? hash.slice("#category-".length)
    : "";
  const loaded = categories.data !== undefined;

  useEffect(() => writeExpanded(expanded), [expanded]);

  // A #category-<key> link expands its category, on arrival and on in-app hash changes.
  const [seenLink, setSeenLink] = useState("");
  if (linked !== seenLink) {
    setSeenLink(linked);
    if (linked !== "" && !expanded.has(linked)) {
      setExpanded(new Set(expanded).add(linked));
    }
  }

  useEffect(() => {
    if (linked !== "" && loaded) {
      document.getElementById(`category-${linked}`)?.scrollIntoView();
    }
  }, [linked, loaded]);

  const searching = query.trim() !== "";
  const visible = (categories.data ?? []).filter(
    (c) => !searching || matches(c, query),
  );
  const isOpen = (key: string) =>
    searching ? !searchClosed.has(key) : expanded.has(key);

  function toggle(key: string) {
    const flip = (prev: Set<string>) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    };
    if (searching) setSearchClosed(flip);
    else setExpanded(flip);
  }

  function setAll(open: boolean) {
    const keys = (categories.data ?? []).map((c) => c.key);
    setExpanded(new Set(open ? keys : []));
    setSearchClosed(new Set(open ? [] : keys));
  }

  function send(key: string, body: Update) {
    update.mutate(
      { key, body },
      {
        onError: (err) => {
          // The server has the final word (a policy group may select the category): its refusal
          // opens the same notice instead of an error.
          const notices = licenseNoticesFromError(err, categories.data);
          if (notices && !body.acknowledge_license) {
            setPending({ key, body, notices });
          }
        },
      },
    );
  }

  function apply(category: Category, body: Update) {
    update.reset();
    const notices = needsAcknowledgement(category, body);
    if (notices.length > 0) {
      setPending({ key: category.key, body, notices });
      return;
    }
    send(category.key, body);
  }

  return (
    <>
      <PageHeader
        title="Filter categories"
        description="Curated block lists by category. Sources, licenses and attribution ship with Nexora and cannot be edited; turn categories and individual sources on or off."
      />
      <ErrorAlert
        error={categories.error}
        prefix="Could not load categories"
        className="mb-4"
      />
      {pending === null && (
        <ErrorAlert
          error={update.error}
          prefix="Could not update the category"
          thing="This category"
          className="mb-4"
        />
      )}
      {!canUpdate && categories.data && (
        <p className="text-muted-foreground mb-4 text-sm">
          Operators and administrators can enable categories and sources.
        </p>
      )}
      {categories.isPending && (
        <p className="text-muted-foreground text-sm">Loading…</p>
      )}
      {categories.data && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <Input
            data-testid="categories-search"
            type="search"
            aria-label="Search categories and sources"
            placeholder="Search categories and sources"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setSearchClosed(new Set());
            }}
            className="w-full sm:w-72"
          />
          <Button
            data-testid="categories-expand-all"
            variant="outline"
            size="sm"
            onClick={() => setAll(true)}
          >
            Expand all
          </Button>
          <Button
            data-testid="categories-collapse-all"
            variant="outline"
            size="sm"
            onClick={() => setAll(false)}
          >
            Collapse all
          </Button>
        </div>
      )}
      {categories.data && visible.length === 0 && (
        <p className="text-muted-foreground text-sm">
          No category or source matches “{query.trim()}”.
        </p>
      )}
      <div className="grid gap-3">
        {visible.map((c) => (
          <CategoryCard
            key={c.key}
            category={c}
            open={isOpen(c.key)}
            onToggleOpen={() => toggle(c.key)}
            disabled={!canUpdate || update.isPending || pending !== null}
            onChange={(body) => apply(c, body)}
          />
        ))}
      </div>
      <LicenseNoticeDialog
        notices={pending?.notices ?? null}
        confirmLabel="Acknowledge and enable"
        pending={update.isPending}
        onCancel={() => {
          update.reset();
          setPending(null);
        }}
        onConfirm={() => {
          if (pending) {
            send(pending.key, { ...pending.body, acknowledge_license: true });
          }
          setPending(null);
        }}
      />
    </>
  );
}

function CategoryCard({
  category: c,
  open,
  onToggleOpen,
  disabled,
  onChange,
}: {
  category: Category;
  open: boolean;
  onToggleOpen: () => void;
  disabled: boolean;
  onChange: (body: Update) => void;
}) {
  const enabledSources = c.sources.filter((s) => s.enabled);
  const entries = enabledSources.reduce((n, s) => n + s.entry_count, 0);
  const failed = enabledSources.some((s) => s.last_error);
  const nonCommercial = c.sources.some((s) => s.commercial_use === false);
  const headingId = `category-heading-${c.key}`;
  const sourcesId = `category-sources-${c.key}`;
  return (
    <Card
      id={`category-${c.key}`}
      data-testid={`category-${c.key}`}
      aria-labelledby={headingId}
      role="region"
      className="scroll-mt-4 overflow-hidden"
    >
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        {/* Accordion pattern: the heading wraps the disclosure button, the switch stays outside it. */}
        <h2 className="min-w-0 flex-1 basis-64">
          <button
            type="button"
            data-testid={`category-toggle-${c.key}`}
            aria-expanded={open}
            aria-controls={sourcesId}
            onClick={onToggleOpen}
            className="focus-visible:ring-ring flex w-full items-start gap-2 rounded-md text-left focus-visible:ring-1 focus-visible:outline-none"
          >
            <ChevronRight
              aria-hidden
              className={cn(
                "text-muted-foreground mt-0.5 h-4 w-4 shrink-0 transition-transform",
                open && "rotate-90",
              )}
            />
            <span className="min-w-0">
              <span id={headingId} className="block font-semibold">
                {c.name}
              </span>
              <span className="text-muted-foreground block text-sm font-normal">
                {c.description}
              </span>
            </span>
          </button>
        </h2>
        <span
          data-testid={`category-summary-${c.key}`}
          className="text-muted-foreground text-xs whitespace-nowrap tabular-nums"
        >
          {enabledSources.length} of {c.sources.length} sources on
          {entries > 0 && ` · ${entries.toLocaleString()} entries`}
        </span>
        {failed ? (
          <span data-testid={`category-stale-${c.key}`}>
            <StatusDot tone="destructive">Failed</StatusDot>
          </span>
        ) : (
          c.stale && (
            <span data-testid={`category-stale-${c.key}`}>
              <StatusDot tone="warning">Stale</StatusDot>
            </span>
          )
        )}
        {nonCommercial && (
          <Badge
            variant="outline"
            data-testid={`category-noncommercial-${c.key}`}
            title="A source in this category is not free for commercial use"
            className="border-warning/50 text-warning whitespace-nowrap"
          >
            Non-commercial
          </Badge>
        )}
        <Switch
          aria-label={`Enable ${c.name}`}
          checked={c.enabled}
          disabled={disabled}
          onCheckedChange={(v) =>
            onChange({
              enabled: v,
              revision: c.revision,
              acknowledge_license: false,
            })
          }
        />
      </div>
      <div id={sourcesId}>
        {open && (
          <Table className="border-t">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-5">Source</TableHead>
                <TableHead>License</TableHead>
                <TableHead>Attribution</TableHead>
                <TableHead className="text-right">Entries</TableHead>
                <TableHead>Last refresh</TableHead>
                <TableHead className="w-20 pr-5">Enabled</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {c.sources.map((s) => (
                <SourceRow
                  key={s.key}
                  source={s}
                  disabled={disabled}
                  onToggle={(v) =>
                    onChange({
                      enabled: c.enabled,
                      revision: c.revision,
                      acknowledge_license: false,
                      sources: [{ key: s.key, enabled: v }],
                    })
                  }
                />
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </Card>
  );
}

function SourceRow({
  source: s,
  disabled,
  onToggle,
}: {
  source: Source;
  disabled: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  return (
    <TableRow data-testid={`source-row-${s.key}`}>
      <TableCell className="py-3 pl-5 align-top">
        <div className="font-medium">{s.name}</div>
        <a
          href={s.url}
          target="_blank"
          rel="noreferrer"
          className="text-muted-foreground hover:text-foreground inline-flex max-w-xs items-center gap-1 font-mono text-xs break-all underline-offset-2 hover:underline"
        >
          {s.url}
          <ExternalLink aria-hidden className="h-3 w-3 shrink-0" />
        </a>
        {s.archive_member && (
          <div className="text-muted-foreground font-mono text-xs break-all">
            member {s.archive_member}
          </div>
        )}
      </TableCell>
      <TableCell className="py-3 align-top">
        <a
          href={s.license_url}
          target="_blank"
          rel="noreferrer"
          className="whitespace-nowrap underline underline-offset-2"
        >
          {s.license}
        </a>
        {!s.commercial_use && (
          <div className="mt-1">
            <Badge
              variant="outline"
              data-testid={`source-noncommercial-${s.key}`}
              title={s.notice}
              className="border-warning/50 text-warning whitespace-nowrap"
            >
              Not free for commercial use
            </Badge>
          </div>
        )}
      </TableCell>
      <TableCell className="py-3 align-top text-sm">{s.attribution}</TableCell>
      <TableCell className="py-3 text-right align-top tabular-nums">
        {s.entry_count.toLocaleString()}
      </TableCell>
      <TableCell className="py-3 align-top">
        <div className="text-sm whitespace-nowrap">
          {formatAgo(s.last_success_at)}
        </div>
        {s.last_error ? (
          <div className="mt-0.5 max-w-xs">
            <StatusDot tone="destructive">Failed</StatusDot>
            <div className="text-destructive text-xs break-words">
              {s.last_error}
            </div>
          </div>
        ) : (
          s.stale && <StatusDot tone="warning">Stale</StatusDot>
        )}
      </TableCell>
      <TableCell className="py-3 pr-5 align-top">
        <Switch
          data-testid={`source-toggle-${s.key}`}
          aria-label={`Enable source ${s.name}`}
          checked={s.enabled}
          disabled={disabled}
          onCheckedChange={onToggle}
        />
      </TableCell>
    </TableRow>
  );
}
