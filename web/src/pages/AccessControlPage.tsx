import type { ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";

import { api, unwrap, type Schemas } from "@/api/client";
import { useTsigKeys, useZones } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert, MessageRow } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { PageHeader } from "@/components/layout/AppShell";
import { ListEditor } from "@/components/ListEditor";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const ipv4Octet = "(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)";
const ipv4Prefix = new RegExp(
  `^${ipv4Octet}(\\.${ipv4Octet}){3}/(3[0-2]|[12]?\\d)$`,
);
const ipv6Prefix =
  /^(?=.*:)[0-9a-f:]+(:\d{1,3}(\.\d{1,3}){3})?\/(12[0-8]|1[01]\d|[1-9]?\d)$/;

/** Reports whether v looks like an IPv4 or IPv6 prefix (the server does the exact parse). */
function isCIDR(v: string): boolean {
  return (
    ipv4Prefix.test(v) || (ipv6Prefix.test(v) && v.split("::").length <= 2)
  );
}

type AccessControl = Schemas["AccessControl"];

const cidrEditor = {
  inputLabel: "Network (CIDR)",
  placeholder: "192.0.2.0/24 or 2001:db8::/32",
  addLabel: "Add network",
  columnLabel: "Allowed network",
  extraColumn: {
    label: "Family",
    render: (c: string) => (c.includes(":") ? "IPv6" : "IPv4"),
  },
  normalize: (v: string) => v.trim().toLowerCase(),
  validate: (v: string) => (isCIDR(v) ? null : "Invalid CIDR"),
};

export function AccessControlPage() {
  const canUpdate = useCan("updateAccessControl");
  const qc = useQueryClient();
  const acl = useQuery({
    queryKey: ["access-control"],
    queryFn: async () => unwrap(await api.GET("/access-control")),
  });

  // Each section sends both lists with the latest revision, so saving one never resets the other.
  async function save(patch: Partial<AccessControl>) {
    const cur = acl.data!;
    const saved = unwrap(
      await api.PUT("/access-control", {
        body: {
          allow_cidrs: cur.allow_cidrs,
          authoritative_allow_cidrs: cur.authoritative_allow_cidrs,
          revision: cur.revision,
          ...patch,
        },
      }),
    );
    qc.setQueryData(["access-control"], saved);
  }

  return (
    <>
      <PageHeader
        title="Access control"
        description="Who may use this resolver, and who may query the zones Nexora hosts. Addresses outside a list are refused."
      />
      <ErrorAlert
        error={acl.error}
        prefix="Could not load access control"
        className="mb-4"
      />
      <div className="grid max-w-3xl gap-8">
        <Section
          title="Recursion and resolver access"
          text="Clients that may get answers from the cache, forwarding, recursion, rewrites and filtering."
        >
          <Card className="overflow-hidden">
            <ListEditor
              prefix="acl"
              inputTestId="acl-cidr-input"
              help="acl-cidr-input"
              items={acl.data?.allow_cidrs}
              loading={acl.isPending}
              canEdit={canUpdate}
              {...cidrEditor}
              emptyText="No networks are allowed, so every recursive query is refused."
              savedText="Access control saved"
              thing="Access control"
              onSave={(allow_cidrs) => save({ allow_cidrs })}
            />
          </Card>
        </Section>
        <Section
          title="Authoritative query access"
          text="Clients that may query hosted zones. A zone can narrow or widen this under Zones, Transfers. 0.0.0.0/0 and ::/0 allow everyone."
        >
          <Card className="overflow-hidden">
            <ListEditor
              prefix="authacl"
              help="authacl-input"
              items={
                acl.data
                  ? (acl.data.authoritative_allow_cidrs ?? [])
                  : undefined
              }
              loading={acl.isPending}
              canEdit={canUpdate}
              {...cidrEditor}
              emptyText="No networks are allowed, so hosted zones refuse every query unless a zone sets its own list."
              savedText="Authoritative access saved"
              thing="Authoritative access"
              onSave={(authoritative_allow_cidrs) =>
                save({ authoritative_allow_cidrs })
              }
            />
          </Card>
        </Section>
        {!canUpdate && acl.isSuccess && (
          <p className="text-muted-foreground -mt-4 text-sm">
            Operators and administrators can change access control.
          </p>
        )}
        <Section
          title="Zone transfers and updates"
          text="Per-zone query, transfer and update access. Change it on each zone's Transfers tab."
        >
          <ZoneAccessSummary />
        </Section>
      </div>
    </>
  );
}

function Section({
  title,
  text,
  children,
}: {
  title: string;
  text: string;
  children: ReactNode;
}) {
  return (
    <section aria-label={title}>
      <h2 className="text-base font-semibold">{title}</h2>
      <p className="text-muted-foreground mt-0.5 mb-3 max-w-prose text-sm">
        {text}
      </p>
      {children}
    </section>
  );
}

function ZoneAccessSummary() {
  const zones = useZones();
  const keys = useTsigKeys();
  const keyName = (id: string) =>
    keys.data?.find((k) => k.id === id)?.name ?? id;
  const rows = zones.data ?? [];

  return (
    <>
      <ErrorAlert
        error={zones.error}
        prefix="Could not load zones"
        className="mb-3"
      />
      <Card className="overflow-x-auto">
        <Table data-testid="zone-access-summary">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10">Zone</TableHead>
              <TableHead className="h-10">
                <span className="inline-flex items-center gap-1.5">
                  Queries
                  <HelpTip id="access-col-queries" label="Queries column" />
                </span>
              </TableHead>
              <TableHead className="h-10">
                <span className="inline-flex items-center gap-1.5">
                  Transfers
                  <HelpTip id="access-col-transfers" label="Transfers column" />
                </span>
              </TableHead>
              <TableHead className="h-10">
                <span className="inline-flex items-center gap-1.5">
                  Updates
                  <HelpTip id="access-col-updates" label="Updates column" />
                </span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((z) => (
              <TableRow key={z.id} data-testid={`zone-access-row-${z.name}`}>
                <TableCell className="py-2 font-mono text-[13px]">
                  <Link
                    to={`/zones/${z.id}`}
                    className="text-primary font-medium hover:underline"
                  >
                    {z.name}
                  </Link>
                </TableCell>
                <TableCell className="py-2 text-sm">
                  {z.allow_query_cidrs.length === 0 ? (
                    <span className="text-muted-foreground">
                      Inherits authoritative access
                    </span>
                  ) : (
                    <span className="font-mono text-[13px]">
                      {z.allow_query_cidrs.join(", ")}
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-2 text-sm">
                  {z.transfer.allow_cidrs.length === 0 ? (
                    <span className="text-muted-foreground">Refused</span>
                  ) : (
                    <span className="font-mono text-[13px]">
                      {z.transfer.allow_cidrs.join(", ")}
                      {z.transfer.tsig_key_id &&
                        `, key ${keyName(z.transfer.tsig_key_id)}`}
                    </span>
                  )}
                </TableCell>
                <TableCell className="py-2 text-sm">
                  <UpdateAccess zone={z} keyName={keyName} />
                </TableCell>
              </TableRow>
            ))}
            {zones.isPending && <MessageRow colSpan={4}>Loading…</MessageRow>}
            {zones.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={4}>No zones are hosted.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
    </>
  );
}

/** Updates need a TSIG key; source networks narrow who may send them. */
function UpdateAccess({
  zone,
  keyName,
}: {
  zone: Schemas["Zone"];
  keyName: (id: string) => string;
}) {
  const sources = zone.update.allow_cidrs ?? [];
  const refused =
    zone.kind === "secondary" || zone.update.tsig_key_ids.length === 0;
  return (
    <>
      {refused ? (
        <span className="text-muted-foreground">Refused</span>
      ) : (
        <span className="font-mono text-[13px]">
          {zone.update.tsig_key_ids.map(keyName).join(", ")}
        </span>
      )}
      {zone.kind === "primary" && sources.length > 0 && (
        <span className="text-muted-foreground block text-xs">
          from <span className="font-mono">{sources.join(", ")}</span>
        </span>
      )}
    </>
  );
}
