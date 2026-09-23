import { useState, type FormEvent } from "react";
import type { Schemas } from "@/api/client";
import {
  useOdohSettings,
  useRotateOdohKey,
  useUpdateOdohSettings,
} from "@/api/m8";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  SavedNote,
  formatDateTime,
  MessageRow,
} from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { validateOdoh } from "@/lib/m8Validation";

export function OdohSection() {
  const settings = useOdohSettings();
  return (
    <section aria-label="Oblivious DoH" className="mt-8 grid gap-4">
      <h2 className="text-lg font-semibold">Oblivious DoH</h2>
      <p className="text-muted-foreground text-sm">
        Target and proxy roles are off by default and require the DoH listener.
        Proxy targets see only the engine address; the recursion ACL must allow
        proxies that use this engine as a target.
      </p>
      <ErrorAlert
        error={settings.error}
        prefix="Could not load Oblivious DoH settings"
      />
      {settings.isPending && <p>Loading…</p>}
      {settings.data && <OdohForm settings={settings.data} />}
    </section>
  );
}

function OdohForm({ settings }: { settings: Schemas["OdohSettings"] }) {
  const canEdit = useCan("updateOdohSettings");
  const canRotate = useCan("rotateOdohKey");
  const save = useUpdateOdohSettings();
  const rotate = useRotateOdohKey();
  type Draft = Omit<
    Schemas["OdohSettingsUpdate"],
    "proxy_timeout_ms" | "key_rotation_hours"
  > & { proxy_timeout_ms: string; key_rotation_hours: string };
  const [draft, setDraft] = useState<Draft | null>(null);
  const [error, setError] = useState("");
  const [rotating, setRotating] = useState(false);
  const values: Draft = draft ?? {
    target_enabled: settings.target_enabled,
    proxy_enabled: settings.proxy_enabled,
    proxy_targets: settings.proxy_targets,
    proxy_timeout_ms: String(settings.proxy_timeout_ms),
    key_rotation_hours: String(settings.key_rotation_hours),
    revision: settings.revision,
  };
  const change = (fields: Partial<Draft>) => {
    setDraft({ ...values, ...fields });
    setError("");
    save.reset();
  };
  // One blank row makes the first target easy to enter; never submit it as an empty host.
  const targets = values.proxy_targets.length
    ? values.proxy_targets
    : [{ host: "", ca_pem: "" }];
  const targetChange = (
    i: number,
    fields: Partial<Schemas["OdohProxyTarget"]>,
  ) =>
    change({
      proxy_targets: targets.map((t, j) => (i === j ? { ...t, ...fields } : t)),
    });
  function submit(e: FormEvent) {
    e.preventDefault();
    const body: Schemas["OdohSettingsUpdate"] = {
      ...values,
      proxy_targets: values.proxy_targets.map((t) => ({
        ...t,
        host: t.host.trim(),
      })),
      proxy_timeout_ms: Number(values.proxy_timeout_ms),
      key_rotation_hours: Number(values.key_rotation_hours),
    };
    const problem = validateOdoh(body);
    if (problem) {
      setError(problem);
      return;
    }
    save.mutate(body, { onSuccess: () => setDraft(null) });
  }
  return (
    <Card className="min-w-0 grid gap-5 p-5">
      <form className="grid gap-4" onSubmit={submit} noValidate>
        <fieldset className="grid gap-4">
          <div className="flex items-center gap-2">
            <Switch
              disabled={!canEdit || save.isPending || rotate.isPending}
              id="odoh-target-enabled"
              data-testid="odoh-target-enabled"
              checked={values.target_enabled}
              onCheckedChange={(target_enabled) => change({ target_enabled })}
            />
            <Label htmlFor="odoh-target-enabled">Enable target</Label>
            <HelpTip id="odoh-target-enabled" label="Enable target" />
          </div>
          <div className="flex items-center gap-2">
            <Switch
              disabled={!canEdit || save.isPending || rotate.isPending}
              id="odoh-proxy-enabled"
              data-testid="odoh-proxy-enabled"
              checked={values.proxy_enabled}
              onCheckedChange={(proxy_enabled) => change({ proxy_enabled })}
            />
            <Label htmlFor="odoh-proxy-enabled">Enable proxy</Label>
            <HelpTip id="odoh-proxy-enabled" label="Enable proxy" />
          </div>
          <div data-testid="odoh-targets" className="grid gap-3">
            {targets.map((t, i) => (
              <div key={i} className="grid gap-2 rounded-md border p-3">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor={`odoh-host-${i}`}>Target host</Label>
                  <HelpTip id="odoh-target-host" label="Target host" />
                </div>
                <Input
                  disabled={!canEdit || save.isPending || rotate.isPending}
                  id={`odoh-host-${i}`}
                  data-help="odoh-target-host"
                  value={t.host}
                  maxLength={261}
                  onChange={(e) => targetChange(i, { host: e.target.value })}
                  placeholder="odoh.example:443"
                />
                <div className="flex items-center gap-1.5">
                  <Label htmlFor={`odoh-ca-${i}`}>CA PEM (optional)</Label>
                  <HelpTip id="odoh-target-ca" label="CA PEM" />
                </div>
                <Textarea
                  disabled={!canEdit || save.isPending || rotate.isPending}
                  id={`odoh-ca-${i}`}
                  data-help="odoh-target-ca"
                  value={t.ca_pem}
                  maxLength={65536}
                  onChange={(e) => targetChange(i, { ca_pem: e.target.value })}
                />
                {canEdit && (
                  <Button
                    type="button"
                    variant="outline"
                    className="justify-self-start"
                    disabled={save.isPending || rotate.isPending}
                    onClick={() =>
                      change({
                        proxy_targets: targets.filter((_, j) => j !== i),
                      })
                    }
                  >
                    Remove target {i + 1}
                  </Button>
                )}
              </div>
            ))}
            {canEdit && (
              <Button
                type="button"
                variant="outline"
                className="justify-self-start"
                disabled={
                  targets.length >= 64 || save.isPending || rotate.isPending
                }
                onClick={() =>
                  change({
                    proxy_targets: [...targets, { host: "", ca_pem: "" }],
                  })
                }
              >
                Add target
              </Button>
            )}
          </div>
          <div className="grid gap-1.5">
            <div className="flex gap-1.5">
              <Label htmlFor="odoh-timeout">Proxy timeout (ms)</Label>
              <HelpTip id="odoh-timeout" label="Proxy timeout" />
            </div>
            <Input
              disabled={!canEdit || save.isPending || rotate.isPending}
              id="odoh-timeout"
              type="number"
              min={100}
              max={10000}
              value={values.proxy_timeout_ms}
              onChange={(e) => change({ proxy_timeout_ms: e.target.value })}
            />
          </div>
          <div className="grid gap-1.5">
            <div className="flex gap-1.5">
              <Label htmlFor="odoh-rotation">Key rotation (hours)</Label>
              <HelpTip id="odoh-rotation" label="Key rotation" />
            </div>
            <Input
              disabled={!canEdit || save.isPending || rotate.isPending}
              id="odoh-rotation"
              type="number"
              min={1}
              max={720}
              value={values.key_rotation_hours}
              onChange={(e) => change({ key_rotation_hours: e.target.value })}
            />
          </div>
        </fieldset>
        <ErrorAlert
          error={error || save.error}
          thing="Oblivious DoH settings"
        />
        {canEdit && (
          <Button
            type="submit"
            className="justify-self-start"
            disabled={!draft || save.isPending || rotate.isPending}
          >
            {save.isPending ? "Saving…" : "Save Oblivious DoH"}
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
        <SavedNote show={save.isSuccess}>Oblivious DoH saved</SavedNote>
      </form>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-semibold">Keys</h3>
        <HelpTip id="odoh-keys" label="Key publication" />
        {canRotate && (
          <>
            <Button
              variant="outline"
              data-testid="odoh-rotate"
              disabled={save.isPending || rotate.isPending || !!draft}
              onClick={() => setRotating(true)}
            >
              Rotate key
            </Button>
            <HelpTip id="odoh-rotate" label="Rotate key" />
          </>
        )}
      </div>
      {draft && canRotate && (
        <p className="text-muted-foreground text-sm">
          Save settings before rotating a key.
        </p>
      )}
      <Table data-testid="odoh-keys">
        <TableHeader>
          <TableRow>
            <TableHead>Created</TableHead>
            <TableHead>Listed from</TableHead>
            <TableHead>Valid until</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {settings.keys.map((k) => (
            <TableRow key={k.id}>
              <TableCell>{formatDateTime(k.created_at)}</TableCell>
              <TableCell>{formatDateTime(k.publish_after)}</TableCell>
              <TableCell>{formatDateTime(k.not_after)}</TableCell>
            </TableRow>
          ))}
          {!settings.keys.length && (
            <MessageRow colSpan={3}>No keys.</MessageRow>
          )}
        </TableBody>
      </Table>
      {rotating && (
        <ConfirmDialog
          title="Rotate ODoH key"
          description="Create a new key? It is published after five minutes; existing keys remain usable until they expire."
          confirmLabel="Rotate"
          pendingLabel="Rotating…"
          destructive={false}
          onConfirm={() => rotate.mutateAsync()}
          onClose={() => setRotating(false)}
        />
      )}
    </Card>
  );
}
