<!-- operations: First-run setup and access -->

# Access control

Nexora has two client access lists: one for resolving names, one for the zones it hosts. A client
outside the list that applies gets REFUSED, and the refusal shows in the query log with the reason
`acl`. Nexora first checks whether a query's name is in a hosted zone: if it is, authoritative access
applies; if not, recursion access applies.

## Recursion access

Recursion access decides who may use the resolver: cache, forwarding, recursion, rewrites, filtering
and RPZ. It is the global allow list, plus the extra networks set on an engine group for the servers
in that group. New installs allow loopback and private ranges (127.0.0.0/8, ::1/128, 10.0.0.0/8,
172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, fc00::/7 and fe80::/10). Only clients on this list see
the recursion-available flag in answers.

## Authoritative access

Authoritative access decides who may query the zones Nexora hosts. It is a global default list that
applies to every zone without its own list, and allows every address (`0.0.0.0/0` and `::/0`) unless
you narrow it. Upgraded installs keep answering hosted zones to everyone. Recursion access does not
affect hosted zones, so a client outside recursion access can still query them.

## Zone allow-query

A zone's allow-query list, on the zone's Transfers tab, replaces the authoritative default for that
zone; an empty list inherits the default. Transfers keep their own allowed networks and TSIG key, and
an empty transfer list refuses every transfer. Dynamic updates keep their TSIG keys plus an optional
list of allowed source networks, where an empty list means any source. The Access control page
summarises each zone's query, transfer and update access.
