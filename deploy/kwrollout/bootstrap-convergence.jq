# The baseline is captured before bootstrap configuration writes. The parent
# independently verifies these UUIDs against persisted workload identities.
def identity: {id, node_name, engine_group_id};
def nonempty: type == "string" and length > 0;
def version_number: type == "number" and . >= 0 and . == floor;
def fresh($now):
    if type != "string" then false
    else (try (
        (capture("^(?<date>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?<fraction>\\.[0-9]+)?(?<zone>Z|[+-][0-9]{2}:[0-9]{2})$") // error("invalid last-seen time")) as $ts |
        (if $ts.zone == "Z" then 0
         else ($ts.zone[1:3] | tonumber) as $h | ($ts.zone[4:6] | tonumber) as $m |
              if $h > 23 or $m > 59 then error("invalid timezone")
              else ($h * 3600 + $m * 60) * (if $ts.zone[0:1] == "-" then -1 else 1 end) end
         end) as $offset |
        (($ts.date + "Z" | fromdateiso8601) + (("0" + ($ts.fraction // "")) | tonumber) - $offset)
    ) catch null) as $seen |
    $seen != null and ($now - $seen <= 30) and ($seen - $now <= 5)
    end;
now as $now |
($baseline[0] | [.[] | select(.connected == true) | identity] | sort_by(.id)) as $expected |
if type != "array" or length == 0 or ($expected | length == 0) then
    error("empty engine fleet")
elif any(.[]; (.id | nonempty | not)) or
     ([.[].id] | length != (unique | length)) or
     any($expected[]; (.id | nonempty | not) or (.node_name | nonempty | not) or
                     (.engine_group_id | nonempty | not)) or
     ($expected | map(.id) | length != (unique | length)) or
     ($expected | map(.node_name) | length != (unique | length)) then
    error("invalid engine identities")
elif ([.[] | select(.connected == true) | identity] | sort_by(.id)) != $expected then
    error("connected engine identities changed")
else [.[] | select(.id as $id | any($expected[]; .id == $id))] as $engines |
    if any($engines[];
        .connected != true or .revoked_at != null or
        (.status != "current" and .status != "behind") or
        (.persist_error // "") != "" or (.rejected_reason // "") != "" or
        .version_ahead != false or (.last_seen_at | fresh($now) | not) or
        (.applied_version | version_number | not) or (.target_version | version_number | not) or
        .applied_version > .target_version or .target_version > $version or
        (.rejected_version != null and .rejected_version > .applied_version)) then
        error("unhealthy engine or invalid configuration acknowledgement")
    elif all($engines[]; .status == "current" and .applied_version == $version and .target_version == $version) then
        "ready"
    else "pending"
    end
end
