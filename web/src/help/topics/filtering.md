<!-- operations: Filter categories; Filter index performance -->

# Filtering

Filtering blocks or rewrites answers for names on lists. Every resolver builds one filter index from
all lists that apply to it and decides each query against that index. Adding, removing or changing a
list, or turning a category or source on or off, rebuilds that index on every resolver it applies to.

## Blocklists

A blocklist is a list you subscribe to by URL, in hosts, domain-per-line or Adblock format. Nexora
downloads it when you add it and again on its refresh interval (at least 300 seconds, one day by
default). When a download fails, blocking continues with the last good copy of the list; a list that
has not been refreshed within two intervals is marked stale.

A blocklist entry blocks the domain and all of its subdomains. Blocked names get the configured block
answer: a null address (0.0.0.0 or ::), NXDOMAIN or REFUSED. A list of kind Allow adds its domains to
the allowlist instead of blocking them.

The filter index has a memory cap: the engine group's filter index maximum when set (at least 16 MiB),
else half of the engine's container memory limit, else 512 MiB. A configuration whose index exceeds
the cap is rejected and the engine keeps serving the previous one. A rebuild runs while the previous
index keeps serving, so give engines memory for about 3.5 times the largest index you expect on top
of their normal use; 1 GiB covers every catalog category with a small response cache.

## Categories and licenses

Categories group curated sources from the built-in catalog, which ships with Nexora and cannot be
edited. Every category is off after install; turn a category and each of its sources on or off on the
Filter categories page. A category that is on blocks its enabled sources for clients in no policy
group; a policy group can select a category for its own clients whether or not it is on globally.

Each source shows its license and attribution. Sources that do not allow commercial use (for example
OISD and URLhaus) are only enabled after you acknowledge the license, and the acknowledgement is
recorded in the audit log. A category is marked stale when an enabled source's last refresh failed or
is older than two refresh intervals; blocking continues with the last good copy.

## Allowlist

Allowlist entries beat every blocklist, category and policy block. A name on the allowlist, or under
a domain on it, is never blocked.

The allowlist on the Blocklist / allowlist page, together with lists of kind Allow, applies to clients
in no policy group. A policy group has its own allowlist for its clients.

## Policies

Policy groups select clients by source address. A client in a policy group gets only the group's
filter lists, categories, allowlist, safe search and rewrites, instead of the global ones. When a
client's address is in several groups, the group with the most specific prefix wins.

A policy group can be limited to one engine group; its filter lists must then apply to every engine
group or to that one.

## Rewrites

A rewrite answers a name locally with A, AAAA or CNAME records instead of resolving it. A rule is an
exact name or a wildcard `*.domain` that matches the subdomains; an exact rule beats a wildcard, and
the longest wildcard wins. A CNAME rewrite cannot share its name with A or AAAA rewrites.

Global rewrites answer clients in no policy group. A policy group's rewrites apply only to its
clients.

## Safe search

Safe search answers the domains of Google, Bing and DuckDuckGo with their safe search addresses, and
YouTube with its moderate or strict restricted mode. Global safe search applies to clients in no
policy group; each policy group has its own setting.

## RPZ

Response policy zones rewrite or block answers by query name, answer address or name server. A zone is
transferred from a primary (optionally signed with TSIG) or uploaded as a file. The first zone in the
list whose trigger matches decides. A zone's policy override can replace the action of every rule in
it, or disable the zone so that matches are only logged.

Resolvers keep the last good copy of a transferred zone and keep using it when a refresh fails.

## RPZ ZONEMD

Transfer-backed response policy zones offer **Off**, **If present** (default), and **Required** verification. File sources do not use this control. Required rejects missing digests as well as bad ones; If present accepts missing digests but rejects a bad digest when supplied. A failed check keeps the last good policy zone subject to expiry.

The ZONEMD column lists each reporting engine and its verification status or failure reason. No report is not success. Digest integrity does not establish publisher identity; use authenticated transfers where needed.
