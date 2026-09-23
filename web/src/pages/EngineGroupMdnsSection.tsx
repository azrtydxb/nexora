import { useState, type FormEvent } from "react";
import type { Schemas } from "@/api/client";
import { type EngineGroup, useUpdateEngineGroup } from "@/api/fleet";
import { useCan } from "@/auth/AuthProvider";
import { ErrorAlert, SavedNote } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { validateMdns, parseInterfaces } from "@/lib/m8Validation";

type Draft = {
  revision: number;
  name: string;
  enabled: boolean;
  reflect: boolean;
  interfaces: string;
  reflectInterfaces: string;
  timeout: string;
};
export function EngineGroupMdnsSection({ group }: { group: EngineGroup }) {
  const canEdit = useCan("updateEngineGroup");
  const save = useUpdateEngineGroup();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [error, setError] = useState("");
  const values = draft ?? {
    revision: group.revision,
    name: group.name,
    enabled: group.mdns.enabled,
    reflect: group.mdns.reflect,
    interfaces: group.mdns.interfaces.join(", "),
    reflectInterfaces: group.mdns.reflect_interfaces.join(", "),
    timeout: String(group.mdns.timeout_ms),
  };
  const change = (fields: Partial<Draft>) => {
    setDraft({ ...values, ...fields });
    setError("");
    save.reset();
  };
  function submit(e: FormEvent) {
    e.preventDefault();
    const mdns: Schemas["MdnsSettings"] = {
      enabled: values.enabled,
      reflect: values.reflect,
      interfaces: parseInterfaces(values.interfaces),
      reflect_interfaces: parseInterfaces(values.reflectInterfaces),
      timeout_ms: Number(values.timeout),
    };
    const problem = validateMdns(mdns);
    if (problem) {
      setError(problem);
      return;
    }
    save.mutate(
      {
        id: group.id,
        body: { revision: values.revision, name: values.name, mdns },
      },
      { onSuccess: () => setDraft(null) },
    );
  }
  return (
    <Card className="min-w-0 p-5">
      <section aria-label="mDNS" className="grid min-w-0 grid-cols-1 gap-4">
        <h2 className="text-sm font-semibold">mDNS</h2>
        <p className="text-muted-foreground text-sm">
          The mDNS gateway needs hostNetwork or an interface on the LAN segment;
          see the operations guide. Gateway and reflection are independently off
          by default.
        </p>
        <form
          onSubmit={submit}
          className="grid min-w-0 grid-cols-1 gap-4"
          noValidate
        >
          <fieldset className="grid min-w-0 grid-cols-1 gap-4">
            <div className="flex items-center gap-2">
              <Switch
                disabled={!canEdit || save.isPending}
                id="mdns-enabled"
                data-testid="mdns-enabled"
                checked={values.enabled}
                onCheckedChange={(enabled) => change({ enabled })}
              />
              <Label htmlFor="mdns-enabled">Enable mDNS gateway</Label>
              <HelpTip id="mdns-enabled" label="Enable mDNS gateway" />
            </div>
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="mdns-interfaces">Gateway interfaces</Label>
                <HelpTip id="mdns-interfaces" label="Gateway interfaces" />
              </div>
              <Input
                disabled={!canEdit || save.isPending}
                id="mdns-interfaces"
                data-testid="mdns-interfaces"
                value={values.interfaces}
                onChange={(e) => change({ interfaces: e.target.value })}
                placeholder="vlan10, vlan20"
              />
            </div>
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="mdns-timeout">Timeout (ms)</Label>
                <HelpTip id="mdns-timeout" label="Timeout (ms)" />
              </div>
              <Input
                disabled={!canEdit || save.isPending}
                id="mdns-timeout"
                data-testid="mdns-timeout"
                type="number"
                min={100}
                max={5000}
                value={values.timeout}
                onChange={(e) => change({ timeout: e.target.value })}
              />
            </div>
            <div className="flex items-center gap-2">
              <Switch
                disabled={!canEdit || save.isPending}
                id="mdns-reflect"
                data-testid="mdns-reflect"
                checked={values.reflect}
                onCheckedChange={(reflect) => change({ reflect })}
              />
              <Label htmlFor="mdns-reflect">Enable reflection</Label>
              <HelpTip id="mdns-reflect" label="Enable reflection" />
            </div>
            <div className="grid gap-1.5">
              <div className="flex gap-1.5">
                <Label htmlFor="mdns-reflect-interfaces">
                  Reflection interfaces
                </Label>
                <HelpTip
                  id="mdns-reflect-interfaces"
                  label="Reflection interfaces"
                />
              </div>
              <Input
                disabled={!canEdit || save.isPending}
                id="mdns-reflect-interfaces"
                data-testid="mdns-reflect-interfaces"
                value={values.reflectInterfaces}
                onChange={(e) => change({ reflectInterfaces: e.target.value })}
              />
            </div>
          </fieldset>
          <ErrorAlert error={error || save.error} thing="This engine group" />
          {canEdit && (
            <Button
              type="submit"
              disabled={!draft || save.isPending}
              className="justify-self-start"
            >
              {save.isPending ? "Saving…" : "Save mDNS"}
            </Button>
          )}
          {draft && (
            <Button
              type="button"
              variant="outline"
              className="justify-self-start"
              disabled={save.isPending}
              onClick={() => window.location.reload()}
            >
              Discard changes and reload
            </Button>
          )}
          <SavedNote show={save.isSuccess}>mDNS saved</SavedNote>
        </form>
      </section>
    </Card>
  );
}
