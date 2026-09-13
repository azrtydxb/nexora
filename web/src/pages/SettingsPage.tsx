import {
  useState,
  type ChangeEvent,
  type FormEvent,
  type ReactNode,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCan } from "@/auth/AuthProvider";
import {
  ErrorAlert,
  formatDateTime,
  MessageRow,
  SavedNote,
} from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
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

type Settings = Schemas["ResolverSettings"];

type NumberKey =
  | "cache_max_bytes"
  | "cache_min_ttl"
  | "cache_max_ttl"
  | "cache_negative_max_ttl"
  | "cache_stale_window"
  | "block_ttl"
  | "trace_sample_one_in"
  | "trace_slow_threshold_us";

const numberKeys: NumberKey[] = [
  "cache_max_bytes",
  "cache_min_ttl",
  "cache_max_ttl",
  "cache_negative_max_ttl",
  "cache_stale_window",
  "block_ttl",
  "trace_sample_one_in",
  "trace_slow_threshold_us",
];

type Form = Record<NumberKey, string> & {
  strategy: Settings["strategy"];
  block_mode: Settings["block_mode"];
  otlp_endpoint: string;
};

function toForm(s: Settings): Form {
  const f = {
    strategy: s.strategy,
    block_mode: s.block_mode,
    otlp_endpoint: s.otlp_endpoint,
  } as Form;
  for (const k of numberKeys) f[k] = String(s[k]);
  return f;
}

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n % 1 === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

export function SettingsPage() {
  const settings = useQuery({
    queryKey: ["resolver-settings"],
    queryFn: async () => unwrap(await api.GET("/resolver-settings")),
  });
  const versions = useQuery({
    queryKey: ["config-versions"],
    queryFn: async () =>
      unwrap(
        await api.GET("/config-versions", { params: { query: { limit: 50 } } }),
      ),
  });
  const rows = versions.data ?? [];

  return (
    <>
      <PageHeader
        title="Settings"
        description="Resolver behaviour shared by every engine. Saving publishes a new configuration version."
      />
      <ErrorAlert
        error={settings.error}
        prefix="Could not load resolver settings"
        className="mb-4"
      />
      {settings.isPending && (
        <Card className="text-muted-foreground mb-8 p-6 text-sm">Loading…</Card>
      )}
      {settings.data && <SettingsForm settings={settings.data} />}

      <section aria-labelledby="versions-heading" className="mt-10">
        <h2 id="versions-heading" className="mb-1 text-sm font-semibold">
          Configuration history
        </h2>
        <p className="text-muted-foreground mb-3 text-sm">
          The 50 most recent configuration versions. Every change to upstreams,
          filtering, access control or these settings creates one.
        </p>
        <ErrorAlert
          error={versions.error}
          prefix="Could not load configuration versions"
          className="mb-3"
        />
        <Card className="overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-10 w-24 text-right">Version</TableHead>
                <TableHead className="h-10">Published</TableHead>
                <TableHead className="h-10">By</TableHead>
                <TableHead className="h-10">Change</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((v) => (
                <TableRow key={v.version} data-testid="version-row">
                  <TableCell className="py-2.5 text-right font-medium tabular-nums">
                    {v.version}
                  </TableCell>
                  <TableCell className="py-2.5 whitespace-nowrap">
                    {formatDateTime(v.created_at)}
                  </TableCell>
                  <TableCell className="py-2.5">{v.created_by}</TableCell>
                  <TableCell className="text-muted-foreground py-2.5">
                    {v.summary}
                  </TableCell>
                </TableRow>
              ))}
              {versions.isPending && (
                <MessageRow colSpan={4}>Loading…</MessageRow>
              )}
              {versions.isSuccess && rows.length === 0 && (
                <MessageRow colSpan={4}>
                  No configuration published yet.
                </MessageRow>
              )}
            </TableBody>
          </Table>
        </Card>
      </section>
    </>
  );
}

function SettingsForm({ settings }: { settings: Settings }) {
  const canUpdate = useCan("updateResolverSettings");
  const qc = useQueryClient();
  const [form, setForm] = useState<Form>(() => toForm(settings));
  const set = <K extends keyof Form>(key: K, value: Form[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(settings));

  const save = useMutation({
    mutationFn: async () => {
      const body = {
        ...settings,
        strategy: form.strategy,
        block_mode: form.block_mode,
        otlp_endpoint: form.otlp_endpoint.trim(),
      };
      for (const k of numberKeys) body[k] = Number(form[k]);
      return unwrap(await api.PUT("/resolver-settings", { body }));
    },
    onSuccess: async (saved) => {
      qc.setQueryData(["resolver-settings"], saved);
      setForm(toForm(saved));
      await qc.invalidateQueries({ queryKey: ["config-versions"] });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  const num = (key: NumberKey, min: number) => ({
    id: `settings-${key}`,
    type: "number",
    min,
    step: 1,
    required: true,
    value: form[key],
    onChange: (e: ChangeEvent<HTMLInputElement>) => set(key, e.target.value),
  });

  return (
    <form onSubmit={submit}>
      <Card className="overflow-hidden">
        <fieldset disabled={!canUpdate || save.isPending} className="divide-y">
          <Group
            title="Upstream selection"
            description="How engines pick among the enabled upstreams."
          >
            <Field label="Strategy" htmlFor="settings-strategy">
              <Select
                value={form.strategy}
                onValueChange={(v) => set("strategy", v as Form["strategy"])}
                disabled={!canUpdate}
              >
                <SelectTrigger
                  id="settings-strategy"
                  data-testid="settings-strategy"
                  className="h-9"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ordered">
                    Ordered (first healthy)
                  </SelectItem>
                  <SelectItem value="fastest">Fastest (lowest RTT)</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </Group>

          <Group
            title="Cache"
            description="Answers are cached per engine, bounded by memory; TTLs are in seconds."
          >
            <Field
              label="Maximum size (bytes)"
              htmlFor="settings-cache_max_bytes"
              hint={
                formatBytes(Number(form.cache_max_bytes)) || "At least 1 MiB"
              }
            >
              <Input
                {...num("cache_max_bytes", 1048576)}
                data-testid="settings-cache-max-bytes"
              />
            </Field>
            <Field label="Minimum TTL" htmlFor="settings-cache_min_ttl">
              <Input
                {...num("cache_min_ttl", 0)}
                data-testid="settings-cache-min-ttl"
              />
            </Field>
            <Field label="Maximum TTL" htmlFor="settings-cache_max_ttl">
              <Input
                {...num("cache_max_ttl", 0)}
                data-testid="settings-cache-max-ttl"
              />
            </Field>
            <Field
              label="Negative answer max TTL"
              htmlFor="settings-cache_negative_max_ttl"
            >
              <Input
                {...num("cache_negative_max_ttl", 0)}
                data-testid="settings-cache-negative-max-ttl"
              />
            </Field>
            <Field
              label="Serve-stale window"
              htmlFor="settings-cache_stale_window"
              hint="Expired answers are served only when resolution fails"
            >
              <Input
                {...num("cache_stale_window", 0)}
                data-testid="settings-cache-stale-window"
              />
            </Field>
          </Group>

          <Group
            title="Blocking"
            description="The answer engines give for a blocked name."
          >
            <Field label="Response" htmlFor="settings-block-mode">
              <Select
                value={form.block_mode}
                onValueChange={(v) =>
                  set("block_mode", v as Form["block_mode"])
                }
                disabled={!canUpdate}
              >
                <SelectTrigger
                  id="settings-block-mode"
                  data-testid="settings-block-mode"
                  className="h-9"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="null_ip">
                    Null address (0.0.0.0 / ::)
                  </SelectItem>
                  <SelectItem value="nxdomain">NXDOMAIN</SelectItem>
                  <SelectItem value="refused">REFUSED</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label="Blocked answer TTL" htmlFor="settings-block_ttl">
              <Input
                {...num("block_ttl", 0)}
                data-testid="settings-block-ttl"
              />
            </Field>
          </Group>

          <Group
            title="Telemetry"
            description="Where engines export OpenTelemetry data, and which queries become traces (SERVFAIL answers always do)."
          >
            <Field
              label="OTLP endpoint"
              htmlFor="settings-otlp-endpoint"
              hint="Empty uses the management plane's default"
              wide
            >
              <Input
                id="settings-otlp-endpoint"
                data-testid="settings-otlp-endpoint"
                className="font-mono"
                placeholder="http://otel-collector:4317"
                value={form.otlp_endpoint}
                onChange={(e) => set("otlp_endpoint", e.target.value)}
              />
            </Field>
            <Field
              label="Trace one query in"
              htmlFor="settings-trace_sample_one_in"
            >
              <Input
                {...num("trace_sample_one_in", 0)}
                data-testid="settings-sample-one-in"
              />
            </Field>
            <Field
              label="Always trace slower than (µs)"
              htmlFor="settings-trace_slow_threshold_us"
            >
              <Input
                {...num("trace_slow_threshold_us", 0)}
                data-testid="settings-slow-threshold-us"
              />
            </Field>
          </Group>
        </fieldset>
        <div className="bg-muted/40 flex flex-wrap items-center justify-end gap-3 border-t px-5 py-3">
          {save.error ? (
            <ErrorAlert
              error={save.error}
              thing="The resolver settings"
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
          {canUpdate && dirty && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setForm(toForm(settings));
                save.reset();
              }}
            >
              Discard
            </Button>
          )}
          {canUpdate && (
            <Button
              type="submit"
              data-testid="settings-save"
              disabled={!dirty || save.isPending}
            >
              {save.isPending ? "Saving…" : "Save settings"}
            </Button>
          )}
        </div>
      </Card>
    </form>
  );
}

function Group({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-4 px-5 py-5 lg:grid-cols-[16rem_1fr] lg:gap-8">
      <div>
        <h3 className="text-sm font-semibold">{title}</h3>
        <p className="text-muted-foreground mt-1 text-sm">{description}</p>
      </div>
      <div className="grid content-start gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {children}
      </div>
    </div>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  wide,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div
      className={
        wide
          ? "grid content-start gap-1.5 sm:col-span-2"
          : "grid content-start gap-1.5"
      }
    >
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  );
}
