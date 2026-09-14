import { useState, type FormEvent, type ReactNode } from "react";
import { Plus, X } from "lucide-react";

import { type Schemas } from "@/api/client";
import {
  useResolutionSettings,
  useUpdateResolutionSettings,
} from "@/api/resolution";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert, SavedNote } from "@/components/common";
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
import { Switch } from "@/components/ui/switch";

type Settings = Schemas["ResolutionSettings"];

type Form = {
  mode: Settings["mode"];
  qname_minimisation: boolean;
  aggressive_nsec: boolean;
  max_upstream_queries: string;
  max_delegation_depth: string;
  authority_port: string;
  recursor_cache_mib: string;
  root_hints: { name: string; addresses: string }[];
};

function toForm(s: Settings): Form {
  return {
    mode: s.mode,
    qname_minimisation: s.qname_minimisation,
    aggressive_nsec: s.aggressive_nsec,
    max_upstream_queries: String(s.max_upstream_queries),
    max_delegation_depth: String(s.max_delegation_depth),
    authority_port: String(s.authority_port),
    recursor_cache_mib: String(s.recursor_cache_max_bytes / 1048576),
    root_hints: s.root_hints.map((h) => ({
      name: h.name,
      addresses: h.addresses.join(", "),
    })),
  };
}

/** Splits a comma- or whitespace-separated list, dropping empty entries. */
export function splitList(v: string): string[] {
  return v
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export function ResolutionSection() {
  const settings = useResolutionSettings();
  return (
    <section aria-label="Resolution" className="mb-10">
      <h2 className="mb-1 text-sm font-semibold">Resolution</h2>
      <p className="text-muted-foreground mb-3 max-w-prose text-sm">
        Forward mode sends queries to the upstreams below; recursive mode
        resolves from the root servers. Forward zones override the mode for
        their domains. DNSSEC validation applies to recursion and to validating
        forward zones.
      </p>
      <ErrorAlert
        error={settings.error}
        prefix="Could not load resolution settings"
        className="mb-3"
      />
      {settings.isPending && (
        <Card className="text-muted-foreground p-5 text-sm">Loading…</Card>
      )}
      {settings.data && <ResolutionForm settings={settings.data} />}
    </section>
  );
}

function ResolutionForm({ settings }: { settings: Settings }) {
  const canUpdate = useCan("updateResolutionSettings");
  const [form, setForm] = useState<Form>(() => toForm(settings));
  const set = <K extends keyof Form>(key: K, value: Form[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const setHint = (i: number, patch: Partial<Form["root_hints"][number]>) =>
    set(
      "root_hints",
      form.root_hints.map((h, j) => (j === i ? { ...h, ...patch } : h)),
    );
  const save = useUpdateResolutionSettings();
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(settings));

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(
      {
        mode: form.mode,
        qname_minimisation: form.qname_minimisation,
        aggressive_nsec: form.aggressive_nsec,
        max_upstream_queries: Number(form.max_upstream_queries),
        max_delegation_depth: Number(form.max_delegation_depth),
        authority_port: Number(form.authority_port),
        recursor_cache_max_bytes: Number(form.recursor_cache_mib) * 1048576,
        root_hints: form.root_hints.map((h) => ({
          name: h.name.trim(),
          addresses: splitList(h.addresses),
        })),
        revision: settings.revision,
      },
      { onSuccess: (saved) => setForm(toForm(saved)) },
    );
  }

  return (
    <form onSubmit={submit} noValidate>
      <Card className="overflow-hidden">
        <fieldset
          disabled={!canUpdate || save.isPending}
          className="grid gap-5 px-5 py-5"
        >
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <Field label="Mode" htmlFor="resolution-mode">
              <Select
                value={form.mode}
                onValueChange={(v) => set("mode", v as Form["mode"])}
                disabled={!canUpdate}
              >
                <SelectTrigger id="resolution-mode" className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="forward">Forward</SelectItem>
                  <SelectItem value="recursive">Recursive</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <SwitchField
              id="resolution-qname-min"
              label="QNAME minimisation"
              checked={form.qname_minimisation}
              onChange={(v) => set("qname_minimisation", v)}
            />
            <SwitchField
              id="resolution-aggressive-nsec"
              label="Aggressive NSEC caching"
              checked={form.aggressive_nsec}
              onChange={(v) => set("aggressive_nsec", v)}
            />
            <Field
              label="Maximum upstream queries per client query"
              htmlFor="resolution-max-queries"
            >
              <Input
                id="resolution-max-queries"
                type="number"
                min={1}
                max={1000}
                value={form.max_upstream_queries}
                onChange={(e) => set("max_upstream_queries", e.target.value)}
              />
            </Field>
            <Field
              label="Maximum delegation depth"
              htmlFor="resolution-max-depth"
            >
              <Input
                id="resolution-max-depth"
                type="number"
                min={1}
                max={64}
                value={form.max_delegation_depth}
                onChange={(e) => set("max_delegation_depth", e.target.value)}
              />
            </Field>
            <Field
              label="Authority port"
              htmlFor="resolution-authority-port"
              hint="Port queried on authoritative servers; 53 outside test labs"
            >
              <Input
                id="resolution-authority-port"
                type="number"
                min={1}
                max={65535}
                value={form.authority_port}
                onChange={(e) => set("authority_port", e.target.value)}
              />
            </Field>
            <Field
              label="Recursor cache memory (MiB)"
              htmlFor="resolution-recursor-cache"
              hint="Memory for the RRset, aggressive NSEC and server caches of recursive resolution"
            >
              <Input
                id="resolution-recursor-cache"
                type="number"
                min={4}
                max={16384}
                value={form.recursor_cache_mib}
                onChange={(e) => set("recursor_cache_mib", e.target.value)}
              />
            </Field>
          </div>

          <div className="grid gap-2">
            <div>
              <h3 className="text-sm font-semibold">Root hints</h3>
              <p className="text-muted-foreground text-sm">
                Empty uses the built-in IANA root servers. Addresses are bare
                IPs, comma-separated.
              </p>
            </div>
            {form.root_hints.map((h, i) => {
              const n = i + 1;
              return (
                <div
                  key={i}
                  className="grid items-center gap-2 sm:grid-cols-[16rem_1fr_auto]"
                >
                  <Input
                    aria-label={`Root hint name ${n}`}
                    className="font-mono"
                    placeholder="a.root-servers.net."
                    value={h.name}
                    onChange={(e) => setHint(i, { name: e.target.value })}
                  />
                  <Input
                    aria-label={`Root hint addresses ${n}`}
                    className="font-mono"
                    placeholder="198.41.0.4, 2001:503:ba3e::2:30"
                    value={h.addresses}
                    onChange={(e) => setHint(i, { addresses: e.target.value })}
                  />
                  {canUpdate && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      aria-label={`Remove root hint ${n}`}
                      onClick={() =>
                        set(
                          "root_hints",
                          form.root_hints.filter((_, j) => j !== i),
                        )
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              );
            })}
            {canUpdate && (
              <div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    set("root_hints", [
                      ...form.root_hints,
                      { name: "", addresses: "" },
                    ])
                  }
                >
                  <Plus className="mr-1.5 h-4 w-4" />
                  Add root hint
                </Button>
              </div>
            )}
          </div>
        </fieldset>
        <div className="bg-muted/40 flex flex-wrap items-center justify-end gap-3 border-t px-5 py-3">
          {save.error ? (
            <ErrorAlert
              error={save.error}
              thing="The resolution settings"
              className="mr-auto w-auto flex-1 py-2"
            />
          ) : (
            <span className="text-muted-foreground mr-auto text-sm">
              {!canUpdate ? (
                "Operators and administrators can change these settings."
              ) : dirty ? (
                "Unsaved changes"
              ) : (
                <SavedNote show={save.isSuccess}>
                  Resolution settings saved
                </SavedNote>
              )}
            </span>
          )}
          {canUpdate && (
            <Button type="submit" disabled={!dirty || save.isPending}>
              {save.isPending ? "Saving…" : "Save resolution settings"}
            </Button>
          )}
        </div>
      </Card>
    </form>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  );
}

function SwitchField({
  id,
  label,
  checked,
  onChange,
}: {
  id: string;
  label: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-center gap-2 self-end sm:h-9">
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}
