#![no_main]
use libfuzzer_sys::fuzz_target;
use nexora_engine::{edns, wire};

fuzz_target!(|data: &[u8]| {
    let mut out = [0u8; 1232];
    match wire::parse_query(data) {
        Ok(q) => {
            let opt = q.opt.as_ref().map(|o| edns::ReplyOpt {
                udp_size: 1232,
                do_bit: o.do_bit,
                ext_rcode: 0,
                cookie: None,
                ede: None,
            });
            // SERVFAIL carries an RFC 8914 EDE option (6, DNSSEC Bogus) as the validator writes it.
            let servfail_opt = q.opt.as_ref().map(|o| edns::ReplyOpt {
                udp_size: 1232,
                do_bit: o.do_bit,
                ext_rcode: 0,
                cookie: None,
                ede: Some(6),
            });
            let _ =
                wire::write_rcode_reply(&q, wire::RCODE_SERVFAIL, &mut out, servfail_opt.as_ref());
            let _ = wire::write_synth_reply(
                &q,
                wire::RCODE_NOERROR,
                Some(wire::SynthAnswer::A([0; 4])),
                60,
                &mut out,
                opt.as_ref(),
            );
            // Treat the same bytes as an upstream response for this question.
            let _ = wire::walk_response(data, &q);
        }
        Err(_) => {
            let _ = wire::write_error_reply(data, wire::RCODE_FORMERR, &mut out);
        }
    }
});
