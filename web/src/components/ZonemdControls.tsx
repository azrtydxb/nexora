import type { Schemas } from "@/api/client";
import { HelpTip } from "@/components/HelpTip";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export function ZonemdVerifySelect({
  value,
  onChange,
  disabled,
  rpz = false,
}: {
  value: Schemas["ZonemdVerify"];
  onChange: (value: Schemas["ZonemdVerify"]) => void;
  disabled?: boolean;
  rpz?: boolean;
}) {
  const id = rpz ? "rpz-zonemd-verify" : "zonemd-verify";
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center gap-1.5">
        <Label htmlFor={id}>ZONEMD verification</Label>
        {rpz ? (
          <HelpTip id="rpz-zonemd-verify" label="ZONEMD verification" />
        ) : (
          <HelpTip id="zonemd-verify" label="ZONEMD verification" />
        )}
      </div>
      <Select
        value={value}
        onValueChange={(v) => onChange(v as Schemas["ZonemdVerify"])}
        disabled={disabled}
      >
        <SelectTrigger id={id} data-testid={id}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="off">Off</SelectItem>
          <SelectItem value="if_present">If present</SelectItem>
          <SelectItem value="required">Required</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}

const labels: Record<Schemas["ZonemdStatus"], string> = {
  not_checked: "Not checked",
  off: "Off",
  absent: "Absent",
  verified: "Verified",
  failed: "Failed",
};
export function ZonemdStatus({
  status,
  error,
  testId = "zonemd-status",
}: {
  status: Schemas["ZonemdStatus"];
  error: string;
  testId?: string;
}) {
  return (
    <span
      data-testid={testId}
      className="inline-flex flex-wrap items-center gap-2 text-sm"
    >
      <Badge variant={status === "failed" ? "destructive" : "secondary"}>
        {labels[status] ?? status}
      </Badge>
      {error && <span className="break-words">{error}</span>}
    </span>
  );
}
