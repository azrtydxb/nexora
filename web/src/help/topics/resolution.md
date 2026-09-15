<!-- operations: Performance tuning; Known limitations -->

# Forwarding & recursion

Nexora answers names that no hosted zone covers in one of two ways: it forwards them to upstream
servers, or it resolves them itself from the root servers. Answers are cached on each resolver.
Changing upstreams, the upstream strategy or the resolution settings publishes a new configuration
and clears the cache; zone edits do not.

## Resolution mode

- **Forward** (the default) sends queries to the upstream forwarders. A new install starts in forward
  mode with no upstreams, so add at least one before expecting answers.
- **Recursive** resolves from the root servers. It needs direct outbound UDP and TCP port 53. Networks
  whose gateway intercepts DNS (for example a "DNS shield" or DNS content filter) redirect every
  port-53 query to their own resolver, which breaks recursion. Check with
  `dig +norec @198.41.0.4 example.com A`: a real root server answers with a referral and without the
  `ra` flag. Exempt Nexora's servers from the filter or use forward mode on such networks; forward
  mode with DNSSEC validation works through the redirect.

Recursion is bounded per client query: at most 100 queries to authoritative servers (1–1000) and at
most 32 delegations (1–64); a query that needs more gets SERVFAIL. Authoritative servers are queried
on port 53 unless you change the authority port for a test lab. Root hints are empty by default,
which uses the built-in IANA root servers. Aggressive NSEC caching (off by default) answers names that
validated NSEC records already prove do not exist straight from the cache.

## QNAME minimisation

In recursive mode, QNAME minimisation (on by default) sends each server only as much of the name as it
needs to give its referral, instead of the full query name. It improves privacy towards the root and
TLD servers.

## Forward zones

A forward zone sends queries for one domain and all its subdomains to its own servers (1–16 `ip:port`
addresses, tried in order), in forward and in recursive mode. Use it for an internal corporate zone,
for example. Turn on DNSSEC validation for a forward zone when its servers return signed data that
should be validated. A forward zone can apply to every engine group or to one.

## Upstreams

Upstreams are the forwarders used in forward mode, reached over UDP, TCP, DNS over TLS or DNS over
HTTPS. Each attempt is bounded by the upstream's timeout (default 250 ms, 50–5000 ms), and a client
query never waits more than 2 seconds in total. An upstream that fails three times in a row is marked
down for 5 seconds, then one probe query is allowed through. Disabled upstreams stay listed but get no
queries.

An upstream applies to every engine group or to one. A group in `inherit` mode uses its own upstreams
first, then the global ones; a group in `override` mode uses only its own.

## Strategy

The strategy, under Settings, decides how Nexora picks among the enabled upstreams:

- **Ordered** (the default) uses the first healthy upstream in list order.
- **Fastest** tries the upstream with the lowest measured round-trip time first.
- **Parallel** sends each uncached query to several upstreams at once (see below).

Changing the strategy publishes a new configuration and clears the cache.

## Parallel

With **Parallel**, a query that is not in the cache goes to the healthy upstreams at once, lowest
round-trip time first, up to the parallel maximum (0, the default, means every healthy upstream; the
range is 0–8 and never more than 8 are raced). The first NOERROR or NXDOMAIN reply wins; other
answers are used only when every attempt failed. The slower attempts still finish in the background
and keep each upstream's health current.

Parallel multiplies the load on your upstreams by the number of upstreams raced, and some public
resolvers rate-limit. Every raced upstream sees every uncached query name, which matters for privacy
when upstreams are run by different providers.
