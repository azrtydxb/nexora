import { Fragment, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCan } from "@/auth/AuthProvider";
import { PageHeader } from "@/components/layout/AppShell";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

type AuditEvent = Schemas["AuditEvent"];

const pageSize = 100;

const timeFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});

export function AuditPage() {
  const allowed = useCan("listAuditEvents");
  const events = useInfiniteQuery({
    queryKey: ["audit"],
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET("/audit", {
          params: { query: { limit: pageSize, before_id: pageParam } },
        }),
      ),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) =>
      last.length === pageSize ? last[last.length - 1].id : undefined,
    enabled: allowed,
  });
  const [open, setOpen] = useState<Set<number>>(new Set());

  if (!allowed) {
    return (
      <>
        <PageHeader title="Audit log" />
        <Alert>
          <AlertDescription>
            The audit log is available to administrators.
          </AlertDescription>
        </Alert>
      </>
    );
  }

  const rows = events.data?.pages.flat() ?? [];
  const toggle = (id: number) =>
    setOpen((s) => {
      const next = new Set(s);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  return (
    <>
      <PageHeader
        title="Audit log"
        description="Every change to users, tokens and configuration, newest first. Select an entry to see what changed."
      />
      {events.error && (
        <Alert variant="destructive" className="mb-4">
          <AlertDescription>
            Could not load the audit log: {events.error.message}
          </AlertDescription>
        </Alert>
      )}
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-8" />
              <TableHead>Time</TableHead>
              <TableHead>Actor</TableHead>
              <TableHead>Action</TableHead>
              <TableHead>Target</TableHead>
              <TableHead className="text-right">Version</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((ev) => (
              <AuditRow
                key={ev.id}
                ev={ev}
                expanded={open.has(ev.id)}
                onToggle={() => toggle(ev.id)}
              />
            ))}
            {events.isSuccess && rows.length === 0 && (
              <TableRow className="hover:bg-transparent">
                <TableCell
                  colSpan={6}
                  className="text-muted-foreground py-10 text-center"
                >
                  No changes recorded yet.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {events.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button
            variant="outline"
            onClick={() => void events.fetchNextPage()}
            disabled={events.isFetchingNextPage}
          >
            {events.isFetchingNextPage ? "Loading…" : "Load older"}
          </Button>
        </div>
      )}
    </>
  );
}

function AuditRow({
  ev,
  expanded,
  onToggle,
}: {
  ev: AuditEvent;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <Fragment>
      <TableRow
        data-testid="audit-row"
        data-action={ev.action}
        data-actor={ev.actor_name}
        tabIndex={0}
        aria-expanded={expanded}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onToggle();
          }
        }}
        className={cn(
          "focus-visible:bg-muted cursor-pointer focus-visible:outline-none",
          expanded && "bg-muted/50 border-b-0",
        )}
      >
        <TableCell className="py-2.5 pr-0">
          <ChevronRight
            className={cn(
              "text-muted-foreground h-4 w-4 transition-transform",
              expanded && "rotate-90",
            )}
          />
        </TableCell>
        <TableCell className="py-2.5 whitespace-nowrap tabular-nums">
          <time dateTime={ev.at}>{timeFormat.format(new Date(ev.at))}</time>
        </TableCell>
        <TableCell className="py-2.5">
          <span className="font-medium">{ev.actor_name}</span>
          {ev.actor_type !== "user" && (
            <Badge variant="outline" className="ml-2 font-normal">
              {ev.actor_type === "api_token" ? "API token" : "system"}
            </Badge>
          )}
        </TableCell>
        <TableCell className="py-2.5 font-mono text-[13px]">
          {ev.action}
        </TableCell>
        <TableCell className="py-2.5">
          <span className="text-muted-foreground">{ev.target_type}</span>
          {ev.target_id && (
            <span className="ml-1.5 font-mono text-xs">
              {ev.target_id.slice(0, 8)}
            </span>
          )}
        </TableCell>
        <TableCell className="py-2.5 text-right tabular-nums">
          {ev.config_version ?? ""}
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow className="bg-muted/50 hover:bg-muted/50">
          <TableCell colSpan={6} className="pt-0 pb-4">
            <pre
              data-testid={`audit-diff-${ev.id}`}
              className="bg-background max-h-96 overflow-auto rounded-md border p-3 font-mono text-xs leading-relaxed"
            >
              {JSON.stringify(ev.diff, null, 2)}
            </pre>
          </TableCell>
        </TableRow>
      )}
    </Fragment>
  );
}
