import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { Download, Upload } from "lucide-react";

import { type Schemas } from "@/api/client";
import { downloadZoneFile, useImportZoneFile } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert, SavedNote } from "@/components/common";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

type Zone = Schemas["Zone"];

/** Line problems beyond this many are summarised; the first ones are what an operator fixes first. */
const shownLineErrors = 50;

export function ZoneImportExportTab({ zone }: { zone: Zone }) {
  const canImport = useCan("importZoneFile") && zone.kind === "primary";
  const [content, setContent] = useState("");
  const importFile = useImportZoneFile(zone.id);
  const exportFile = useMutation({ mutationFn: () => downloadZoneFile(zone) });
  const details = importFile.error?.details ?? [];

  function submit(e: FormEvent) {
    e.preventDefault();
    importFile.mutate({ revision: zone.revision, content });
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_20rem]">
      {canImport ? (
        <form onSubmit={submit}>
          <Card className="grid gap-4 px-5 py-5">
            <div>
              <h2 className="text-sm font-semibold">Import</h2>
              <p className="text-muted-foreground mt-0.5 max-w-prose text-sm">
                A BIND master file replaces every record and the SOA fields of{" "}
                <span className="font-mono">{zone.name}</span> in one change.
                $INCLUDE and $GENERATE are refused.
              </p>
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-end justify-between gap-3">
                <Label htmlFor="zone-import-content">Zone file</Label>
                <Label
                  htmlFor="zone-import-picker"
                  className="text-primary cursor-pointer text-xs font-normal hover:underline"
                >
                  Open a file…
                </Label>
                <Input
                  id="zone-import-picker"
                  type="file"
                  accept=".zone,.db,.txt,text/plain"
                  className="sr-only"
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    if (file) void file.text().then(setContent);
                    importFile.reset();
                  }}
                />
              </div>
              <Textarea
                id="zone-import-content"
                className="min-h-72 font-mono text-xs"
                spellCheck={false}
                placeholder={`$ORIGIN ${zone.name}\n$TTL 3600\n@ SOA ns1 hostmaster 1 7200 3600 1209600 300\n@ NS ns1\nns1 A 192.0.2.1`}
                value={content}
                onChange={(e) => {
                  setContent(e.target.value);
                  importFile.reset();
                }}
              />
            </div>
            {details.length > 0 ? (
              <Alert variant="destructive">
                <AlertDescription>
                  <p className="mb-1 font-medium">
                    {importFile.error?.message}
                  </p>
                  <ul className="grid gap-0.5 font-mono text-xs">
                    {details.slice(0, shownLineErrors).map((d, i) => (
                      <li key={i}>
                        line {d.line}: {d.message}
                      </li>
                    ))}
                  </ul>
                  {details.length > shownLineErrors && (
                    <p className="mt-1 text-xs">
                      and {details.length - shownLineErrors} more problems
                    </p>
                  )}
                </AlertDescription>
              </Alert>
            ) : (
              <ErrorAlert error={importFile.error} thing="The zone" />
            )}
            <div className="flex items-center justify-end gap-3">
              <SavedNote show={importFile.isSuccess}>
                Imported {importFile.data?.records_imported} records
              </SavedNote>
              <Button
                type="submit"
                disabled={content.trim() === "" || importFile.isPending}
              >
                <Upload className="mr-1.5 h-4 w-4" />
                {importFile.isPending ? "Importing…" : "Import"}
              </Button>
            </div>
          </Card>
        </form>
      ) : (
        <Card className="text-muted-foreground px-5 py-5 text-sm">
          {zone.kind === "secondary"
            ? "Secondary zones take their records from their primaries."
            : "Operators and administrators can import zone files."}
        </Card>
      )}
      <Card className="grid content-start gap-3 px-5 py-5">
        <div>
          <h2 className="text-sm font-semibold">Export</h2>
          <p className="text-muted-foreground mt-0.5 text-sm">
            Download the zone as a BIND master file with its SOA and every
            record.
          </p>
        </div>
        <ErrorAlert error={exportFile.error} />
        <div>
          <Button
            variant="outline"
            disabled={exportFile.isPending}
            onClick={() => exportFile.mutate()}
          >
            <Download className="mr-1.5 h-4 w-4" />
            Export
          </Button>
        </div>
      </Card>
    </div>
  );
}
