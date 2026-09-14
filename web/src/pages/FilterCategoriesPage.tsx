import { useState } from "react";
import { ExternalLink } from "lucide-react";

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
import { Card } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type Category = Schemas["FilterCategory"];
type Source = Schemas["FilterCategorySource"];
type Update = Schemas["FilterCategoryUpdate"];

type Pending = { key: string; body: Update; notices: LicenseNotice[] };

export function FilterCategoriesPage() {
  const canUpdate = useCan("updateFilterCategory");
  const categories = useFilterCategories();
  const update = useUpdateFilterCategory();
  const [pending, setPending] = useState<Pending | null>(null);

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
      <div className="grid gap-5">
        {(categories.data ?? []).map((c) => (
          <CategoryCard
            key={c.key}
            category={c}
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
  disabled,
  onChange,
}: {
  category: Category;
  disabled: boolean;
  onChange: (body: Update) => void;
}) {
  const enabledSources = c.sources.filter((s) => s.enabled).length;
  const headingId = `category-heading-${c.key}`;
  return (
    <Card
      data-testid={`category-${c.key}`}
      aria-labelledby={headingId}
      role="region"
      className="overflow-hidden"
    >
      <div className="flex items-start justify-between gap-4 px-5 py-4">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 id={headingId} className="font-semibold">
              {c.name}
            </h2>
            {c.stale && (
              <Badge
                variant="destructive"
                data-testid={`category-stale-${c.key}`}
              >
                stale
              </Badge>
            )}
          </div>
          <p className="text-muted-foreground mt-0.5 text-sm">
            {c.description}
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {c.enabled ? "Blocking for every client" : "Off globally"} ·{" "}
            {enabledSources} of {c.sources.length} sources enabled
          </p>
        </div>
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
