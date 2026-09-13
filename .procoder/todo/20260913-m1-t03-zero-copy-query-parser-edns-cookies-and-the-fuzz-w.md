# M1 Task 3: Zero-copy query parser, EDNS/cookies, and the fuzz workflow

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 3 ("Zero-copy query parser, EDNS/cookies, and the fuzz workflow") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 3 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `compression_pointer_in_question_is_formerr`, `counts_exceeding_packet_are_formerr`, `edns_opt_with_cookie_is_parsed`, `escaped_dot_and_binary_label_round_trip_in_key`, `formerr_reply_echoes_id`, `label_over_63_and_name_over_255_are_formerr`, `parses_simple_query_and_lowercases_key`, `reply_limit_rules`, `response_bit_is_rejected`, `server_cookie_is_deterministic_per_client_and_changes_with_ip`, `short_packet_is_too_short`, `unknown_opcode_is_notimp`
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib -- wire:: edns::'` with test-only modules -> E0425/E0433 `cannot find function \`parse_query\``, `ParseError`, `OptView`, `reply_limit`, ... (50 errors, all missing items).
- Plan deviations (plan text updated): tests ported to hickory-proto 0.26.3's API (`Message::new(id, type, opcode)`, public `metadata`/`queries`/`answers` fields, `add_authority`); `cargo test` accepts one filter, so the command is `--lib -- wire:: edns::`; `engine/fuzz/Cargo.toml` needs its own `[workspace]` (root `exclude` is overridden by the `engine` member prefix: "current package believes it's in a workspace"); `NameKey` Eq/Hash/Debug implemented over `as_wire()`; `QueryView`/`OptView` derive Debug (needed by `unwrap_err`); `walk_response` requires a matching question, no trailing bytes, OPT only in additional.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib -- wire:: edns::'` -> `test result: ok. 15 passed; 0 failed; 1 ignored`.
- Seeds: `cargo test --locked -p nexora-engine --lib write_fuzz_seeds -- --ignored` -> `1 passed`; `a.bin` 29b, `edns_cookie.bin` 52b, `compressed_name.bin` 18b, `long_name.bin` 271b (255-octet name) copied back with kubectl tar.
- `scripts/dev-exec.sh make fuzz-smoke` -> exit 0, `INFO: seed corpus: files: 4`, `Done 9349329 runs in 61 second(s)`, no `artifacts/parse_query/crash-*`.
- `cargo fmt --all -- --check` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings.
- procoder gate clean at commit; committed dce9f18.
