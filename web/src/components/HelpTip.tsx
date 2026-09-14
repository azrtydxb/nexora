import { useRef, useState } from "react";
import { Link } from "react-router";
import { Info } from "lucide-react";

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { helpFor } from "@/help/catalog";
import { cn } from "@/lib/utils";

/**
 * Info icon for one help catalogue entry. The tooltip opens on hover and keyboard focus; a tap or
 * click toggles it. Test id `help-<id>`, tooltip content id `help-text-<id>`.
 */
export function HelpTip({
  id,
  label,
  className,
}: {
  id: string;
  label?: string;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  // Radix closes an open tooltip on pointer down, before the click: remember the state it had.
  const openAtPointerDown = useRef(false);
  const entry = helpFor(id);
  if (!entry) {
    if (import.meta.env.DEV) throw new Error(`HelpTip: no help entry "${id}"`);
    return null;
  }
  const textId = `help-text-${id}`;

  return (
    <Tooltip open={open} onOpenChange={setOpen}>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={`Help: ${label ?? id}`}
          aria-describedby={textId}
          data-testid={`help-${id}`}
          className={cn(
            "text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full align-middle focus-visible:ring-2 focus-visible:outline-none",
            className,
          )}
          onPointerDown={() => {
            openAtPointerDown.current = open;
          }}
          onClick={(e) => {
            e.preventDefault();
            // detail 0: a keyboard activation, which has no pointer down.
            if (e.detail === 0) setOpen((o) => !o);
            else setOpen(!openAtPointerDown.current);
          }}
        >
          <Info className="h-4 w-4" aria-hidden="true" />
        </button>
      </TooltipTrigger>
      <TooltipContent
        id={textId}
        side="top"
        collisionPadding={8}
        className="max-w-xs space-y-1 text-xs leading-5"
      >
        <p>{entry.text}</p>
        {entry.default && <p>Default: {entry.default}</p>}
        {entry.range && <p>Range: {entry.range}</p>}
        {entry.effect && <p>Effect: {entry.effect}</p>}
        {entry.topic && (
          <p>
            <Link
              to={`/help/${entry.topic}${entry.anchor ? `#${entry.anchor}` : ""}`}
              className="text-primary font-medium underline-offset-2 hover:underline"
            >
              Learn more
            </Link>
          </p>
        )}
      </TooltipContent>
    </Tooltip>
  );
}
