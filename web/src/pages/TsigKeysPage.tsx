import { useState, type FormEvent } from "react";
import { ChevronLeft, Plus } from "lucide-react";
import { Link } from "react-router";

import { type Schemas } from "@/api/client";
import { useCreateTsigKey, useDeleteTsigKey, useTsigKeys } from "@/api/zones";
import { useCan } from "@/auth/AuthProvider";
import {
  ConfirmDialog,
  ErrorAlert,
  formatDateTime,
  MessageRow,
  SecretValue,
} from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { PageHeader } from "@/components/layout/AppShell";
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
import { fqdn } from "@/lib/zoneRdataHints";

type TsigKey = Schemas["TsigKey"];
type Algorithm = Schemas["TsigKeyAlgorithm"];

const algorithms: Algorithm[] = ["hmac-sha256", "hmac-sha384", "hmac-sha512"];

export function TsigKeysPage() {
  const canCreate = useCan("createTsigKey");
  const canDelete = useCan("deleteTsigKey");
  const keys = useTsigKeys();
  const del = useDeleteTsigKey();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<TsigKey | null>(null);
  const rows = keys.data ?? [];
  const cols = 3 + (canDelete ? 1 : 0);

  return (
    <>
      <Link
        to="/zones"
        className="text-muted-foreground hover:text-foreground mb-2 inline-flex items-center gap-1 text-sm"
      >
        <ChevronLeft className="h-4 w-4" />
        Zones
      </Link>
      <PageHeader
        title="TSIG keys"
        description="Shared secrets that sign zone transfers, NOTIFY messages and dynamic updates. Secrets are stored encrypted and shown only once, when the key is created."
        actions={
          canCreate && (
            <Button onClick={() => setCreating(true)}>
              <Plus className="mr-1.5 h-4 w-4" />
              New TSIG key
            </Button>
          )
        }
      />
      <ErrorAlert
        error={keys.error}
        prefix="Could not load TSIG keys"
        className="mb-4"
      />
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead>Algorithm</TableHead>
              <TableHead>Created</TableHead>
              {canDelete && (
                <TableHead className="w-24 text-right">Actions</TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((k) => (
              <TableRow key={k.id}>
                <TableCell className="py-3 font-mono text-[13px]">
                  {k.name}
                </TableCell>
                <TableCell className="py-3">{k.algorithm}</TableCell>
                <TableCell className="py-3 text-sm">
                  {formatDateTime(k.created_at)}
                </TableCell>
                {canDelete && (
                  <TableCell className="py-1.5 text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="hover:text-destructive h-8"
                      onClick={() => setDeleting(k)}
                    >
                      Delete
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {keys.isPending && <MessageRow colSpan={cols}>Loading…</MessageRow>}
            {keys.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>No TSIG keys.</MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      {creating && <NewKeyDialog onClose={() => setCreating(false)} />}
      {deleting && (
        <ConfirmDialog
          title="Delete TSIG key"
          description={`Delete ${deleting.name}? Zones still referencing it must drop it first; peers signing with it are refused afterwards.`}
          confirmLabel="Delete"
          pendingLabel="Deleting…"
          thing="This key"
          onConfirm={() => del.mutateAsync(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

function NewKeyDialog({ onClose }: { onClose: () => void }) {
  const create = useCreateTsigKey();
  const [name, setName] = useState("");
  const [algorithm, setAlgorithm] = useState<Algorithm>("hmac-sha256");
  const [secret, setSecret] = useState("");
  const created = create.data;

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate({
      name: fqdn(name),
      algorithm,
      ...(secret.trim() === "" ? {} : { secret: secret.trim() }),
    });
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>New TSIG key</DialogTitle>
          <DialogDescription>
            {created
              ? "Configure the peer with this key now."
              : "Name the key as the peers will. Leave the secret empty to generate a random one."}
          </DialogDescription>
        </DialogHeader>
        {created ? (
          <div className="grid gap-4">
            <p className="text-sm font-medium">
              This secret is shown once. Copy it now; it cannot be retrieved
              later.
            </p>
            <SecretValue value={created.secret} testId="tsig-secret" />
            <div className="grid gap-1.5">
              <span className="text-muted-foreground text-xs">
                BIND configuration
              </span>
              <SecretValue
                value={`key "${created.name}" {\n  algorithm ${created.algorithm};\n  secret "${created.secret}";\n};`}
                testId="tsig-bind-snippet"
              />
            </div>
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={submit} className="grid gap-4" noValidate>
            <div className="grid grid-cols-[1fr_11rem] gap-4">
              <div className="grid gap-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="tsig-name">Key name</Label>
                  <HelpTip id="tsig-name" label="Key name" />
                </div>
                <Input
                  id="tsig-name"
                  className="font-mono"
                  placeholder="xfr-key."
                  maxLength={255}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="grid gap-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="tsig-algorithm">Algorithm</Label>
                  <HelpTip id="tsig-algorithm" label="Algorithm" />
                </div>
                <Select
                  value={algorithm}
                  onValueChange={(v) => setAlgorithm(v as Algorithm)}
                >
                  <SelectTrigger id="tsig-algorithm" className="h-9">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {algorithms.map((a) => (
                      <SelectItem key={a} value={a}>
                        {a}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor="tsig-secret">Secret (base64, optional)</Label>
                <HelpTip id="tsig-secret" label="Secret (base64, optional)" />
              </div>
              <Input
                id="tsig-secret"
                type="password"
                className="font-mono"
                autoComplete="off"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
              />
              <p className="text-muted-foreground text-xs">
                16 to 64 bytes. Stored encrypted and never shown again.
              </p>
            </div>
            <ErrorAlert error={create.error} thing="This key" />
            <DialogFooter className="gap-2 pt-2">
              <Button type="button" variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
