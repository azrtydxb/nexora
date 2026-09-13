# What a human decided

Written 2026-09-13 11:14 UTC. procoder reads this
file to avoid asking a question twice; edit an answer here to change what
it believes. Reword the question and it will be asked again.

## [decision] decisions.md

Key: 24d31879fc98
Question: Nexora v1 spec interview (2026-09-13)

All answers recorded in .procoder/specs/nexora-v1.md.

Answer: Not a question — a log entry; every spec decision was answered by the user in the interview and is recorded in .procoder/specs/nexora-v1.md

## [decision] decisions.md

Key: 429b03f7094a
Question: Engine language and base

- Rust engine on hickory-proto codec, own server loop/cache/resolver (recommended)
- Rust, adopt hickory-server/recursor more wholesale
- C++ fresh engine (not dns-c), sanitizers + fuzzing from day one

**Answer (2026-09-13):** Rust + hickory-proto codec; own server loop, cache, resolver.

Answer: Rust engine on hickory-proto codec, own server loop/cache/resolver (chosen by the user via the question tool on 2026-09-13)
