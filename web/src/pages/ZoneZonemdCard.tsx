import { useState } from "react";
import type { Schemas } from "@/api/client";
import { useCatalogZones } from "@/api/m8";
import { useUpdateZone } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert, SavedNote } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { ZonemdStatus, ZonemdVerifySelect } from "@/components/ZonemdControls";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

export function ZoneZonemdCard({ zone }: { zone: Schemas["Zone"] }) {
  const allowed = useCan("updateZone");
  const catalogs = useCatalogZones();
  const save = useUpdateZone(zone.id);
  const manager = catalogs.data?.find(
    (c) => c.id === zone.catalog_zone_id && c.role === "consumer",
  );
  const managed = !!manager;
  const isCatalog = catalogs.data?.some((c) => c.zone_id === zone.id) ?? false;
  const canEdit = allowed && catalogs.isSuccess && !managed;
  // Capture the revision with the draft; unrelated cache refreshes must not bless stale edits.
  const [draft, setDraft] = useState<Schemas["ZoneUpdate"] | null>(null);
  const values = draft ?? zone;
  const change = (fields: Partial<Schemas["ZoneUpdate"]>) => {
    save.reset();
    setDraft({
      revision: zone.revision,
      zonemd_generate: zone.zonemd_generate,
      zonemd_verify: zone.zonemd_verify,
      catalog_zone_id: zone.catalog_zone_id,
      ...draft,
      ...fields,
    });
  };
  const producers =
    catalogs.data?.filter(
      (c) => c.role === "producer" && c.zone_id !== zone.id,
    ) ?? [];
  return (
    <Card className="min-w-0 mb-6 p-5">
      <section aria-label="ZONEMD and catalog" className="grid gap-4">
        <h2 className="text-sm font-semibold">ZONEMD and catalog</h2>
        {managed && (
          <p className="text-muted-foreground text-sm">
            Managed by catalog {manager?.name}. Change its source catalog to
            update it.
          </p>
        )}
        {isCatalog && (
          <p className="text-muted-foreground text-sm">
            Catalog zones cannot be members of another catalog.
          </p>
        )}
        {zone.kind === "primary" ? (
          <div className="flex items-center gap-2">
            <Switch
              id="zonemd-generate"
              data-testid="zonemd-generate"
              checked={values.zonemd_generate}
              disabled={!canEdit || save.isPending}
              onCheckedChange={(v) => change({ zonemd_generate: v })}
            />
            <Label htmlFor="zonemd-generate">Generate ZONEMD</Label>
            <HelpTip id="zonemd-generate" label="Generate ZONEMD" />
          </div>
        ) : (
          <>
            <ZonemdVerifySelect
              value={values.zonemd_verify ?? "if_present"}
              disabled={!canEdit || save.isPending}
              onChange={(v) => change({ zonemd_verify: v })}
            />
            <ZonemdStatus
              status={zone.zonemd_status}
              error={zone.zonemd_error}
            />
          </>
        )}
        {!managed && !isCatalog && (
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="zone-catalog-select">Producer catalog</Label>
              <HelpTip id="zone-catalog-select" label="Producer catalog" />
            </div>
            <Select
              value={values.catalog_zone_id ?? "none"}
              disabled={
                !canEdit || isCatalog || save.isPending || !catalogs.isSuccess
              }
              onValueChange={(v) =>
                change({ catalog_zone_id: v === "none" ? null : v })
              }
            >
              <SelectTrigger
                id="zone-catalog-select"
                data-testid="zone-catalog-select"
              >
                <SelectValue placeholder="Loading catalogs…" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">No catalog</SelectItem>
                {values.catalog_zone_id &&
                  !producers.some((c) => c.id === values.catalog_zone_id) && (
                    <SelectItem value={values.catalog_zone_id} disabled>
                      Current catalog (unavailable)
                    </SelectItem>
                  )}
                {producers.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        <ErrorAlert error={catalogs.error} prefix="Could not load catalogs" />
        <ErrorAlert error={save.error} thing="This zone" />
        {canEdit && (
          <Button
            className="justify-self-start"
            disabled={!draft || save.isPending || !catalogs.isSuccess}
            onClick={() =>
              draft &&
              save.mutate(
                zone.kind === "primary"
                  ? {
                      revision: draft.revision,
                      zonemd_generate: draft.zonemd_generate,
                      ...(isCatalog
                        ? {}
                        : { catalog_zone_id: draft.catalog_zone_id }),
                    }
                  : {
                      revision: draft.revision,
                      zonemd_verify: draft.zonemd_verify,
                      ...(isCatalog
                        ? {}
                        : { catalog_zone_id: draft.catalog_zone_id }),
                    },
                { onSuccess: () => setDraft(null) },
              )
            }
          >
            {save.isPending ? "Saving…" : "Save ZONEMD and catalog"}
          </Button>
        )}
        {draft && (
          <Button
            variant="outline"
            className="justify-self-start"
            disabled={save.isPending}
            onClick={() => window.location.reload()}
          >
            Discard changes and reload
          </Button>
        )}
        <SavedNote show={save.isSuccess}>ZONEMD and catalog saved</SavedNote>
      </section>
    </Card>
  );
}
