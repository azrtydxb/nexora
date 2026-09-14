<!-- operations: Filter categories; Filter index performance -->

# Filtering

Filtering blocks or rewrites answers for names on lists. Every engine builds one filter index from
all lists that apply to it and decides each query against that index.

## Blocklists

A blocklist entry blocks the domain and all of its subdomains. Blocked names get the configured block
answer: a null address (0.0.0.0 or ::), NXDOMAIN or REFUSED. Lists are fetched by the management
plane and refreshed on their interval; when a refresh fails, blocking continues with the last good
copy of the list.

The filter index has a memory cap: the engine group's filter index maximum when set (at least 16 MiB),
else half of the engine's container memory limit, else 512 MiB. A configuration whose index exceeds
the cap is rejected and the engine keeps serving the previous one. A rebuild runs while the previous
index keeps serving, so give engines memory for about 3.5 times the largest index you expect on top
of their normal use; 1 GiB covers every catalog category with a small response cache.

## Categories and licenses

Categories group curated sources from the built-in catalog. Every category is off after install;
turn a category and each of its sources on or off on the Categories page. Each source shows its
license and attribution. Sources that do not allow commercial use (for example OISD and URLhaus) are
only enabled after you acknowledge the license, and the acknowledgement is recorded in the audit log.
A category is marked stale when an enabled source's last refresh failed or is older than two refresh
intervals.

## Allowlist

The allowlist beats every blocklist: a name on it, or under a domain on it, is never blocked.

## Policies

Policy groups select clients by source address. A client in a policy group gets only the group's
lists and rewrites, instead of the global ones. Safe search can be set for every client or per group.

## Rewrites

A rewrite answers a name locally with A, AAAA or CNAME records instead of resolving it. A rule is an
exact name or a wildcard `*.domain` that matches the subdomains. A policy group's rewrites apply only
to its clients.

## Safe search

Safe search rewrites the major search engines and YouTube to their restricted endpoints, for every
client or for the clients of one policy group.

## RPZ

Response policy zones rewrite or block answers by query name, answer address or name server. A zone is
transferred from a primary (optionally signed with TSIG) or uploaded as a file. The first zone in the
list whose trigger matches decides. Engines keep the last good copy of a transferred zone and keep
using it when a refresh fails.
