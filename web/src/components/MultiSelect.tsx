import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Check, ChevronDown, X } from "lucide-react";

import { cn } from "@/lib/utils";

type Option = { value: string; label?: string };

/**
 * A filter control that selects any number of options. The trigger has test id `id` and shows
 * "Any", up to two labels ("A, AAAA"), or "N selected". The panel is a listbox with a toggle per
 * option (`${id}-option-${value}`), a clear button (`${id}-clear`) and, when searchable, a search
 * box (`${id}-search`). It closes on Escape and on a click outside; arrow keys move between options.
 */
export function MultiSelect({
  id,
  label,
  options,
  value,
  onChange,
  searchable = options.length > 8,
}: {
  id: string;
  label: string;
  options: Option[];
  value: string[];
  onChange: (next: string[]) => void;
  searchable?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [needle, setNeedle] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setOpen(false);
      trigger.current?.focus();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const labelOf = (v: string) => options.find((o) => o.value === v)?.label ?? v;
  const text =
    value.length === 0
      ? "Any"
      : value.length <= 2
        ? value.map(labelOf).join(", ")
        : `${value.length} selected`;
  const q = needle.trim().toLowerCase();
  const shown = q
    ? options.filter((o) =>
        `${o.value} ${o.label ?? ""}`.toLowerCase().includes(q),
      )
    : options;

  const toggle = (v: string) =>
    onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v]);

  // Arrow keys move focus between the search box and the options.
  function onPanelKey(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = Array.from(
      panel.current?.querySelectorAll<HTMLElement>("[data-nav]") ?? [],
    );
    if (items.length === 0) return;
    e.preventDefault();
    const at = items.indexOf(document.activeElement as HTMLElement);
    const step = e.key === "ArrowDown" ? 1 : -1;
    items[(at + step + items.length) % items.length].focus();
  }

  return (
    <div ref={root} className="relative">
      <button
        ref={trigger}
        type="button"
        id={id}
        data-testid={id}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="border-input bg-background focus-visible:ring-ring flex h-9 w-full items-center justify-between gap-1 rounded-md border px-3 text-left text-sm shadow-sm focus-visible:ring-1 focus-visible:outline-none"
        onClick={() => {
          setNeedle("");
          setOpen((o) => !o);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" && !open) {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        <span
          className={cn(
            "truncate",
            value.length === 0 && "text-muted-foreground",
          )}
        >
          {text}
        </span>
        <ChevronDown className="h-4 w-4 shrink-0 opacity-50" />
      </button>
      {open && (
        <div
          ref={panel}
          className="bg-popover text-popover-foreground absolute left-0 z-50 mt-1 w-full rounded-md border p-1 shadow-md sm:w-auto sm:min-w-full"
          onKeyDown={onPanelKey}
        >
          <div className="flex items-center gap-1 p-1">
            {searchable && (
              <input
                data-testid={`${id}-search`}
                data-nav
                aria-label={`Search ${label}`}
                autoFocus
                className="border-input bg-background placeholder:text-muted-foreground focus-visible:ring-ring h-8 min-w-0 flex-1 rounded-md border px-2 text-sm focus-visible:ring-1 focus-visible:outline-none"
                placeholder="Search"
                value={needle}
                onChange={(e) => setNeedle(e.target.value)}
              />
            )}
            <button
              type="button"
              data-testid={`${id}-clear`}
              className="text-muted-foreground hover:text-foreground focus-visible:ring-ring ml-auto flex h-8 items-center gap-1 rounded-md px-2 text-xs focus-visible:ring-1 focus-visible:outline-none disabled:opacity-50"
              disabled={value.length === 0}
              onClick={() => onChange([])}
            >
              <X className="h-3.5 w-3.5" />
              Clear
            </button>
          </div>
          <div
            role="listbox"
            aria-label={label}
            aria-multiselectable="true"
            className="max-h-72 overflow-auto"
          >
            {shown.map((o) => {
              const selected = value.includes(o.value);
              return (
                <button
                  key={o.value}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  data-testid={`${id}-option-${o.value}`}
                  data-nav
                  autoFocus={!searchable && o === shown[0]}
                  className="hover:bg-accent focus:bg-accent focus:text-accent-foreground flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm whitespace-nowrap outline-none"
                  onClick={() => toggle(o.value)}
                >
                  <span
                    className={cn(
                      "border-primary flex h-4 w-4 shrink-0 items-center justify-center rounded-sm border",
                      selected && "bg-primary text-primary-foreground",
                    )}
                  >
                    {selected && <Check className="h-3 w-3" />}
                  </span>
                  {o.label ?? o.value}
                </button>
              );
            })}
            {shown.length === 0 && (
              <div className="text-muted-foreground px-2 py-1.5 text-sm">
                No options
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
