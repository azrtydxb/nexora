import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";

import { ApiError, type Schemas } from "@/api/client";
import { fetchRecordSet, useSaveRecord, useZone } from "@/api/zones";
import { ErrorAlert, errorMessage } from "@/components/common";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
  absoluteName,
  checkRdata,
  rdataHints,
  recordTypes,
  relativeName,
  type RecordType,
} from "@/lib/zoneRdataHints";

type RecordRow = Schemas["Record"];
type Form = { name: string; type: RecordType; ttl: string; data: string };
type Field = "name" | "ttl" | "data";

const isRecordType = (t: string): t is RecordType =>
  (recordTypes as readonly string[]).includes(t);

// Validation codes the server returns per field (mgmt/internal/zone/validate.go); the rest belong to Data.
const fieldOfCode: Record<string, Field> = {
  invalid_name: "name",
  out_of_zone: "name",
  invalid_ttl: "ttl",
};

function toForm(r: RecordRow | undefined, zoneName: string, ttl: number): Form {
  return {
    name: r ? relativeName(r.name, zoneName) : "",
    type: r && isRecordType(r.type) ? r.type : "A",
    ttl: String(r?.ttl ?? ttl),
    data: r?.data ?? "",
  };
}

function checkTtl(v: string): string | null {
  return /^\d+$/.test(v.trim()) && Number(v) <= 2147483647
    ? null
    : "Enter a TTL in seconds (0–2147483647).";
}

/** The add/edit record dialog; an edit refused for a stale revision offers the server's current value. */
export function ZoneRecordEditor({
  zoneId,
  zoneName,
  record,
  onClose,
}: {
  zoneId: string;
  zoneName: string;
  record?: RecordRow;
  onClose(): void;
}) {
  const id = useId();
  const zone = useZone(zoneId);
  const defaultTtl = zone.data?.default_ttl ?? 3600;
  const [base, setBase] = useState(record);
  const [form, setForm] = useState<Form>(() =>
    toForm(record, zoneName, defaultTtl),
  );
  const [touched, setTouched] = useState(false);
  const [conflict, setConflict] = useState<RecordRow[] | null>(null);
  const save = useSaveRecord(zoneId);
  const current = useMutation({
    mutationFn: () => fetchRecordSet(zoneId, base!.name, base!.type),
    onSuccess: setConflict,
  });

  const set = <K extends keyof Form>(key: K, value: Form[K]) => {
    setForm((f) => ({ ...f, [key]: value }));
    save.reset();
  };

  const isRevisionConflict = (err: ApiError) =>
    base !== undefined && err.status === 409 && err.code === "conflict";
  const local: Record<Field, string | null> = {
    name: null,
    ttl: checkTtl(form.ttl),
    data: checkRdata(form.type, form.data),
  };
  const serverError = save.error;
  const serverField =
    serverError?.status === 422
      ? (fieldOfCode[serverError.code] ?? "data")
      : null;
  const message = (f: Field) =>
    (touched ? local[f] : null) ??
    (serverField === f ? serverError?.message : null);
  const general =
    serverError && serverField === null && !isRevisionConflict(serverError)
      ? serverError
      : null;
  const latest = conflict?.find((r) => r.id === base?.id);
  const hint = rdataHints[form.type];

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (local.ttl || local.data) return;
    save.mutate(
      {
        id: base?.id,
        revision: base?.revision,
        input: {
          name: absoluteName(form.name, zoneName),
          type: form.type,
          ttl: Number(form.ttl),
          data: form.data.trim(),
        },
      },
      {
        onSuccess: onClose,
        onError: (err) => {
          if (isRevisionConflict(err)) current.mutate();
        },
      },
    );
  }

  function reload() {
    if (latest) {
      setBase(latest);
      setForm(toForm(latest, zoneName, defaultTtl));
    } else {
      // Deleted meanwhile: saving creates the record again.
      setBase(undefined);
    }
    setConflict(null);
    save.reset();
  }

  const described = (f: Field, help?: string) => {
    const m = message(f);
    return {
      "aria-invalid": m ? true : undefined,
      "aria-describedby": m ? `${id}-${f}-error` : help,
    };
  };
  const fieldError = (f: Field) => {
    const m = message(f);
    return m ? (
      <p id={`${id}-${f}-error`} className="text-destructive text-xs">
        {m}
      </p>
    ) : null;
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Record</DialogTitle>
          <DialogDescription>
            {base ? "Edit" : "Add"} a record in{" "}
            <span className="font-mono">{zoneName}</span>. Saving bumps the zone
            serial and notifies secondaries.
          </DialogDescription>
        </DialogHeader>
        {conflict ? (
          <div
            role="alertdialog"
            aria-labelledby={`${id}-conflict-title`}
            aria-describedby={`${id}-conflict-body`}
            className="border-warning/50 grid gap-3 rounded-md border p-4"
          >
            <div
              id={`${id}-conflict-title`}
              className="flex items-center gap-2 font-medium"
            >
              <TriangleAlert className="text-warning h-4 w-4" />
              This record was changed by someone else
            </div>
            <div id={`${id}-conflict-body`} className="grid gap-2 text-sm">
              {latest ? (
                <>
                  <span className="text-muted-foreground">
                    The current value on the server:
                  </span>
                  <code className="bg-muted rounded px-2 py-1.5 font-mono text-xs break-all">
                    {relativeName(latest.name, zoneName)} {latest.ttl}{" "}
                    {latest.type} {latest.data}
                  </code>
                  <span className="text-muted-foreground">
                    Reload to edit the current value; your changes are
                    discarded.
                  </span>
                </>
              ) : (
                <span className="text-muted-foreground">
                  It was deleted. Reload to keep your values and save them as a
                  new record.
                </span>
              )}
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button type="button" onClick={reload}>
                Reload
              </Button>
            </div>
          </div>
        ) : (
          <form onSubmit={submit} className="grid gap-4" noValidate>
            <div className="grid grid-cols-[1fr_8rem_7rem] gap-3">
              <div className="grid content-start gap-1.5">
                <Label htmlFor={`${id}-name`}>Name</Label>
                <div className="flex items-center">
                  <Input
                    id={`${id}-name`}
                    className="rounded-r-none font-mono"
                    placeholder="@"
                    maxLength={255}
                    value={form.name}
                    onChange={(e) => set("name", e.target.value)}
                    {...described("name", `${id}-name-help`)}
                  />
                  <span
                    className="bg-muted text-muted-foreground flex h-9 max-w-40 items-center truncate rounded-r-md border border-l-0 px-2 font-mono text-xs"
                    title={zoneName}
                  >
                    .{zoneName}
                  </span>
                </div>
                {fieldError("name") ?? (
                  <p
                    id={`${id}-name-help`}
                    className="text-muted-foreground text-xs"
                  >
                    @ for the apex; end with a dot for an absolute name.
                  </p>
                )}
              </div>
              <div className="grid content-start gap-1.5">
                <Label htmlFor={`${id}-type`}>Type</Label>
                <Select
                  value={form.type}
                  onValueChange={(v) => set("type", v as RecordType)}
                >
                  <SelectTrigger id={`${id}-type`} className="h-9">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {recordTypes.map((t) => (
                      <SelectItem key={t} value={t}>
                        {t}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid content-start gap-1.5">
                <Label htmlFor={`${id}-ttl`}>TTL</Label>
                <Input
                  id={`${id}-ttl`}
                  inputMode="numeric"
                  value={form.ttl}
                  onChange={(e) => set("ttl", e.target.value)}
                  {...described("ttl")}
                />
                {fieldError("ttl")}
              </div>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor={`${id}-data`}>Data</Label>
              <Input
                id={`${id}-data`}
                className="font-mono"
                placeholder={hint.placeholder}
                value={form.data}
                onChange={(e) => set("data", e.target.value)}
                onBlur={() => form.data !== "" && setTouched(true)}
                {...described("data", `${id}-data-help`)}
              />
              {fieldError("data") ?? (
                <p
                  id={`${id}-data-help`}
                  className="text-muted-foreground text-xs"
                >
                  {hint.help}
                </p>
              )}
            </div>
            {general && <ErrorAlert error={general} thing="This record" />}
            {current.error && (
              <ErrorAlert
                error={current.error}
                prefix={errorMessage(serverError, "This record")}
              />
            )}
            <DialogFooter className="gap-2 pt-2">
              <Button type="button" variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={save.isPending || current.isPending}
              >
                {save.isPending ? "Saving…" : "Save"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
