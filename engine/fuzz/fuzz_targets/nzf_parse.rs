#![no_main]
use libfuzzer_sys::fuzz_target;
use nexora_engine::authoritative::{nzf, zone::Zone};

// NZF1 blobs arrive from the management plane; parsing and zone building must never panic or
// read out of bounds, whatever the bytes.
fuzz_target!(|data: &[u8]| {
    let Ok(p) = nzf::parse(data) else {
        let _ = nzf::decompress(data, 1 << 20);
        return;
    };
    match p.kind {
        nzf::Kind::Full => {
            if let Ok(z) = Zone::from_image(&p) {
                let _ = z.records_sorted();
                let _ = z.soa_rdata();
                let _ = z.nsec_covering(b"");
                let _ = z.nsec3_covering(&[0xff; 20]);
                // Feed the same image back as a delta source: exercises apply's error paths.
                let _ = z.apply(&p);
            }
        }
        nzf::Kind::Delta => {
            let _ = Zone::from_image(&p);
        }
    }
});
