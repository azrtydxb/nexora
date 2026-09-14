//! Attribution of blocked, allowed, rewritten and RPZ decisions, through the decision cache.
mod pipeline;

use nexora_engine::filter::decisions::DecisionCache;
use nexora_engine::filter::index::{FilterDecision, ListHit};
use nexora_engine::filter::{domain_to_wire, synth};

#[test]
fn allowed_hits_carry_list_and_rule_through_the_decision_cache() {
    // One block list listing ads.example.test and one allow list listing ok.ads.example.test.
    let view = synth::view_with(&[
        ("block-1", false, &["ads.example.test"]),
        ("allow-1", true, &["ok.ads.example.test"]),
    ]);
    let cache = DecisionCache::new(16);
    let blocked = domain_to_wire(b"x.ads.example.test").unwrap();
    let allowed = domain_to_wire(b"www.ok.ads.example.test").unwrap();
    for round in 0..2 {
        match cache.decide(&view, &blocked) {
            FilterDecision::Blocked(ListHit { offset, .. }) => {
                assert_eq!(offset, 2, "round {round}: suffix after \\x01x")
            }
            other => panic!("round {round}: {other:?}"),
        }
        match cache.decide(&view, &allowed) {
            FilterDecision::Allowed(hit) => {
                assert_eq!(hit.offset, 4, "round {round}: suffix after \\x03www");
                assert_eq!(
                    view.index().lists()[usize::from(hit.list)].id.as_ref(),
                    "allow-1"
                );
            }
            other => panic!("round {round}: {other:?}"),
        }
    }
    assert!(cache.hits() >= 2, "second round came from the cache");
}

#[test]
fn rewrite_and_rpz_attribution_in_query_records() {
    let rig = pipeline::Rig::start(pipeline::RigOptions {
        rewrites: &[
            ("rw.attr.test", "A", "192.0.2.55"),
            ("*.wild.attr.test", "A", "192.0.2.56"),
        ],
        rpz_file: Some(
            "$TTL 60\n@ SOA ns.rpz.attr. hostmaster.rpz.attr. 1 60 60 86400 60\nblocked.attr.test CNAME .\n",
        ),
    });
    rig.query_a("rw.attr.test.");
    rig.query_a("deep.x.wild.attr.test.");
    rig.query_a("blocked.attr.test.");
    let recs = rig.records(3);
    let by = |n: &str| {
        recs.iter()
            .find(|r| r.name == n)
            .unwrap_or_else(|| panic!("{n} missing: {recs:?}"))
    };
    assert_eq!(
        (
            by("rw.attr.test.").source.as_str(),
            by("rw.attr.test.").rule.as_str()
        ),
        ("rewrite", "rw.attr.test")
    );
    assert_eq!(by("deep.x.wild.attr.test.").rule, "*.wild.attr.test");
    let rpz = by("blocked.attr.test.");
    assert_eq!(
        (rpz.source.as_str(), rpz.rpz_zone.as_str()),
        ("rpz", rig.rpz_zone_id())
    );
}
