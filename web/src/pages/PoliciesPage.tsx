import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";

import { api, ApiError, unwrap, type Schemas } from "@/api/client";
import {
  licenseNoticesFromError,
  useFilterCategories,
  type LicenseNotice,
} from "@/api/filterCategories";
import {
  useCreatePolicyGroup,
  useDeletePolicyGroup,
  useGlobalSafeSearch,
  usePolicyGroup,
  usePolicyGroups,
  useUpdateGlobalSafeSearch,
  useUpdatePolicyGroup,
} from "@/api/policies";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  MessageRow,
  SavedNote,
} from "@/components/common";
import { LicenseNoticeDialog } from "@/components/categories";
import { EngineGroupName, EngineGroupSelect } from "@/components/fleet";
import { HelpTip } from "@/components/HelpTip";
import { PageHeader } from "@/components/layout/AppShell";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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

type PolicyGroup = Schemas["PolicyGroup"];
type Category = Schemas["FilterCategory"];
type SafeSearch = Schemas["SafeSearch"];
type YouTube = SafeSearch["youtube"];

const offSafeSearch: SafeSearch = {
  google: false,
  bing: false,
  duckduckgo: false,
  youtube: "off",
};

const engines: { key: "google" | "bing" | "duckduckgo"; label: string }[] = [
  { key: "google", label: "Google" },
  { key: "bing", label: "Bing" },
  { key: "duckduckgo", label: "DuckDuckGo" },
];

const youtubeModes: { value: YouTube; label: string }[] = [
  { value: "off", label: "Off" },
  { value: "moderate", label: "Moderate" },
  { value: "strict", label: "Strict" },
];

const conflictText =
  "This group was changed by someone else. Reload to see the latest version.";

/** One entry per non-empty trimmed line. */
function lines(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

function safeSearchSummary(s: SafeSearch): string {
  const on = engines.filter((e) => s[e.key]).map((e) => e.label);
  if (s.youtube !== "off") on.push(`YouTube ${s.youtube}`);
  return on.length ? on.join(", ") : "Off";
}

function useFilterLists() {
  return useQuery({
    queryKey: ["filter-lists"],
    queryFn: async () => unwrap(await api.GET("/filter-lists")),
  });
}

export function PoliciesPage() {
  const canCreate = useCan("createPolicyGroup");
  const canUpdate = useCan("updatePolicyGroup");
  const canDelete = useCan("deletePolicyGroup");
  const groups = usePolicyGroups();
  const lists = useFilterLists();
  const categories = useFilterCategories();
  const del = useDeletePolicyGroup();
  const [editing, setEditing] = useState<PolicyGroup | "new" | null>(null);
  const [deleting, setDeleting] = useState<PolicyGroup | null>(null);
  const rows = groups.data ?? [];
  const listNames = new Map((lists.data ?? []).map((l) => [l.id, l.name]));
  const categoryNames = new Map(
    (categories.data ?? []).map((c) => [c.key, c.name]),
  );
  const cols = 6 + (canUpdate || canDelete ? 1 : 0);

  return (
    <>
      <PageHeader
        title="Policies"
        description="Safe search for every client, and per-client policy groups selected by source address."
      />
      <GlobalSafeSearchCard />

      <section aria-labelledby="policy-groups-heading" className="mt-10">
        <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2
              id="policy-groups-heading"
              className="mb-1 text-sm font-semibold"
            >
              Policy groups
            </h2>
            <p className="text-muted-foreground max-w-prose text-sm">
              Clients in a group get only that group's filter lists, allowlist,
              safe search and rewrites. The most specific CIDR wins.
            </p>
          </div>
          {canCreate && (
            <Button onClick={() => setEditing("new")}>
              <Plus className="mr-1.5 h-4 w-4" />
              New group
            </Button>
          )}
        </div>
        <ErrorAlert
          error={groups.error}
          prefix="Could not load policy groups"
          className="mb-3"
        />
        <Card className="overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Name</TableHead>
                <TableHead>CIDRs</TableHead>
                <TableHead>Filter lists</TableHead>
                <TableHead>Categories</TableHead>
                <TableHead>Safe search</TableHead>
                <TableHead>Engine group</TableHead>
                {(canUpdate || canDelete) && (
                  <TableHead className="w-24 text-right">Actions</TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((g) => (
                <TableRow key={g.id}>
                  <TableCell className="py-3 align-top">
                    <div className="font-medium">{g.name}</div>
                    {g.description && (
                      <div className="text-muted-foreground mt-0.5 text-xs">
                        {g.description}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="py-3 align-top font-mono text-[13px]">
                    {g.cidrs.map((c) => (
                      <div key={c}>{c}</div>
                    ))}
                  </TableCell>
                  <TableCell className="py-3 align-top">
                    {g.filter_list_ids.length === 0 ? (
                      <span className="text-muted-foreground">None</span>
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {g.filter_list_ids.map((id) => (
                          <Badge key={id} variant="secondary">
                            {listNames.get(id) ?? id.slice(0, 8)}
                          </Badge>
                        ))}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="py-3 align-top">
                    {g.category_keys.length === 0 ? (
                      <span className="text-muted-foreground">None</span>
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {g.category_keys.map((k) => (
                          <Badge key={k} variant="secondary">
                            {categoryNames.get(k) ?? k}
                          </Badge>
                        ))}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground py-3 align-top">
                    {safeSearchSummary(g.safe_search)}
                  </TableCell>
                  <TableCell className="py-3 align-top whitespace-nowrap">
                    <EngineGroupName id={g.engine_group_id} />
                  </TableCell>
                  {(canUpdate || canDelete) && (
                    <TableCell className="py-2 text-right align-top whitespace-nowrap">
                      {canUpdate && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8"
                          aria-label={`Edit ${g.name}`}
                          onClick={() => setEditing(g)}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                      )}
                      {canDelete && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="hover:text-destructive h-8 w-8"
                          aria-label={`Delete ${g.name}`}
                          onClick={() => setDeleting(g)}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      )}
                    </TableCell>
                  )}
                </TableRow>
              ))}
              {groups.isPending && (
                <MessageRow colSpan={cols}>Loading…</MessageRow>
              )}
              {groups.isSuccess && rows.length === 0 && (
                <MessageRow colSpan={cols}>
                  No policy groups yet. Every client uses the global filtering
                  and safe search.
                </MessageRow>
              )}
            </TableBody>
          </Table>
        </Card>
      </section>

      {editing !== null && (
        <PolicyGroupDialog
          group={editing === "new" ? null : editing}
          lists={lists.data}
          categories={categories.data}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete policy group"
          description={`Delete policy group ${deleting.name}? Its rewrites are deleted too.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This group"
          onConfirm={() =>
            del.mutateAsync({ id: deleting.id, revision: deleting.revision })
          }
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

function GlobalSafeSearchCard() {
  const canUpdate = useCan("updateGlobalSafeSearch");
  const current = useGlobalSafeSearch();
  return (
    <section aria-labelledby="global-safe-search-heading">
      <Card className="overflow-hidden">
        <div className="grid gap-4 px-5 py-5 lg:grid-cols-[16rem_1fr] lg:gap-8">
          <div>
            <h2
              id="global-safe-search-heading"
              className="text-sm font-semibold"
            >
              Global safe search
            </h2>
            <p className="text-muted-foreground mt-1 text-sm">
              Applies to clients in no policy group. Engines answer search
              domains with the providers' safe search addresses.
            </p>
          </div>
          <div>
            <ErrorAlert
              error={current.error}
              prefix="Could not load global safe search"
            />
            {current.isPending && (
              <p className="text-muted-foreground text-sm">Loading…</p>
            )}
            {current.data && (
              <GlobalSafeSearchForm
                current={current.data}
                canUpdate={canUpdate}
              />
            )}
          </div>
        </div>
      </Card>
    </section>
  );
}

function GlobalSafeSearchForm({
  current,
  canUpdate,
}: {
  current: Schemas["GlobalSafeSearch"];
  canUpdate: boolean;
}) {
  const [form, setForm] = useState<SafeSearch>({
    google: current.google,
    bing: current.bing,
    duckduckgo: current.duckduckgo,
    youtube: current.youtube,
  });
  const save = useUpdateGlobalSafeSearch();

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate({ ...form, revision: current.revision });
  }

  return (
    <form onSubmit={submit} className="grid gap-4">
      <SafeSearchFields
        idPrefix="global-ss"
        value={form}
        disabled={!canUpdate || save.isPending}
        onChange={(v) => {
          setForm(v);
          save.reset();
        }}
      />
      {save.error ? (
        <ErrorAlert error={save.error} thing="Global safe search" />
      ) : null}
      {canUpdate ? (
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={save.isPending}>
            Save global safe search
          </Button>
          <SavedNote show={save.isSuccess}>Global safe search saved</SavedNote>
        </div>
      ) : (
        <p className="text-muted-foreground text-sm">
          Operators and administrators can change safe search.
        </p>
      )}
    </form>
  );
}

function SafeSearchFields({
  idPrefix,
  value,
  disabled,
  onChange,
}: {
  idPrefix: string;
  value: SafeSearch;
  disabled?: boolean;
  onChange: (v: SafeSearch) => void;
}) {
  return (
    <div className="grid gap-4 sm:grid-cols-[auto_1fr] sm:gap-8">
      <div className="grid content-start gap-2.5">
        {engines.map((e) => (
          <div key={e.key} className="flex items-center gap-2.5">
            <Switch
              id={`${idPrefix}-${e.key}`}
              data-help="safe-search-engine"
              checked={value[e.key]}
              disabled={disabled}
              onCheckedChange={(v) => onChange({ ...value, [e.key]: v })}
            />
            <Label htmlFor={`${idPrefix}-${e.key}`}>{e.label}</Label>
            <HelpTip id="safe-search-engine" label={`${e.label} safe search`} />
          </div>
        ))}
      </div>
      <fieldset
        disabled={disabled}
        data-help="safe-search-youtube"
        className="grid content-start gap-2"
      >
        <legend className="mb-2 flex items-center gap-1.5 text-sm font-medium">
          YouTube
          <HelpTip id="safe-search-youtube" label="YouTube restricted mode" />
        </legend>
        <div className="flex flex-wrap gap-x-5 gap-y-2">
          {youtubeModes.map((m) => (
            <label
              key={m.value}
              className="flex items-center gap-2 text-sm has-[:disabled]:opacity-60"
            >
              <input
                type="radio"
                name={`${idPrefix}-youtube`}
                value={m.value}
                checked={value.youtube === m.value}
                onChange={() => onChange({ ...value, youtube: m.value })}
                className="accent-primary h-4 w-4"
              />
              {m.label}
            </label>
          ))}
        </div>
      </fieldset>
    </div>
  );
}

type GroupForm = {
  name: string;
  description: string;
  cidrs: string;
  filterListIds: string[];
  categoryKeys: string[];
  allowlist: string;
  safeSearch: SafeSearch;
  engineGroupId: string | null;
  revision: number;
};

function toGroupForm(g: PolicyGroup | null): GroupForm {
  return {
    name: g?.name ?? "",
    description: g?.description ?? "",
    cidrs: g?.cidrs.join("\n") ?? "",
    filterListIds: g?.filter_list_ids ?? [],
    categoryKeys: g?.category_keys ?? [],
    allowlist: g?.allowlist.join("\n") ?? "",
    safeSearch: g?.safe_search ?? offSafeSearch,
    engineGroupId: g?.engine_group_id ?? null,
    revision: g?.revision ?? 0,
  };
}

function PolicyGroupDialog({
  group,
  lists,
  categories,
  onClose,
}: {
  group: PolicyGroup | null;
  lists: Schemas["FilterList"][] | undefined;
  categories: Category[] | undefined;
  onClose: () => void;
}) {
  const [form, setForm] = useState<GroupForm>(() => toGroupForm(group));
  const [notices, setNotices] = useState<LicenseNotice[] | null>(null);
  const set = <K extends keyof GroupForm>(key: K, value: GroupForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const create = useCreatePolicyGroup();
  const update = useUpdatePolicyGroup();
  const fresh = usePolicyGroup(group?.id ?? "");
  const save = group ? update : create;
  // Only custom block lists can be assigned; allow lists feed the allowlist, not a group, and
  // catalog-managed lists are selected through category_keys.
  const blockLists = (lists ?? []).filter(
    (l) => l.kind === "block" && !l.managed_by_catalog,
  );
  const conflict =
    save.error instanceof ApiError && save.error.code === "conflict";

  function submit(e: FormEvent) {
    e.preventDefault();
    save.reset();
    const pending = groupLicenseNotices(
      categories,
      form.categoryKeys,
      group?.category_keys ?? [],
    );
    if (pending.length > 0) {
      setNotices(pending);
      return;
    }
    send(false);
  }

  function send(acknowledged: boolean) {
    const body = {
      name: form.name.trim(),
      description: form.description.trim(),
      cidrs: lines(form.cidrs),
      filter_list_ids: form.filterListIds,
      allowlist: lines(form.allowlist),
      safe_search: form.safeSearch,
      engine_group_id: form.engineGroupId,
      category_keys: form.categoryKeys,
      acknowledge_license: acknowledged,
    };
    const done = {
      onSuccess: onClose,
      onError: (err: Error) => {
        // The server's refusal (the catalog changed since the page loaded) opens the same notice.
        const refused = licenseNoticesFromError(err, categories);
        if (refused && !acknowledged) setNotices(refused);
      },
    };
    if (group) {
      update.mutate(
        { id: group.id, body: { ...body, revision: form.revision } },
        done,
      );
    } else {
      create.mutate(body, done);
    }
  }

  async function reload() {
    const r = await fresh.refetch();
    if (r.data) {
      setForm(toGroupForm(r.data));
      update.reset();
    }
  }

  function toggleCategory(key: string, on: boolean) {
    set(
      "categoryKeys",
      on
        ? [...form.categoryKeys, key]
        : form.categoryKeys.filter((x) => x !== key),
    );
  }

  function toggleList(id: string, on: boolean) {
    set(
      "filterListIds",
      on
        ? [...form.filterListIds, id]
        : form.filterListIds.filter((x) => x !== id),
    );
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {group ? "Edit policy group" : "New policy group"}
          </DialogTitle>
          <DialogDescription>
            Changes publish a new configuration version that every engine
            applies.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="group-name">Name</Label>
              <HelpTip id="group-name" label="Name" />
            </div>
            <Input
              id="group-name"
              required
              maxLength={63}
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="group-description">Description</Label>
              <HelpTip id="group-description" label="Description" />
            </div>
            <Input
              id="group-description"
              maxLength={500}
              value={form.description}
              onChange={(e) => set("description", e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="policygroup-engine-group">Engine group</Label>
              <HelpTip id="policygroup-engine-group" label="Engine group" />
            </div>
            <EngineGroupSelect
              id="policygroup-engine-group"
              testId="policygroup-engine-group"
              value={form.engineGroupId}
              onChange={(v) => set("engineGroupId", v)}
            />
            <p className="text-muted-foreground text-xs">
              The group's filter lists must apply to the same engine group or to
              all of them.
            </p>
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="group-cidrs">Client CIDRs</Label>
              <HelpTip id="group-cidrs" label="Client CIDRs" />
            </div>
            <Textarea
              id="group-cidrs"
              className="min-h-20 font-mono text-[13px]"
              placeholder={"192.168.10.0/24\nfd00:10::/64"}
              aria-describedby="group-cidrs-hint"
              value={form.cidrs}
              onChange={(e) => set("cidrs", e.target.value)}
            />
            <p id="group-cidrs-hint" className="text-muted-foreground text-xs">
              One prefix per line.
            </p>
          </div>
          <fieldset data-help="group-filter-lists" className="grid gap-2">
            <legend className="mb-1.5 flex items-center gap-1.5 text-sm font-medium">
              Filter lists
              <HelpTip id="group-filter-lists" label="Group filter lists" />
            </legend>
            {blockLists.length === 0 ? (
              <p className="text-muted-foreground text-sm">
                No filter lists yet
              </p>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2">
                {blockLists.map((l) => (
                  <label key={l.id} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      className="accent-primary h-4 w-4"
                      checked={form.filterListIds.includes(l.id)}
                      onChange={(e) => toggleList(l.id, e.target.checked)}
                    />
                    {l.name}
                    {!l.enabled && (
                      <span className="text-muted-foreground text-xs">
                        (disabled)
                      </span>
                    )}
                  </label>
                ))}
              </div>
            )}
          </fieldset>
          <fieldset data-help="group-categories" className="grid gap-2">
            <legend className="mb-1.5 flex items-center gap-1.5 text-sm font-medium">
              Categories
              <HelpTip id="group-categories" label="Group categories" />
            </legend>
            {(categories ?? []).length === 0 ? (
              <p className="text-muted-foreground text-sm">
                No filter categories available
              </p>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2">
                {(categories ?? []).map((c) => (
                  <label
                    key={c.key}
                    className="flex items-center gap-2 text-sm"
                  >
                    <input
                      type="checkbox"
                      className="accent-primary h-4 w-4"
                      checked={form.categoryKeys.includes(c.key)}
                      onChange={(e) => toggleCategory(c.key, e.target.checked)}
                    />
                    {c.name}
                  </label>
                ))}
              </div>
            )}
            <p className="text-muted-foreground text-xs">
              Blocks the category's enabled sources for this group, whether or
              not the category is on globally.
            </p>
          </fieldset>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="group-allowlist">Allowlist</Label>
              <HelpTip id="group-allowlist" label="Allowlist" />
            </div>
            <Textarea
              id="group-allowlist"
              className="min-h-20 font-mono text-[13px]"
              placeholder="school.example"
              aria-describedby="group-allowlist-hint"
              value={form.allowlist}
              onChange={(e) => set("allowlist", e.target.value)}
            />
            <p
              id="group-allowlist-hint"
              className="text-muted-foreground text-xs"
            >
              One domain per line; subdomains are allowed too.
            </p>
          </div>
          <div className="grid gap-2">
            <div className="text-sm font-medium">Safe search</div>
            <SafeSearchFields
              idPrefix="group-ss"
              value={form.safeSearch}
              onChange={(v) => set("safeSearch", v)}
            />
          </div>
          {conflict ? (
            <Alert variant="destructive">
              <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
                <span>{conflictText}</span>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={fresh.isFetching}
                  onClick={() => void reload()}
                >
                  Reload
                </Button>
              </AlertDescription>
            </Alert>
          ) : (
            <ErrorAlert
              error={notices ? null : save.error}
              thing="This group"
            />
          )}
          <ErrorAlert error={fresh.error} prefix="Could not reload the group" />
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
        <LicenseNoticeDialog
          notices={notices}
          confirmLabel="Acknowledge and save"
          pending={save.isPending}
          onCancel={() => {
            save.reset();
            setNotices(null);
          }}
          onConfirm={() => {
            setNotices(null);
            send(true);
          }}
        />
      </DialogContent>
    </Dialog>
  );
}

/**
 * The enabled sources of newly selected categories that are not free for commercial use: the group
 * puts them in effect for its clients, so saving needs acknowledge_license.
 */
function groupLicenseNotices(
  categories: Category[] | undefined,
  selected: string[],
  before: string[],
): LicenseNotice[] {
  return (categories ?? [])
    .filter((c) => selected.includes(c.key) && !before.includes(c.key))
    .flatMap((c) => c.sources.filter((s) => s.enabled && !s.commercial_use))
    .map((s) => ({ key: s.key, name: s.name, notice: s.notice }));
}
