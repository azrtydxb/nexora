<!-- operations: Performance tuning; Known limitations -->

# Forwarding & recursion

Every engine resolves names that no hosted zone answers, either by forwarding them to upstream
servers or by recursing from the root servers itself. Answers are cached per engine. Changing
upstreams, the upstream strategy or resolution settings clears the cache; zone edits do not.

## Resolution mode

- **Forward** sends queries to the upstream forwarders. A new install starts in forward mode with no
  upstreams, so add at least one before expecting answers.
- **Recursive** resolves from the root servers. It needs direct outbound UDP and TCP port 53. Networks
  whose gateway intercepts DNS (for example a "DNS shield" or DNS content filter) redirect every
  port-53 query to their own resolver, which breaks recursion. Check with
  `dig +norec @198.41.0.4 example.com A`: a real root server answers with a referral and without the
  `ra` flag. Exempt the engines from the filter or use forward mode on such networks.

## QNAME minimisation

In recursive mode, QNAME minimisation sends each server only as much of the name as it needs to give
its referral, instead of the full query name. It improves privacy towards the root and TLD servers.

## Forward zones

A forward zone sends queries for one domain and its subdomains to its own servers, whatever the
resolution mode. Turn on DNSSEC validation for a forward zone when its servers return signed data
that should be validated.

## Upstreams

Upstreams are the forwarders used in forward mode: UDP, TCP, DoT or DoH. Each attempt is bounded by
the upstream's timeout (default 250 ms). An upstream that fails three times in a row is marked down
for 5 seconds, then one probe query is allowed through. Upstreams can apply to every engine group or
to one.

## Strategy

The strategy decides how engines pick among the enabled upstreams:

- **ordered** uses the first healthy upstream in list order.
- **fastest** chooses by measured round-trip time.
- **parallel** sends each query to several upstreams at once (see below).

## Parallel

With **parallel**, a query that is not in the cache goes to the healthy upstreams at once, fastest
first, up to the parallel maximum (0 means every candidate, never more than 8). The first NOERROR or
NXDOMAIN reply wins. The slower attempts still finish in the background and keep each upstream's
health current.

Parallel multiplies the load on your upstreams by the number of upstreams raced, and every raced
upstream sees every uncached query name, which matters for privacy when upstreams are run by
different providers.
