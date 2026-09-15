import { useState, type FormEvent } from "react";
import { Plus, Search } from "lucide-react";

import { type Schemas } from "@/api/client";
import { useDeleteRecord, useRecords } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import { ConfirmDialog, ErrorAlert, MessageRow } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
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
import { absoluteName, recordTypes, relativeName } from "@/lib/zoneRdataHints";
import { ZoneRecordEditor } from "@/pages/ZoneRecordEditor";

type Zone = Schemas["Zone"];
type RecordRow = Schemas["Record"];

export function ZoneRecordsTab({ zone }: { zone: Zone }) {
  const editable = zone.kind === "primary";
  const canCreate = useCan("createZoneRecord") && editable;
  const canUpdate = useCan("updateZoneRecord") && editable;
  const canDelete = useCan("deleteZoneRecord") && editable;
  const [nameInput, setNameInput] = useState("");
  const [filter, setFilter] = useState<{ name?: string; type?: string }>({});
  const records = useRecords(zone.id, filter);
  const del = useDeleteRecord(zone.id);
  const [editing, setEditing] = useState<RecordRow | "new" | null>(null);
  const [deleting, setDeleting] = useState<RecordRow | null>(null);
  const rows = records.data?.pages.flatMap((p) => p.items) ?? [];
  const actions = canUpdate || canDelete;
  const cols = 4 + (actions ? 1 : 0);

  function applyName(e: FormEvent) {
    e.preventDefault();
    const v = nameInput.trim();
    setFilter((f) => ({
      ...f,
      name: v === "" ? undefined : absoluteName(v, zone.name),
    }));
  }

  return (
    <>
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <form onSubmit={applyName} className="grid gap-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="record-filter-name">Owner</Label>
            <HelpTip id="record-filter-name" label="Owner" />
          </div>
          <div className="relative">
            <Search className="text-muted-foreground pointer-events-none absolute top-2.5 left-2.5 h-4 w-4" />
            <Input
              id="record-filter-name"
              className="w-56 pl-8 font-mono"
              placeholder="@ or www, Enter to filter"
              value={nameInput}
              onChange={(e) => setNameInput(e.target.value)}
              onBlur={applyName}
            />
          </div>
        </form>
        <div className="grid gap-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="record-filter-type">Record type</Label>
            <HelpTip id="record-filter-type" label="Record type" />
          </div>
          <Select
            value={filter.type ?? "all"}
            onValueChange={(v) =>
              setFilter((f) => ({ ...f, type: v === "all" ? undefined : v }))
            }
          >
            <SelectTrigger id="record-filter-type" className="h-9 w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All types</SelectItem>
              {recordTypes.map((t) => (
                <SelectItem key={t} value={t}>
                  {t}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="ml-auto flex items-center gap-3">
          {!editable && (
            <span className="text-muted-foreground text-sm">
              Records of a secondary zone come from its primaries.
            </span>
          )}
          {canCreate && (
            <Button onClick={() => setEditing("new")}>
              <Plus className="mr-1.5 h-4 w-4" />
              Add record
            </Button>
          )}
        </div>
      </div>
      <ErrorAlert
        error={records.error}
        prefix="Could not load records"
        className="mb-3"
      />
      <Card className="overflow-hidden">
        <Table aria-label="Records">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead className="w-24">Type</TableHead>
              <TableHead className="w-24 text-right">TTL</TableHead>
              <TableHead>Data</TableHead>
              {actions && (
                <TableHead className="w-40 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.id}>
                <TableCell className="py-2 font-mono text-[13px]">
                  {relativeName(r.name, zone.name)}
                </TableCell>
                <TableCell className="py-2 text-[13px] font-medium">
                  {r.type}
                </TableCell>
                <TableCell className="py-2 text-right tabular-nums">
                  {r.ttl}
                </TableCell>
                <TableCell className="max-w-xl py-2 font-mono text-[13px] break-all">
                  {r.data}
                </TableCell>
                {actions && (
                  <TableCell className="py-1.5 text-right whitespace-nowrap">
                    {canUpdate && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8"
                        onClick={() => setEditing(r)}
                      >
                        Edit
                      </Button>
                    )}
                    {canDelete && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="hover:text-destructive h-8"
                        onClick={() => setDeleting(r)}
                      >
                        Delete
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {records.isPending && (
              <MessageRow colSpan={cols}>Loading…</MessageRow>
            )}
            {records.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>
                {filter.name || filter.type
                  ? "No records match the filter."
                  : "No records."}
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      <div className="text-muted-foreground mt-3 flex items-center gap-3 text-sm">
        {records.isSuccess && (
          <span className="tabular-nums">
            {rows.length} record{rows.length === 1 ? "" : "s"} shown
            {records.hasNextPage && ", more available"}
          </span>
        )}
        {records.hasNextPage && (
          <Button
            variant="outline"
            size="sm"
            disabled={records.isFetchingNextPage}
            onClick={() => void records.fetchNextPage()}
          >
            {records.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        )}
      </div>

      {editing !== null && (
        <ZoneRecordEditor
          zoneId={zone.id}
          zoneName={zone.name}
          record={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete record"
          description={`Delete the ${deleting.type} record ${relativeName(deleting.name, zone.name)} → ${deleting.data}?`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This record"
          onConfirm={() => del.mutateAsync(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}
