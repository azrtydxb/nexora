#include "gate.h"
#include <assert.h>
#include <limits.h>
#include <stdio.h>
int main(void) {
  struct fence_window w = {100, 200};
  assert(!fence_allows(NULL, 100));
  assert(!fence_allows(&w, 99));
  assert(fence_allows(&w, 100));
  assert(fence_allows(&w, 199));
  assert(!fence_allows(&w, 200));
  assert(!fence_allows(&w, 900)); /* SIGSTOP interval/resume, stale replay */
  w = (struct fence_window){0, 200};
  assert(!fence_allows(&w, 1));
  w = (struct fence_window){200, 100};
  assert(!fence_allows(&w, 201));
  w = (struct fence_window){100, 100 + FENCE_HORIZON_NS + 1};
  assert(!fence_allows(&w, 101));
  w.deadline_ns--;
  assert(fence_allows(&w, 101));
  w = (struct fence_window){ULLONG_MAX - 10, 10};
  assert(!fence_allows(&w, ULLONG_MAX - 1));
  puts("policy arithmetic PASS (not kernel evidence)");
}
