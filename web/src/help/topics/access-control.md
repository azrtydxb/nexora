<!-- operations: First-run setup and access -->

# Access control

Nexora has two client access lists: one for resolving names, one for the zones it hosts. A client
outside the list that applies gets REFUSED, and the refusal shows in the query log with the reason
`acl`.

## Recursion access

Recursion access decides who may use the resolver: cache, forwarding, recursion, rewrites, filtering
and RPZ. It is the global allow list followed by the engine group's extra networks. New installs allow
loopback and private networks. Only clients on this list see the recursion-available flag in answers.

## Authoritative access

Authoritative access decides who may query the zones Nexora hosts. It is a global default list that
applies to every zone without its own list, and allows every address (`0.0.0.0/0, ::/0`) unless you
narrow it. Upgraded installs keep answering hosted zones to everyone.

## Zone allow-query

A zone's allow-query list overrides the authoritative default for that zone; an empty list inherits the
default. Transfers keep their own allowed networks and TSIG keys, and dynamic updates keep their TSIG
keys plus an optional list of allowed source networks (empty means any source).
