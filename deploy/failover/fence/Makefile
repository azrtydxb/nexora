CC ?= cc
BPF_CC ?= clang
BPF_INCLUDES ?=
CFLAGS := -O2 -g -Wall -Wextra -Werror

.PHONY: all test clean
all: gate.bpf.o fence-driver

gate.bpf.o: gate.bpf.c gate.h
	$(BPF_CC) -target bpf $(CFLAGS) $(BPF_INCLUDES) -c $< -o $@

fence-driver: driver.c gate.h
	$(CC) $(CFLAGS) $$(pkg-config --cflags libbpf) driver.c -o $@ $$(pkg-config --libs libbpf)

policy-test: policy_test.c gate.h
	$(CC) $(CFLAGS) policy_test.c -o $@

test: policy-test
	./policy-test
	python3 -m unittest -v test_contract.py

clean:
	rm -f gate.bpf.o missing-map.bpf.o unsupported.bpf.o fence-driver policy-test

.PHONY: smoke-build
smoke-build: all missing-map.bpf.o unsupported.bpf.o

missing-map.bpf.o: negative.bpf.c gate.h
	$(BPF_CC) -target bpf $(CFLAGS) $(BPF_INCLUDES) -DMISSING_MAP -c $< -o $@

unsupported.bpf.o: negative.bpf.c gate.h
	$(BPF_CC) -target bpf $(CFLAGS) $(BPF_INCLUDES) -c $< -o $@

# Explicit fault fixture, never installed as the ordinary driver.
fence-driver-partial: driver.c gate.h
	$(CC) $(CFLAGS) -DFENCE_TEST_FAIL_SECOND $$(pkg-config --cflags libbpf) driver.c -o $@ $$(pkg-config --libs libbpf)
