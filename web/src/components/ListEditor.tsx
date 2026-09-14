import { useState, type FormEvent, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";

import { ErrorAlert, MessageRow, SavedNote } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

/**
 * Edits a server-held list of strings locally and saves it in one PUT. Test ids are
 * `<prefix>-input`/`-add`/`-save`, rows `<prefix>-row-<value>` and `<prefix>-remove-<value>`
 * (the access control screen names its input `acl-cidr-input`, passed as inputTestId).
 */
export function ListEditor({
  prefix,
  inputTestId = `${prefix}-input`,
  items,
  loading,
  canEdit,
  inputLabel,
  placeholder,
  addLabel,
  columnLabel,
  extraColumn,
  emptyText,
  savedText,
  thing,
  normalize,
  validate,
  onSave,
  help,
}: {
  prefix: string;
  inputTestId?: string;
  items: string[] | undefined;
  loading: boolean;
  canEdit: boolean;
  inputLabel: string;
  placeholder: string;
  addLabel: string;
  columnLabel: string;
  extraColumn?: { label: string; render: (v: string) => ReactNode };
  emptyText: string;
  savedText: string;
  thing: string;
  normalize: (v: string) => string;
  validate: (v: string) => string | null;
  onSave: (items: string[]) => Promise<unknown>;
  /** Help catalogue id: an info icon beside the input. */
  help?: string;
}) {
  const [draft, setDraft] = useState<string[] | null>(null);
  const [input, setInput] = useState("");
  const [inputError, setInputError] = useState("");
  const values = draft ?? items ?? [];
  const dirty = draft !== null;
  const cols = 1 + (extraColumn ? 1 : 0) + (canEdit ? 1 : 0);
  const errorId = `${prefix}-input-error`;

  const save = useMutation({
    mutationFn: () => onSave(values),
    onSuccess: () => setDraft(null),
  });

  function add(e: FormEvent) {
    e.preventDefault();
    const v = normalize(input);
    const problem = validate(v);
    if (problem) {
      setInputError(problem);
      return;
    }
    if (values.includes(v)) {
      setInputError(`${v} is already listed`);
      return;
    }
    setDraft([...values, v]);
    setInput("");
    setInputError("");
    save.reset();
  }

  function remove(v: string) {
    setDraft(values.filter((x) => x !== v));
    save.reset();
  }

  return (
    <>
      {canEdit && (
        <form
          onSubmit={add}
          className="flex flex-wrap items-start gap-2 border-b p-4"
          noValidate
        >
          <div className="grid min-w-56 flex-1 gap-1.5">
            <Label htmlFor={inputTestId} className="sr-only">
              {inputLabel}
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id={inputTestId}
                data-testid={inputTestId}
                className="font-mono"
                placeholder={placeholder}
                value={input}
                aria-invalid={inputError !== ""}
                aria-describedby={inputError ? errorId : undefined}
                onChange={(e) => {
                  setInput(e.target.value);
                  setInputError("");
                }}
              />
              {help && <HelpTip id={help} label={inputLabel} />}
            </div>
            {inputError && (
              <p id={errorId} className="text-destructive text-xs">
                {inputError}
              </p>
            )}
          </div>
          <Button
            type="submit"
            variant="outline"
            data-testid={`${prefix}-add`}
            className="h-9"
            disabled={items === undefined}
          >
            <Plus className="mr-1.5 h-4 w-4" />
            {addLabel}
          </Button>
        </form>
      )}
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-10">{columnLabel}</TableHead>
            {extraColumn && (
              <TableHead className="h-10">{extraColumn.label}</TableHead>
            )}
            {canEdit && <TableHead className="h-10 w-16" />}
          </TableRow>
        </TableHeader>
        <TableBody>
          {values.map((v) => (
            <TableRow key={v} data-testid={`${prefix}-row-${v}`}>
              <TableCell className="py-2 font-mono text-[13px]">{v}</TableCell>
              {extraColumn && (
                <TableCell className="text-muted-foreground py-2">
                  {extraColumn.render(v)}
                </TableCell>
              )}
              {canEdit && (
                <TableCell className="py-1.5 text-right">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="hover:text-destructive h-8 w-8"
                    data-testid={`${prefix}-remove-${v}`}
                    aria-label={`Remove ${v}`}
                    onClick={() => remove(v)}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </TableCell>
              )}
            </TableRow>
          ))}
          {loading && <MessageRow colSpan={cols}>Loading…</MessageRow>}
          {!loading && items !== undefined && values.length === 0 && (
            <MessageRow colSpan={cols}>{emptyText}</MessageRow>
          )}
        </TableBody>
      </Table>
      {canEdit && (
        <div className="bg-muted/40 flex flex-wrap items-center justify-end gap-3 border-t px-4 py-3">
          {save.error ? (
            <ErrorAlert
              error={save.error}
              thing={thing}
              className="mr-auto w-auto flex-1 py-2"
            />
          ) : (
            <span className="text-muted-foreground mr-auto text-sm">
              {dirty ? (
                "Unsaved changes"
              ) : (
                <SavedNote show={save.isSuccess}>{savedText}</SavedNote>
              )}
            </span>
          )}
          {dirty && (
            <Button
              variant="ghost"
              onClick={() => {
                setDraft(null);
                save.reset();
              }}
            >
              Discard
            </Button>
          )}
          <Button
            data-testid={`${prefix}-save`}
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Saving…" : "Save changes"}
          </Button>
        </div>
      )}
    </>
  );
}
