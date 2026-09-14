import { useState, type FormEvent, type ReactNode } from "react";
import { Plus, RefreshCw, X } from "lucide-react";

import { ApiError, type Schemas } from "@/api/client";
import {
  useRefreshZone,
  useTsigKeys,
  useUpdateZone,
  useZone,
} from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import {
  ErrorAlert,
  Fact,
  formatAgo,
  formatDateTime,
  SavedNote,
} from "@/components/common";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { splitList } from "@/lib/zoneRdataHints";
import { SecondaryState } from "@/pages/ZonesPage";

type Zone = Schemas["Zone"];
type TsigKey = Schemas["TsigKey"];
type Endpoint = { address: string; key: string };

type Form = {
  allowQuery: string;
  allow: string;
  transferKey: string;
  notify: Endpoint[];
  primaries: Endpoint[];
  updateKeys: string[];
  updateAllow: string;
};

const none = "none";

const endpointsIn = (eps: Schemas["ZoneEndpoint"][]): Endpoint[] =>
  eps.map((e) => ({ address: e.address, key: e.tsig_key_id ?? none }));

const endpointsOut = (eps: Endpoint[]): Schemas["ZoneEndpoint"][] =>
  eps
    .filter((e) => e.address.trim() !== "")
    .map((e) => ({
      address: e.address.trim(),
      tsig_key_id: e.key === none ? null : e.key,
    }));

function toForm(z: Zone): Form {
  return {
    allowQuery: z.allow_query_cidrs.join(", "),
    allow: z.transfer.allow_cidrs.join(", "),
    transferKey: z.transfer.tsig_key_id ?? none,
    notify: endpointsIn(z.notify),
    primaries: endpointsIn(z.primaries),
    updateKeys: [...z.update.tsig_key_ids].sort(),
    updateAllow: (z.update.allow_cidrs ?? []).join(", "),
  };
}

export function ZoneTransfersTab({ zone }: { zone: Zone }) {
  const canUpdate = useCan("updateZone");
  const keys = useTsigKeys();
  const refetchZone = useZone(zone.id).refetch;
  const [form, setForm] = useState(() => toForm(zone));
  const save = useUpdateZone(zone.id);
  const secondary = zone.kind === "secondary";
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(zone));
  const stale = save.error instanceof ApiError && save.error.status === 409;
  const keyList = keys.data ?? [];

  const set = <K extends keyof Form>(key: K, value: Form[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(
      {
        revision: zone.revision,
        allow_query_cidrs: splitList(form.allowQuery),
        transfer: {
          allow_cidrs: splitList(form.allow),
          tsig_key_id: form.transferKey === none ? null : form.transferKey,
        },
        notify: endpointsOut(form.notify),
        ...(secondary
          ? { primaries: endpointsOut(form.primaries) }
          : {
              update: {
                tsig_key_ids: form.updateKeys,
                allow_cidrs: splitList(form.updateAllow),
              },
            }),
      },
      { onSuccess: (saved) => setForm(toForm(saved)) },
    );
  }

  async function reload() {
    const fresh = await refetchZone();
    if (fresh.data) setForm(toForm(fresh.data));
    save.reset();
  }

  return (
    <div className="grid gap-6">
      {secondary && <SecondaryStatus zone={zone} />}
      <form onSubmit={submit}>
        <Card className="overflow-hidden">
          <fieldset
            disabled={!canUpdate || save.isPending}
            className="grid gap-6 px-5 py-5"
          >
            {secondary && (
              <Section
                title="Primaries"
                help="Servers this zone is transferred from, as ip:port, with the TSIG key the transfer is signed with."
              >
                <EndpointsEditor
                  label="Primary"
                  placeholder="192.0.2.53:53"
                  items={form.primaries}
                  keys={keyList}
                  onChange={(v) => set("primaries", v)}
                />
              </Section>
            )}
            <Section
              title="Query access"
              help="Who may query this zone. A list here replaces the global authoritative query access for this zone."
            >
              <div className="grid content-start gap-1.5">
                <Label htmlFor="zone-allow-query">Allowed query networks</Label>
                <Input
                  id="zone-allow-query"
                  data-testid="zone-allow-query"
                  className="font-mono"
                  placeholder="192.0.2.0/24, 2001:db8::/32"
                  value={form.allowQuery}
                  onChange={(e) => set("allowQuery", e.target.value)}
                />
                <p className="text-muted-foreground text-xs">
                  Empty uses the global authoritative query access.
                </p>
              </div>
            </Section>
            <Section
              title="Outgoing transfers"
              help="Who may transfer this zone (AXFR/IXFR). An empty list refuses every transfer; a TSIG key additionally requires signed requests."
            >
              <div className="grid gap-4 md:grid-cols-[1fr_14rem]">
                <div className="grid content-start gap-1.5">
                  <Label htmlFor="zone-transfer-allow">
                    Allowed transfer networks
                  </Label>
                  <Input
                    id="zone-transfer-allow"
                    className="font-mono"
                    placeholder="192.0.2.0/24, 2001:db8::/32"
                    value={form.allow}
                    onChange={(e) => set("allow", e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    Addresses or CIDR prefixes, separated by commas.
                  </p>
                </div>
                <div className="grid content-start gap-1.5">
                  <Label htmlFor="zone-transfer-key">Transfer TSIG key</Label>
                  <KeySelect
                    id="zone-transfer-key"
                    value={form.transferKey}
                    keys={keyList}
                    onChange={(v) => set("transferKey", v)}
                  />
                </div>
              </div>
            </Section>
            <Section
              title="Notify targets"
              help="Secondaries sent a NOTIFY whenever the zone changes, as ip:port."
            >
              <EndpointsEditor
                label="Notify target"
                placeholder="192.0.2.54:53"
                items={form.notify}
                keys={keyList}
                onChange={(v) => set("notify", v)}
              />
            </Section>
            {!secondary && (
              <Section
                title="Dynamic updates"
                help="TSIG keys allowed to change records with RFC 2136 updates. None refuses updates."
              >
                <fieldset aria-label="Update TSIG keys" className="grid gap-2">
                  {keyList.length === 0 && (
                    <p className="text-muted-foreground text-sm">
                      No TSIG keys exist yet.
                    </p>
                  )}
                  {keyList.map((k) => (
                    <label
                      key={k.id}
                      className="flex items-center gap-2 font-mono text-[13px]"
                    >
                      <input
                        type="checkbox"
                        className="accent-primary h-4 w-4"
                        checked={form.updateKeys.includes(k.id)}
                        onChange={(e) =>
                          set(
                            "updateKeys",
                            e.target.checked
                              ? [...form.updateKeys, k.id].sort()
                              : form.updateKeys.filter((id) => id !== k.id),
                          )
                        }
                      />
                      {k.name}
                      <span className="text-muted-foreground font-sans text-xs">
                        {k.algorithm}
                      </span>
                    </label>
                  ))}
                </fieldset>
                <div className="mt-4 grid content-start gap-1.5">
                  <Label htmlFor="zone-update-allow">
                    Allowed update sources
                  </Label>
                  <Input
                    id="zone-update-allow"
                    data-testid="zone-update-allow"
                    className="font-mono"
                    placeholder="192.0.2.0/24, 2001:db8::/32"
                    value={form.updateAllow}
                    onChange={(e) => set("updateAllow", e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    Empty allows any source; a TSIG key is always required.
                  </p>
                </div>
              </Section>
            )}
          </fieldset>
          <div className="bg-muted/40 flex flex-wrap items-center justify-end gap-3 border-t px-5 py-3">
            {stale ? (
              <Alert variant="destructive" className="mr-auto flex-1 py-2">
                <AlertDescription className="flex flex-wrap items-center gap-3">
                  The zone was changed by someone else since this form loaded.
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => void reload()}
                  >
                    Reload
                  </Button>
                </AlertDescription>
              </Alert>
            ) : save.error ? (
              <ErrorAlert
                error={save.error}
                className="mr-auto w-auto flex-1 py-2"
              />
            ) : (
              <span className="text-muted-foreground mr-auto text-sm">
                {!canUpdate ? (
                  "Operators and administrators can change these settings."
                ) : dirty ? (
                  "Unsaved changes"
                ) : (
                  <SavedNote show={save.isSuccess}>Settings saved</SavedNote>
                )}
              </span>
            )}
            {canUpdate && (
              <Button type="submit" disabled={!dirty || save.isPending}>
                {save.isPending ? "Saving…" : "Save settings"}
              </Button>
            )}
          </div>
        </Card>
      </form>
    </div>
  );
}

function Section({
  title,
  help,
  children,
}: {
  title: string;
  help: string;
  children: ReactNode;
}) {
  return (
    <section aria-label={title}>
      <h3 className="text-sm font-semibold">{title}</h3>
      <p className="text-muted-foreground mt-0.5 mb-3 max-w-prose text-sm">
        {help}
      </p>
      {children}
    </section>
  );
}

function KeySelect({
  id,
  value,
  keys,
  label,
  onChange,
}: {
  id?: string;
  value: string;
  keys: TsigKey[];
  label?: string;
  onChange: (v: string) => void;
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger id={id} aria-label={label} className="h-9">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={none}>No TSIG key</SelectItem>
        {keys.map((k) => (
          <SelectItem key={k.id} value={k.id}>
            {k.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function EndpointsEditor({
  label,
  placeholder,
  items,
  keys,
  onChange,
}: {
  label: string;
  placeholder: string;
  items: Endpoint[];
  keys: TsigKey[];
  onChange: (v: Endpoint[]) => void;
}) {
  const update = (i: number, patch: Partial<Endpoint>) =>
    onChange(items.map((e, j) => (j === i ? { ...e, ...patch } : e)));
  return (
    <div className="grid gap-2">
      {items.map((e, i) => (
        <div key={i} className="grid grid-cols-[1fr_14rem_auto] gap-2">
          <Input
            aria-label={`${label} ${i + 1} address`}
            className="font-mono"
            placeholder={placeholder}
            value={e.address}
            onChange={(ev) => update(i, { address: ev.target.value })}
          />
          <KeySelect
            label={`${label} ${i + 1} TSIG key`}
            value={e.key}
            keys={keys}
            onChange={(key) => update(i, { key })}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-9 w-9"
            aria-label={`Remove ${label.toLowerCase()} ${i + 1}`}
            onClick={() => onChange(items.filter((_, j) => j !== i))}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onChange([...items, { address: "", key: none }])}
        >
          <Plus className="mr-1.5 h-4 w-4" />
          Add {label.toLowerCase()}
        </Button>
      </div>
    </div>
  );
}

function SecondaryStatus({ zone }: { zone: Zone }) {
  const canRefresh = useCan("refreshZone");
  const refresh = useRefreshZone(zone.id);
  const s = zone.secondary_status;
  return (
    <Card className="px-5 py-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <h3 className="text-sm font-semibold">Refresh status</h3>
          <SecondaryState zone={zone} />
        </div>
        <div className="flex items-center gap-3">
          <SavedNote show={refresh.isSuccess}>Refresh requested</SavedNote>
          {canRefresh && (
            <Button
              type="button"
              variant="outline"
              disabled={refresh.isPending}
              onClick={() => refresh.mutate()}
            >
              <RefreshCw className="mr-1.5 h-4 w-4" />
              Refresh now
            </Button>
          )}
        </div>
      </div>
      <ErrorAlert error={refresh.error} className="mb-3" />
      {s && (
        <dl className="grid gap-4 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <Fact label="Last refresh">
            {formatAgo(s.last_refresh_at)}
            {s.last_trigger && (
              <span className="text-muted-foreground"> ({s.last_trigger})</span>
            )}
          </Fact>
          <Fact label="Last success">{formatDateTime(s.last_success_at)}</Fact>
          <Fact label="Next refresh">{formatDateTime(s.next_refresh_at)}</Fact>
          <Fact label="Expires">{formatDateTime(s.expires_at)}</Fact>
          {s.last_error && (
            <Fact label="Last error" className="sm:col-span-2 lg:col-span-4">
              <span className="text-destructive break-all">{s.last_error}</span>
            </Fact>
          )}
        </dl>
      )}
    </Card>
  );
}
