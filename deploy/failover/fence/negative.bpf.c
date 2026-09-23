/* Smoke-only negative fixtures. Never attach these objects. */
#include "gate.h"
#include <linux/bpf.h>
#include <linux/pkt_cls.h>

#include <bpf/bpf_helpers.h>
#ifndef MISSING_MAP
struct {
  __uint(type, BPF_MAP_TYPE_HASH);
  __uint(max_entries, 1);
  __type(key, __u32);
  __type(value, struct fence_window);
} window SEC(".maps");
#endif
SEC("tc")
int nexora_fence(struct __sk_buff *skb) {
  (void)skb;
#ifdef MISSING_MAP
  return TC_ACT_SHOT;
#else
  /* An unknown helper must be rejected by the verifier. No graceful fallback.
   */
  long (*unsupported)(void) = (void *)0x7fffffff;
  return unsupported();
#endif
}
char LICENSE[] SEC("license") = "GPL";
