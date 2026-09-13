import { useState } from "react";
import { ChevronLeft, Trash2 } from "lucide-react";
import { Link, useNavigate, useParams } from "react-router";

import { useDeleteZone, useZone } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import { ConfirmDialog, ErrorAlert } from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ZoneDnssecTab } from "@/pages/ZoneDnssecTab";
import { ZoneImportExportTab } from "@/pages/ZoneImportExportTab";
import { ZoneRecordsTab } from "@/pages/ZoneRecordsTab";
import { ZoneTransfersTab } from "@/pages/ZoneTransfersTab";

export function ZoneDetailPage() {
  const { zoneId = "" } = useParams();
  const navigate = useNavigate();
  const zone = useZone(zoneId);
  const canDelete = useCan("deleteZone");
  const del = useDeleteZone();
  const [deleting, setDeleting] = useState(false);
  const z = zone.data;

  return (
    <>
      <Link
        to="/zones"
        className="text-muted-foreground hover:text-foreground mb-2 inline-flex items-center gap-1 text-sm"
      >
        <ChevronLeft className="h-4 w-4" />
        Zones
      </Link>
      <ErrorAlert
        error={zone.error}
        prefix="Could not load the zone"
        className="mb-4"
      />
      {zone.isPending && (
        <Card className="text-muted-foreground p-5 text-sm">Loading…</Card>
      )}
      {z && (
        <>
          <PageHeader
            title={z.name}
            actions={
              canDelete && (
                <Button
                  variant="outline"
                  className="hover:text-destructive"
                  onClick={() => setDeleting(true)}
                >
                  <Trash2 className="mr-1.5 h-4 w-4" />
                  Delete zone
                </Button>
              )
            }
          />
          <div className="text-muted-foreground -mt-4 mb-6 flex flex-wrap items-center gap-2 text-sm">
            <Badge variant="secondary">
              {z.kind === "primary" ? "Primary" : "Secondary"}
            </Badge>
            <span className="tabular-nums">serial {z.serial}</span>
            <span aria-hidden>·</span>
            <span className="tabular-nums">default TTL {z.default_ttl}</span>
            {z.dnssec_enabled && (
              <Badge variant="outline" className="border-success/40">
                signed
              </Badge>
            )}
          </div>
          <Tabs defaultValue="records">
            <TabsList>
              <TabsTrigger value="records">Records</TabsTrigger>
              <TabsTrigger value="transfers">Transfers</TabsTrigger>
              <TabsTrigger value="dnssec">DNSSEC</TabsTrigger>
              <TabsTrigger value="import-export">Import/Export</TabsTrigger>
            </TabsList>
            <TabsContent value="records" className="mt-4">
              <ZoneRecordsTab zone={z} />
            </TabsContent>
            <TabsContent value="transfers" className="mt-4">
              <ZoneTransfersTab zone={z} />
            </TabsContent>
            <TabsContent value="dnssec" className="mt-4">
              <ZoneDnssecTab zone={z} />
            </TabsContent>
            <TabsContent value="import-export" className="mt-4">
              <ZoneImportExportTab zone={z} />
            </TabsContent>
          </Tabs>
          {deleting && (
            <ConfirmDialog
              title="Delete zone"
              description={`Delete ${z.name} with all its records, keys and settings? Engines stop answering for it once they apply the new configuration.`}
              confirmLabel="Delete zone"
              pendingLabel="Deleting…"
              thing="This zone"
              onConfirm={async () => {
                await del.mutateAsync(z);
                void navigate("/zones");
              }}
              onClose={() => setDeleting(false)}
            />
          )}
        </>
      )}
    </>
  );
}
