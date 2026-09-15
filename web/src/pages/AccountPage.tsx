import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, unwrap, type Schemas } from "@/api/client";
import { useCurrentUser } from "@/auth/AuthProvider";
import {
  ErrorAlert,
  Fact,
  formatDateTime,
  SavedNote,
} from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
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
import { Switch } from "@/components/ui/switch";
import { defaultPreferences, type Preferences } from "@/lib/preferences";
import { applyThemePreference } from "@/lib/theme";

type User = Schemas["User"];

const themes: { value: Preferences["theme"]; label: string }[] = [
  { value: "system", label: "System" },
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
];

function formFrom(u: User) {
  return {
    revision: u.revision,
    email: u.email,
    displayName: u.display_name,
    preferences: { ...defaultPreferences, ...u.preferences },
  };
}

export function AccountPage() {
  const { user } = useCurrentUser();
  // RequireAuth renders this page only once the profile has loaded.
  if (!user) return null;
  return <AccountForm key={user.id} user={user} />;
}

function AccountForm({ user }: { user: User }) {
  const qc = useQueryClient();
  const [form, setForm] = useState(() => formFrom(user));
  const local = user.source === "local";

  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/auth/me", {
          body: {
            revision: form.revision,
            // Identity-provider users keep the email and name their provider sends.
            ...(local
              ? {
                  email: form.email.trim(),
                  display_name: form.displayName.trim(),
                }
              : {}),
            preferences: form.preferences,
          },
        }),
      ),
    onSuccess: async (saved) => {
      setForm(formFrom(saved));
      applyThemePreference(saved.preferences.theme);
      qc.setQueryData(["me"], saved);
      await qc.invalidateQueries({ queryKey: ["me"] });
    },
  });

  const edit = (patch: Partial<typeof form>) => {
    save.reset();
    setForm((f) => ({ ...f, ...patch }));
  };
  const setPref = <K extends keyof Preferences>(k: K, v: Preferences[K]) =>
    edit({ preferences: { ...form.preferences, [k]: v } });

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  return (
    <div data-testid="account-page" className="grid max-w-3xl gap-6">
      <PageHeader
        title="Your account"
        description="Your sign-in details and how Nexora shows information to you."
      />
      <Card className="p-5">
        <dl className="grid grid-cols-2 gap-4 text-sm sm:grid-cols-3">
          <Fact label="Username">{user.username}</Fact>
          <Fact label="Role">{user.role}</Fact>
          <Fact label="Sign-in">
            {local ? "Password" : "Identity provider"}
          </Fact>
          <Fact label="Created">{formatDateTime(user.created_at)}</Fact>
          <Fact label="Last sign-in">{formatDateTime(user.last_login_at)}</Fact>
        </dl>
      </Card>
      <Card className="p-5">
        <form onSubmit={submit} className="grid gap-5">
          <ErrorAlert error={save.error} thing="Your profile" />
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="account-email">Email</Label>
                <HelpTip id="account-email" label="Email" />
              </div>
              <Input
                id="account-email"
                data-testid="account-email"
                type="email"
                autoComplete="email"
                disabled={!local}
                value={form.email}
                onChange={(e) => edit({ email: e.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="account-display-name">Display name</Label>
                <HelpTip id="account-display-name" label="Display name" />
              </div>
              <Input
                id="account-display-name"
                data-testid="account-display-name"
                autoComplete="name"
                maxLength={64}
                disabled={!local}
                value={form.displayName}
                onChange={(e) => edit({ displayName: e.target.value })}
              />
            </div>
            {!local && (
              <p
                data-testid="account-idp-note"
                className="text-muted-foreground text-xs sm:col-span-2"
              >
                Managed by your identity provider
              </p>
            )}
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="account-theme">Theme</Label>
                <HelpTip id="account-theme" label="Theme" />
              </div>
              <Select
                value={form.preferences.theme}
                onValueChange={(v) =>
                  setPref("theme", v as Preferences["theme"])
                }
              >
                <SelectTrigger
                  id="account-theme"
                  data-testid="account-theme"
                  className="h-9"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {themes.map((t) => (
                    <SelectItem key={t.value} value={t.value}>
                      {t.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="account-time-zone">Time zone</Label>
                <HelpTip id="account-time-zone" label="Time zone" />
              </div>
              <Input
                id="account-time-zone"
                data-testid="account-time-zone"
                placeholder="Browser time zone"
                aria-describedby="account-time-zone-hint"
                value={form.preferences.time_zone}
                onChange={(e) => setPref("time_zone", e.target.value.trim())}
              />
              <p
                id="account-time-zone-hint"
                className="text-muted-foreground text-xs"
              >
                An IANA name such as Europe/Brussels.
              </p>
            </div>
          </div>
          <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5 text-sm">
            <span>
              <span className="flex items-center gap-1.5">
                <label htmlFor="account-clock-24h" className="font-medium">
                  24-hour clock
                </label>
                <HelpTip id="account-clock-24h" label="24-hour clock" />
              </span>
              <span className="text-muted-foreground block text-xs">
                Show times as 14:05 instead of 2:05 PM.
              </span>
            </span>
            <Switch
              id="account-clock-24h"
              data-testid="account-clock-24h"
              checked={form.preferences.clock_24h}
              onCheckedChange={(v) => setPref("clock_24h", v)}
            />
          </div>
          <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5 text-sm">
            <span>
              <span className="flex items-center gap-1.5">
                <label htmlFor="account-querylog-live" className="font-medium">
                  Query log live by default
                </label>
                <HelpTip
                  id="account-querylog-live"
                  label="Query log live by default"
                />
              </span>
              <span className="text-muted-foreground block text-xs">
                The query log opens following new queries as they arrive.
              </span>
            </span>
            <Switch
              id="account-querylog-live"
              data-testid="account-querylog-live"
              checked={form.preferences.querylog_live}
              onCheckedChange={(v) => setPref("querylog_live", v)}
            />
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button
              type="submit"
              data-testid="account-save"
              disabled={save.isPending}
            >
              {save.isPending ? "Saving…" : "Save profile"}
            </Button>
            <SavedNote show={save.isSuccess}>Profile saved</SavedNote>
          </div>
        </form>
      </Card>
    </div>
  );
}
