//! Test-only DNSSEC signing helpers (ECDSA P-256).

use hickory_proto::dnssec::crypto::EcdsaSigningKey;
use hickory_proto::dnssec::rdata::{DNSKEY, DNSSECRData, DS, NSEC, NSEC3, RRSIG, sig::SigInput};
use hickory_proto::dnssec::{Algorithm, DigestType, Nsec3HashAlgorithm, SigningKey, TBS};
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType, SerialNumber};

pub struct TestKey {
    pub zone: Name,
    pub dnskey: DNSKEY,
    key: EcdsaSigningKey,
}

impl TestKey {
    pub fn generate(zone: &str, ksk: bool) -> Self {
        let pkcs8 = EcdsaSigningKey::generate_pkcs8(Algorithm::ECDSAP256SHA256).unwrap();
        let key = EcdsaSigningKey::from_pkcs8(&pkcs8, Algorithm::ECDSAP256SHA256).unwrap();
        let dnskey = DNSKEY::new(true, ksk, false, key.to_public_key().unwrap());
        TestKey {
            zone: Name::from_ascii(zone).unwrap(),
            dnskey,
            key,
        }
    }

    /// Sets the REVOKE flag (RFC 5011 §3), which changes the key tag.
    pub fn set_revoked(&mut self) {
        self.dnskey = DNSKEY::with_flags(
            self.dnskey.flags() | 0x0080,
            self.dnskey.public_key().clone(),
        );
    }

    pub fn ds(&self) -> DS {
        let digest = self
            .dnskey
            .to_digest(&self.zone, DigestType::SHA256)
            .unwrap();
        DS::new(
            self.dnskey.calculate_key_tag().unwrap(),
            Algorithm::ECDSAP256SHA256,
            DigestType::SHA256,
            digest.as_ref().to_vec(),
        )
    }

    pub fn dnskey_record(&self) -> Record {
        Record::from_rdata(
            self.zone.clone(),
            3600,
            RData::DNSSEC(DNSSECRData::DNSKEY(self.dnskey.clone())),
        )
    }

    pub fn sign(&self, rrset: &[Record], inception: u32, expiration: u32) -> Record {
        let first = &rrset[0];
        let input = SigInput {
            type_covered: first.record_type(),
            algorithm: Algorithm::ECDSAP256SHA256,
            num_labels: first.name.num_labels(),
            original_ttl: first.ttl,
            sig_expiration: SerialNumber::new(expiration),
            sig_inception: SerialNumber::new(inception),
            key_tag: self.dnskey.calculate_key_tag().unwrap(),
            signer_name: self.zone.clone(),
        };
        let tbs = TBS::from_input(&first.name, DNSClass::IN, &input, rrset.iter()).unwrap();
        let sig = self.key.sign(&tbs).unwrap();
        Record::from_rdata(
            first.name.clone(),
            first.ttl,
            RData::DNSSEC(DNSSECRData::RRSIG(RRSIG::from_sig(input, sig))),
        )
    }
}

pub fn nsec_chain(_zone: &str, names: &[(&str, &[RecordType])]) -> Vec<(Name, NSEC)> {
    let mut v: Vec<(Name, Vec<RecordType>)> = names
        .iter()
        .map(|(n, t)| (Name::from_ascii(n).unwrap(), t.to_vec()))
        .collect();
    v.sort_by(|a, b| super::denial::canonical_cmp(&a.0, &b.0));
    (0..v.len())
        .map(|i| {
            let next = v[(i + 1) % v.len()].0.clone();
            let mut types = v[i].1.clone();
            types.extend([RecordType::RRSIG, RecordType::NSEC]);
            (v[i].0.clone(), NSEC::new(next, types))
        })
        .collect()
}

pub fn nsec3_chain(
    zone: &str,
    names: &[(&str, &[RecordType])],
    iterations: u16,
    opt_out: bool,
) -> Vec<(Name, NSEC3)> {
    let z = Name::from_ascii(zone).unwrap();
    let mut hashed: Vec<(Vec<u8>, Vec<RecordType>)> = names
        .iter()
        .map(|(n, t)| {
            let h = Nsec3HashAlgorithm::SHA1
                .hash(&[], &Name::from_ascii(n).unwrap(), iterations)
                .unwrap();
            (h.as_ref().to_vec(), t.to_vec())
        })
        .collect();
    hashed.sort();
    (0..hashed.len())
        .map(|i| {
            let next = hashed[(i + 1) % hashed.len()].0.clone();
            let label = data_encoding::BASE32HEX_NOPAD
                .encode(&hashed[i].0)
                .to_ascii_lowercase();
            let owner = Name::from_ascii(&label).unwrap().append_domain(&z).unwrap();
            (
                owner,
                NSEC3::new(
                    Nsec3HashAlgorithm::SHA1,
                    opt_out,
                    iterations,
                    vec![],
                    next,
                    hashed[i].1.clone(),
                ),
            )
        })
        .collect()
}
