CC ?= cc
CFLAGS ?= -O2 -pipe -Wall -Wextra -Werror -std=c17
CPPFLAGS ?=
LDFLAGS ?=

all: sys-dbusd-daemon-helper

sys-dbusd-daemon-helper: src/main.c
	$(CC) $(CPPFLAGS) $(CFLAGS) -o $@ $< $(LDFLAGS)

clean:
	rm -f sys-dbusd-daemon-helper

.PHONY: all clean
