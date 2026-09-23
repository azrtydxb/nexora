#include "gate.h"
#include <linux/bpf.h>
#include <linux/pkt_cls.h>

#include <bpf/bpf_helpers.h>

/* Hash replacement publishes a complete window atomically. No pins/reused maps.
 */
struct {
  __uint(type, BPF_MAP_TYPE_HASH);
  __uint(max_entries, 1);
  __type(key, __u32);
  __type(value, struct fence_window);
} window SEC(".maps");

SEC("tc")
int nexora_fence(struct __sk_buff *skb) {
  __u32 key = 0;
  struct fence_window *w = bpf_map_lookup_elem(&window, &key);
  (void)skb;
  return fence_allows(w, bpf_ktime_get_boot_ns()) ? TC_ACT_OK : TC_ACT_SHOT;
}
char LICENSE[] SEC("license") = "GPL";
