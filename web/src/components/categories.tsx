import { TriangleAlert } from "lucide-react";

import { type LicenseNotice } from "@/api/filterCategories";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/**
 * Shows the catalog notice of every source that is not free for commercial use and asks for explicit
 * acknowledgement; nothing is sent with acknowledge_license until the operator confirms.
 */
export function LicenseNoticeDialog({
  notices,
  confirmLabel,
  pending,
  onConfirm,
  onCancel,
}: {
  notices: LicenseNotice[] | null;
  confirmLabel: string;
  pending?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Dialog
      open={notices !== null}
      onOpenChange={(open) => !open && onCancel()}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>License notice</DialogTitle>
          <DialogDescription>
            This change turns on sources that are not free for commercial use.
            Confirm that your use complies with their licenses.
          </DialogDescription>
        </DialogHeader>
        <ul className="grid gap-3 text-sm">
          {notices?.map((s) => (
            <li
              key={s.key || s.name}
              className="border-warning/40 bg-warning/10 flex gap-2.5 rounded-md border p-3"
            >
              <TriangleAlert
                aria-hidden
                className="text-warning mt-0.5 h-4 w-4 shrink-0"
              />
              <div>
                <div className="font-medium">{s.name}</div>
                <p className="text-muted-foreground mt-0.5">{s.notice}</p>
              </div>
            </li>
          ))}
        </ul>
        <DialogFooter className="gap-2">
          <Button type="button" variant="outline" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="button" disabled={pending} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
