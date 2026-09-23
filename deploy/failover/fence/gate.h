#ifndef NEXORA_GATE_H
#define NEXORA_GATE_H
/* Fixed ABI; native endian, nanoseconds in this boot's CLOCK_BOOTTIME domain.
 */
#define FENCE_HORIZON_NS 5000000000ULL
struct fence_window {
  unsigned long long captured_ns;
  unsigned long long deadline_ns;
};
static inline int fence_allows(const struct fence_window *w,
                               unsigned long long now) {
  return w && w->captured_ns != 0 && w->deadline_ns > w->captured_ns &&
         w->deadline_ns - w->captured_ns <= FENCE_HORIZON_NS &&
         now >= w->captured_ns && now < w->deadline_ns;
}
#endif
