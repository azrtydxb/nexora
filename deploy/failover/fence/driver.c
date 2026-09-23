#define _GNU_SOURCE
#include "gate.h"
#include <bpf/bpf.h>
#include <bpf/libbpf.h>
#include <errno.h>
#include <linux/rtnetlink.h>
#include <net/if.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

static void die(const char *s) {
  perror(s);
  exit(1);
}
/* BPF boottime is host-relative; an offset time namespace is not equivalent.
 * Reject unknown/child-offset configurations rather than translate clocks. */
static void validate_clock_domain(void) {
  char current[128], children[128], clock[32];
  long long seconds, nanos;
  ssize_t a = readlink("/proc/self/ns/time", current, sizeof(current));
  ssize_t b =
      readlink("/proc/self/ns/time_for_children", children, sizeof(children));
  if (a <= 0 || b != a || memcmp(current, children, a)) {
    fprintf(stderr, "unknown or different child time namespace\n");
    exit(1);
  }
  FILE *f = fopen("/proc/self/timens_offsets", "r");
  if (!f)
    die("time namespace offsets unavailable");
  int seen = 0;
  while (fscanf(f, "%31s %lld %lld", clock, &seconds, &nanos) == 3) {
    if (seconds || nanos) {
      fprintf(stderr, "nonzero time namespace offset\n");
      exit(1);
    }
    if (!strcmp(clock, "boottime"))
      seen = 1;
  }
  fclose(f);
  if (!seen) {
    fprintf(stderr, "unknown boottime offset\n");
    exit(1);
  }
}

static unsigned long long boot_ns(void) {
  struct timespec ts;
  if (clock_gettime(CLOCK_BOOTTIME, &ts))
    die("CLOCK_BOOTTIME");
  return (unsigned long long)ts.tv_sec * 1000000000ULL + ts.tv_nsec;
}

/* Read namespace-local kernel identity; do not trust a sysfs mount inherited
 * from another namespace. Exclusive namespace administration is mandatory. */
static void inspect(int index, const char *token, int type) {
  int fd = socket(AF_NETLINK, SOCK_RAW | SOCK_CLOEXEC, NETLINK_ROUTE);
  if (fd < 0)
    die("netlink socket");
  struct {
    struct nlmsghdr h;
    union {
      struct ifinfomsg link;
      struct tcmsg tc;
    } body;
  } request = {.h = {.nlmsg_len = NLMSG_LENGTH(type == RTM_GETLINK
                                                   ? sizeof(struct ifinfomsg)
                                                   : sizeof(struct tcmsg)),
                     .nlmsg_type = type,
                     .nlmsg_flags = NLM_F_REQUEST | NLM_F_DUMP,
                     .nlmsg_seq = 1}};
  struct sockaddr_nl kernel = {.nl_family = AF_NETLINK};
  if (sendto(fd, &request, request.h.nlmsg_len, 0, (struct sockaddr *)&kernel,
             sizeof(kernel)) < 0)
    die("netlink send");
  int found = 0;
  for (;;) {
    union {
      struct nlmsghdr align;
      char bytes[32768];
    } buffer;
    char *buf = buffer.bytes;
    struct iovec io = {.iov_base = buf, .iov_len = sizeof(buffer.bytes)};
    struct msghdr msg = {.msg_iov = &io, .msg_iovlen = 1};
    int size = recvmsg(fd, &msg, 0);
    if (size <= 0 || (msg.msg_flags & MSG_TRUNC))
      die("netlink receive");
    for (struct nlmsghdr *h = (void *)buf; NLMSG_OK(h, size);
         h = NLMSG_NEXT(h, size)) {
      if (h->nlmsg_flags & NLM_F_DUMP_INTR) {
        fprintf(stderr, "interrupted netlink dump\n");
        exit(1);
      }
      if (h->nlmsg_type == NLMSG_ERROR) {
        fprintf(stderr, "netlink error\n");
        exit(1);
      }
      if (h->nlmsg_seq != 1) {
        fprintf(stderr, "unexpected netlink sequence\n");
        exit(1);
      }
      if (h->nlmsg_type == NLMSG_DONE) {
        if (NLMSG_PAYLOAD(h, 0) >= sizeof(int) && *(int *)NLMSG_DATA(h)) {
          fprintf(stderr, "failed netlink dump\n");
          exit(1);
        }
        close(fd);
        if (type == RTM_GETLINK && !found) {
          fprintf(stderr, "interface disappeared\n");
          exit(1);
        }
        return;
      }
      if (type == RTM_GETLINK && h->nlmsg_type == RTM_NEWLINK) {
        struct ifinfomsg *link = NLMSG_DATA(h);
        if (link->ifi_index != index)
          continue;
        int length = IFLA_PAYLOAD(h), alias_ok = 0, veth = 0;
        if (link->ifi_flags & IFF_UP) {
          fprintf(stderr, "interface must be down\n");
          exit(1);
        }
        for (struct rtattr *r = IFLA_RTA(link); RTA_OK(r, length);
             r = RTA_NEXT(r, length)) {
          if ((r->rta_type & NLA_TYPE_MASK) == IFLA_IFALIAS &&
              RTA_PAYLOAD(r) == strlen(token) + 1 &&
              !memcmp(RTA_DATA(r), token, strlen(token) + 1))
            alias_ok = 1;
          if ((r->rta_type & NLA_TYPE_MASK) == IFLA_XDP) {
            int nested = RTA_PAYLOAD(r);
            for (struct rtattr *n = RTA_DATA(r); RTA_OK(n, nested);
                 n = RTA_NEXT(n, nested)) {
              if ((n->rta_type & NLA_TYPE_MASK) == IFLA_XDP_ATTACHED &&
                  (RTA_PAYLOAD(n) != 1 ||
                   *(unsigned char *)RTA_DATA(n) != XDP_ATTACHED_NONE)) {
                fprintf(stderr, "foreign XDP attachment\n");
                exit(1);
              }
            }
          }
          if ((r->rta_type & NLA_TYPE_MASK) == IFLA_LINKINFO) {
            int nested = RTA_PAYLOAD(r);
            for (struct rtattr *n = RTA_DATA(r); RTA_OK(n, nested);
                 n = RTA_NEXT(n, nested)) {
              if ((n->rta_type & NLA_TYPE_MASK) == IFLA_INFO_KIND &&
                  RTA_PAYLOAD(n) == 5 && !memcmp(RTA_DATA(n), "veth", 5))
                veth = 1;
            }
          }
        }
        if (!alias_ok || !veth) {
          fprintf(stderr, "foreign interface/alias or non-veth\n");
          exit(1);
        }
        found = 1;
      }
      if (type == RTM_GETQDISC && h->nlmsg_type == RTM_NEWQDISC) {
        struct tcmsg *tc = NLMSG_DATA(h);
        if (tc->tcm_ifindex != index)
          continue;
        int length = TCA_PAYLOAD(h), noqueue = 0;
        for (struct rtattr *r = TCA_RTA(tc); RTA_OK(r, length);
             r = RTA_NEXT(r, length)) {
          if ((r->rta_type & NLA_TYPE_MASK) == TCA_KIND &&
              RTA_PAYLOAD(r) == 8 && !memcmp(RTA_DATA(r), "noqueue", 8))
            noqueue = 1;
        }
        if (!noqueue) {
          fprintf(stderr, "foreign qdisc/filter scope\n");
          exit(1);
        }
      }
    }
  }
}

static void no_tcx(int index) {
  const enum bpf_attach_type types[] = {BPF_TCX_INGRESS, BPF_TCX_EGRESS};
  for (unsigned int i = 0; i < sizeof(types) / sizeof(types[0]); i++) {
    __u32 flags = 0, count = 0;
    if (bpf_prog_query(index, types[i], 0, &flags, NULL, &count) || count) {
      fprintf(stderr, "foreign TCX attachment or unsupported TCX query\n");
      exit(1);
    }
  }
}

static void dual_inventory(void) {
  const char *names[] = {"lo", "fg0", "fp0", "bg0", "bp0", "mg0", "mp0"};
  struct if_nameindex *list = if_nameindex();
  if (!list)
    die("interface inventory");
  int count = 0;
  for (struct if_nameindex *p = list; p->if_index; p++) {
    int found = 0;
    for (unsigned int i = 0; i < sizeof(names) / sizeof(names[0]); i++)
      found |= !strcmp(names[i], p->if_name);
    if (!found) {
      fprintf(stderr, "extra isolated lab interface\n");
      exit(1);
    }
    no_tcx(p->if_index);
    count++;
  }
  if_freenameindex(list);
  if (count != 7) {
    fprintf(stderr, "missing isolated lab interface\n");
    exit(1);
  }
}

static int owned_interface(const char *name, const char *token) {
  char ours[128], init[128];
  ssize_t a = readlink("/proc/self/ns/net", ours, sizeof(ours));
  ssize_t b = readlink("/proc/1/ns/net", init, sizeof(init));
  if (a <= 0 || b <= 0 || (a == b && !memcmp(ours, init, a))) {
    fprintf(stderr, "refuse PID1 network namespace or unknown identity\n");
    exit(1);
  }
  if (!*name || strlen(name) >= IFNAMSIZ || strchr(name, '/') ||
      strncmp(token, "nexora-fence:", 13) || strlen(token) < 29 ||
      strlen(token) > 200) {
    fprintf(stderr, "invalid assignment\n");
    exit(1);
  }
  int index = if_nametoindex(name);
  if (!index)
    die("if_nametoindex");
  inspect(index, token, RTM_GETLINK);
  inspect(index, token, RTM_GETQDISC);
  /* TCX can run before classic TC. Unknown query support is a refusal. */
  no_tcx(index);
  return index;
}

/* Refuse map ABI drift and implicit pin/reuse before libbpf can load anything.
 * The object itself is trusted executable code, shipped with this driver. */
static void validate_object(struct bpf_object *obj) {
  struct bpf_map *m;
  struct bpf_program *p;
  int maps = 0, programs = 0;
  bpf_object__for_each_map(m, obj) {
    maps++;
    if (strcmp(bpf_map__name(m), "window") || bpf_map__pin_path(m) ||
        bpf_map__type(m) != BPF_MAP_TYPE_HASH ||
        bpf_map__key_size(m) != sizeof(unsigned int) ||
        bpf_map__value_size(m) != sizeof(struct fence_window) ||
        bpf_map__max_entries(m) != 1 || bpf_map__map_flags(m)) {
      fprintf(stderr, "foreign map or invalid map ABI\n");
      exit(1);
    }
  }
  bpf_object__for_each_program(p, obj) {
    programs++;
    if (strcmp(bpf_program__name(p), "nexora_fence") ||
        strcmp(bpf_program__section_name(p), "tc")) {
      fprintf(stderr, "foreign program\n");
      exit(1);
    }
  }
  if (maps != 1 || programs != 1) {
    fprintf(stderr, "missing/extra program/map\n");
    exit(1);
  }
}

int main(int argc, char **argv) {
  int dual = argc == 7 && !strcmp(argv[1], "--isolated-dual-lab");
  if (argc != 4 && !dual) {
    fprintf(stderr,
            "usage: fence-driver IFACE nexora-fence:TOKEN gate.bpf.o\n");
    return 2;
  }
  validate_clock_domain();
  if (dual)
    dual_inventory();
  const char *names[2] = {argv[dual ? 2 : 1], dual ? argv[4] : NULL};
  const char *aliases[2] = {argv[dual ? 3 : 2], dual ? argv[5] : NULL};
  int indices[2] = {owned_interface(names[0], aliases[0]), 0};
  if (dual) {
    indices[1] = owned_interface(names[1], aliases[1]);
    if (indices[0] == indices[1] || !strcmp(aliases[0], aliases[1])) {
      fprintf(stderr, "distinct exact dual assignments required\n");
      return 1;
    }
  }
  struct bpf_object *obj = bpf_object__open_file(argv[dual ? 6 : 3], NULL);
  if (libbpf_get_error(obj)) {
    fprintf(stderr, "object open failed\n");
    return 1;
  }
  validate_object(obj);
  /* Unsupported helper/kernel/verifier fails before touching interface. */
  if (bpf_object__load(obj)) {
    fprintf(stderr, "kernel gate unsupported/load failed\n");
    return 1;
  }
  struct bpf_program *prog =
      bpf_object__find_program_by_name(obj, "nexora_fence");
  int map = bpf_object__find_map_fd_by_name(obj, "window");
  if (!prog || map < 0) {
    fprintf(stderr, "missing program/map\n");
    return 1;
  }
  unsigned int program = 0;
  /* Both links stay DOWN throughout installation. Missing map entry DENYs
   * every attached hook. Partial installation never reaches READY or ARM.
   * Retain any first hook on error; a new process must never adopt it. */
  for (int i = 0; i < (dual ? 2 : 1); i++) {
#ifdef FENCE_TEST_FAIL_SECOND
    if (dual && i == 1) {
      fprintf(stderr, "INJECTED_SECOND_ATTACH_FAILURE\n");
      return 42;
    }
#endif
    if (owned_interface(names[i], aliases[i]) != indices[i]) {
      fprintf(stderr, "interface identity changed\n");
      return 1;
    }
    LIBBPF_OPTS(bpf_tc_hook, hook, .ifindex = indices[i],
                .attach_point = BPF_TC_EGRESS);
    if (bpf_tc_hook_create(&hook)) {
      fprintf(stderr, "foreign/existing TC hook or unsupported\n");
      return 1;
    }
    LIBBPF_OPTS(bpf_tc_opts, opts, .prog_fd = bpf_program__fd(prog),
                .handle = 1, .priority = 1);
    if (bpf_tc_attach(&hook, &opts)) {
      fprintf(stderr, "attach failed; keep BOTH interfaces DOWN\n");
      return 1;
    }
    LIBBPF_OPTS(bpf_tc_opts, query, .handle = 1, .priority = 1);
    if (bpf_tc_query(&hook, &query) || !query.prog_id ||
        (i && query.prog_id != program)) {
      fprintf(stderr, "exact shared program readback failed\n");
      return 1;
    }
    program = query.prog_id;
  }
  setvbuf(stdout, NULL, _IOLBF, 0);
  if (dual)
    printf("READY2 ifindex=%d backend=%d program=%u horizon_ns=%llu\n",
           indices[0], indices[1], program, FENCE_HORIZON_NS);
  else
    printf("READY ifindex=%d program=%u horizon_ns=%llu\n", indices[0], program,
           FENCE_HORIZON_NS);
  /* One outstanding ticket, issued BEFORE allocator request. ARM carries only
   * that immutable ticket; never computes now + TTL after a lease response. */
  struct fence_window ticket = {0};
  char line[128], extra, canonical[128];
  unsigned long long requested;
  unsigned int key = 0;
  while (fgets(line, sizeof(line), stdin)) {
    if (!strcmp(line, "CAPTURE\n") && (!dual || !ticket.captured_ns)) {
      ticket.captured_ns = boot_ns();
      ticket.deadline_ns = ticket.captured_ns + FENCE_HORIZON_NS;
      printf("TICKET %llu %llu\n", ticket.captured_ns, ticket.deadline_ns);
    } else if (sscanf(line, "ARM %llu %c", &requested, &extra) == 1 &&
               requested == ticket.captured_ns &&
               fence_allows(&ticket, boot_ns()) &&
               (snprintf(canonical, sizeof(canonical), "ARM %llu\n", requested),
                !strcmp(line, canonical))) {
      if (bpf_map_update_elem(map, &key, &ticket, BPF_ANY))
        die("map update");
      ticket = (struct fence_window){0};
      puts("ARMED");
    } else if (!strcmp(line, "DENY\n")) {
      ticket = (struct fence_window){0};
      if (bpf_map_delete_elem(map, &key) && errno != ENOENT)
        die("map delete");
      puts("DENIED");
    } else {
      if (dual) {
        /* Terminal protocol poison; clear the ONE map for both hooks. */
        if (bpf_map_delete_elem(map, &key) && errno != ENOENT)
          die("poison deny");
        puts("REJECTED");
        return 1;
      }
      /* Preserve detached smoke protocol; the Go owner still poisons itself. */
      puts("REJECTED");
    }
  }
  /* Intentionally leave kernel attachment and map alive. No exit-time unguarded
   * window; parent deletes the entire exclusively owned namespace for cleanup.
   */
  bpf_object__close(obj);
  return ferror(stdin) ? 1 : 0;
}
