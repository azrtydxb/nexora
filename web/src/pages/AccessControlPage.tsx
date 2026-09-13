import { useQuery, useQueryClient } from "@tanstack/react-query";

import { api, unwrap } from "@/api/client";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert } from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
import { ListEditor } from "@/components/ListEditor";
import { Card } from "@/components/ui/card";

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

export function AccessControlPage() {
  const canUpdate = useCan("updateAccessControl");
  const qc = useQueryClient();
  const acl = useQuery({
    queryKey: ["access-control"],
    queryFn: async () => unwrap(await api.GET("/access-control")),
  });

  return (
    <>
      <PageHeader
        title="Access control"
        description="Client networks allowed to query the engines. Queries from any other address are refused."
      />
      <ErrorAlert
        error={acl.error}
        prefix="Could not load access control"
        className="mb-4"
      />
      <Card className="max-w-3xl overflow-hidden">
        <ListEditor
          prefix="acl"
          inputTestId="acl-cidr-input"
          items={acl.data?.allow_cidrs}
          loading={acl.isPending}
          canEdit={canUpdate}
          inputLabel="Network (CIDR)"
          placeholder="192.0.2.0/24 or 2001:db8::/32"
          addLabel="Add network"
          columnLabel="Allowed network"
          extraColumn={{
            label: "Family",
            render: (c) => (c.includes(":") ? "IPv6" : "IPv4"),
          }}
          emptyText="No networks are allowed, so every query is refused."
          savedText="Access control saved"
          thing="Access control"
          normalize={(v) => v.trim().toLowerCase()}
          validate={(v) => (isCIDR(v) ? null : "Invalid CIDR")}
          onSave={async (allow_cidrs) => {
            const saved = unwrap(
              await api.PUT("/access-control", {
                body: { allow_cidrs, revision: acl.data!.revision },
              }),
            );
            qc.setQueryData(["access-control"], saved);
          }}
        />
      </Card>
      {!canUpdate && acl.isSuccess && (
        <p className="text-muted-foreground mt-3 text-sm">
          Operators and administrators can change access control.
        </p>
      )}
    </>
  );
}
