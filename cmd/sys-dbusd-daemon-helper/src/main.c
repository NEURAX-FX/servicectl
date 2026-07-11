#define _GNU_SOURCE

#include <arpa/inet.h>
#include <endian.h>
#include <errno.h>
#include <poll.h>
#include <pwd.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/un.h>
#include <unistd.h>

#ifndef SDBUSD_CONTROL_PATH
#define SDBUSD_CONTROL_PATH "/run/servicectl/sys-dbusd/control.sock"
#endif

#ifndef SDBUSD_DAEMON_PATH
#define SDBUSD_DAEMON_PATH "/usr/bin/dbus-daemon"
#endif

#define PROTOCOL_VERSION 1
#define HEADER_SIZE 28
#define MAX_PAYLOAD (64U << 10)
#define MAX_ENV_ENTRIES 256
#define MAX_ENV_VALUE (8U << 10)
#define MAX_ENV_TOTAL (32U << 10)
#define FRONTEND_DAEMON_HELPER 1
#define MESSAGE_HELLO 1
#define MESSAGE_SET_ENVIRONMENT 2
#define MESSAGE_ACTIVATE 3
#define MESSAGE_ACTIVATION_RESULT 4

#define EXIT_GENERIC_FAILURE 1
#define EXIT_NO_MEMORY 2
#define EXIT_CONFIG_INVALID 3
#define EXIT_SETUP_FAILED 4
#define EXIT_NAME_INVALID 5
#define EXIT_SERVICE_NOT_FOUND 6
#define EXIT_PERMISSIONS_INVALID 7
#define EXIT_FILE_INVALID 8
#define EXIT_EXEC_FAILED 9
#define EXIT_INVALID_ARGS 10
#define EXIT_CHILD_SIGNALED 11

extern char **environ;

struct packet_header {
    unsigned char magic[8];
    uint16_t version;
    uint16_t type;
    uint64_t request_id;
    uint32_t length;
    uint32_t flags;
} __attribute__((packed));

struct env_entry {
    char *key;
    char *value;
};

static const unsigned char protocol_magic[8] = {'S', 'D', 'B', 'U', 'S', 0, 0, 1};

static int valid_name(const char *name) {
    size_t length;
    int dots = 0;
    int element_start = 1;
    const unsigned char *p;

    if (!name || !*name || name[0] == ':')
        return 0;
    length = strlen(name);
    if (length > 255)
        return 0;
    for (p = (const unsigned char *)name; *p; ++p) {
        if (*p == '.') {
            if (element_start)
                return 0;
            element_start = 1;
            ++dots;
            continue;
        }
        if (element_start) {
            if (!((*p >= 'A' && *p <= 'Z') || (*p >= 'a' && *p <= 'z') || *p == '_' || *p == '-'))
                return 0;
            element_start = 0;
        } else if (!((*p >= 'A' && *p <= 'Z') || (*p >= 'a' && *p <= 'z') || (*p >= '0' && *p <= '9') || *p == '_' || *p == '-')) {
            return 0;
        }
    }
    return dots > 0 && !element_start;
}

static int blocked_environment(const char *key) {
    static const char *blocked[] = {
        "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "GCONV_PATH", "GLIBC_TUNABLES", "MALLOC_TRACE",
        "PYTHONHOME", "PYTHONPATH", "PERL5LIB", "PERLLIB", "RUBYLIB", "RUBYOPT", "NODE_OPTIONS", "NODE_PATH",
        "BASH_ENV", "ENV", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SESSION_BUS_PID", "DBUS_SESSION_BUS_WINDOWID",
        "DBUS_STARTER_ADDRESS", "DBUS_STARTER_BUS_TYPE", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
        "SSH_AUTH_SOCK", "NOTIFY_SOCKET", "LISTEN_FDS", "LISTEN_PID", "LISTEN_FDNAMES", "WATCHDOG_PID",
        "WATCHDOG_USEC", "INVOCATION_ID", "JOURNAL_STREAM", "MANAGERPID", "SYSTEMD_EXEC_PID", NULL,
    };
    size_t i;
    for (i = 0; blocked[i]; ++i) {
        if (strcmp(key, blocked[i]) == 0)
            return 1;
    }
    return 0;
}

static void free_environment(struct env_entry *entries, size_t count) {
    size_t i;
    for (i = 0; i < count; ++i) {
        free(entries[i].key);
        free(entries[i].value);
    }
    free(entries);
}

static int collect_environment(struct env_entry **out_entries, size_t *out_count) {
    struct env_entry *entries = calloc(MAX_ENV_ENTRIES, sizeof(*entries));
    size_t count = 0;
    size_t total = 0;
    char **p;

    if (!entries)
        return EXIT_NO_MEMORY;
    for (p = environ; p && *p; ++p) {
        const char *equal = strchr(*p, '=');
        size_t key_length;
        size_t value_length;
        if (!equal || equal == *p)
            continue;
        key_length = (size_t)(equal - *p);
        value_length = strlen(equal + 1);
        if (value_length > MAX_ENV_VALUE) {
            free_environment(entries, count);
            return EXIT_INVALID_ARGS;
        }
        char *key = strndup(*p, key_length);
        char *value = strdup(equal + 1);
        if (!key || !value) {
            free(key);
            free(value);
            free_environment(entries, count);
            return EXIT_NO_MEMORY;
        }
        if (strchr(key, '=') || blocked_environment(key)) {
            free(key);
            free(value);
            continue;
        }
        size_t existing;
        for (existing = 0; existing < count; ++existing) {
            if (strcmp(entries[existing].key, key) == 0)
                break;
        }
        if (existing < count) {
            total -= strlen(entries[existing].key) + strlen(entries[existing].value) + 2;
            free(entries[existing].key);
            free(entries[existing].value);
            entries[existing].key = key;
            entries[existing].value = value;
            total += key_length + value_length + 2;
            if (total > MAX_ENV_TOTAL) {
                free_environment(entries, count);
                return EXIT_INVALID_ARGS;
            }
            continue;
        }
        if (count >= MAX_ENV_ENTRIES || total + key_length + value_length + 2 > MAX_ENV_TOTAL) {
            free(key);
            free(value);
            free_environment(entries, count);
            return EXIT_INVALID_ARGS;
        }
        entries[count].key = key;
        entries[count].value = value;
        ++count;
        total += key_length + value_length + 2;
    }
    *out_entries = entries;
    *out_count = count;
    return 0;
}

static void close_unneeded_fds(void) {
#ifdef SYS_close_range
    if (syscall(SYS_close_range, 3U, ~0U, 0U) == 0)
        return;
#endif
    long maximum = sysconf(_SC_OPEN_MAX);
    if (maximum < 0 || maximum > 1048576)
        maximum = 65536;
    for (int fd = 3; fd < maximum; ++fd)
        close(fd);
}

static int check_invocation(void) {
#ifndef SDBUSD_TESTING
    struct passwd *dbus_user = getpwnam("dbus");
    char parent_link[64];
    char parent_exe[4096];
    ssize_t n;

    if (!dbus_user || getuid() != dbus_user->pw_uid || geteuid() != 0)
        return EXIT_PERMISSIONS_INVALID;
    snprintf(parent_link, sizeof(parent_link), "/proc/%ld/exe", (long)getppid());
    n = readlink(parent_link, parent_exe, sizeof(parent_exe) - 1);
    if (n < 0)
        return EXIT_PERMISSIONS_INVALID;
    parent_exe[n] = '\0';
    if (strcmp(parent_exe, SDBUSD_DAEMON_PATH) != 0)
        return EXIT_PERMISSIONS_INVALID;
#endif
    return 0;
}

static int connect_core(void) {
    struct sockaddr_un address;
    struct ucred peer;
    socklen_t peer_length = sizeof(peer);
    int fd = socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
    if (fd < 0)
        return -1;
    memset(&address, 0, sizeof(address));
    address.sun_family = AF_UNIX;
    if (strlen(SDBUSD_CONTROL_PATH) >= sizeof(address.sun_path)) {
        close(fd);
        errno = ENAMETOOLONG;
        return -1;
    }
    strcpy(address.sun_path, SDBUSD_CONTROL_PATH);
    if (connect(fd, (struct sockaddr *)&address, sizeof(address)) < 0) {
        close(fd);
        return -1;
    }
    if (getsockopt(fd, SOL_SOCKET, SO_PEERCRED, &peer, &peer_length) < 0 || peer.uid != 0) {
        close(fd);
        errno = EPERM;
        return -1;
    }
    return fd;
}

static int send_packet(int fd, uint16_t type, uint64_t request_id, const void *payload, uint32_t length) {
    unsigned char *buffer;
    struct packet_header header;
    ssize_t sent;
    if (length > MAX_PAYLOAD)
        return -1;
    memset(&header, 0, sizeof(header));
    memcpy(header.magic, protocol_magic, sizeof(header.magic));
    header.version = htons(PROTOCOL_VERSION);
    header.type = htons(type);
    header.request_id = htobe64(request_id);
    header.length = htonl(length);
    buffer = malloc(HEADER_SIZE + length);
    if (!buffer)
        return -1;
    memcpy(buffer, &header, HEADER_SIZE);
    if (length)
        memcpy(buffer + HEADER_SIZE, payload, length);
    sent = send(fd, buffer, HEADER_SIZE + length, MSG_NOSIGNAL);
    free(buffer);
    return sent == (ssize_t)(HEADER_SIZE + length) ? 0 : -1;
}

static int receive_packet(int fd, uint16_t expected_type, uint64_t expected_request, unsigned char **out_payload, uint32_t *out_length) {
    struct pollfd pollfd = {.fd = fd, .events = POLLIN};
    unsigned char buffer[HEADER_SIZE + MAX_PAYLOAD];
    struct packet_header header;
    ssize_t n;
    int poll_result = poll(&pollfd, 1, 35000);
    if (poll_result <= 0)
        return -1;
    n = recv(fd, buffer, sizeof(buffer), 0);
    if (n < HEADER_SIZE)
        return -1;
    memcpy(&header, buffer, HEADER_SIZE);
    uint32_t length = ntohl(header.length);
    if (memcmp(header.magic, protocol_magic, sizeof(header.magic)) != 0 || ntohs(header.version) != PROTOCOL_VERSION || ntohs(header.type) != expected_type || be64toh(header.request_id) != expected_request || ntohl(header.flags) != 0 || length > MAX_PAYLOAD || n != (ssize_t)(HEADER_SIZE + length))
        return -1;
    *out_payload = malloc(length ? length : 1);
    if (!*out_payload)
        return -1;
    if (length)
        memcpy(*out_payload, buffer + HEADER_SIZE, length);
    *out_length = length;
    return 0;
}

static int send_hello(int fd) {
    unsigned char frontend = FRONTEND_DAEMON_HELPER;
    unsigned char *payload = NULL;
    uint32_t length = 0;
    if (send_packet(fd, MESSAGE_HELLO, 1, &frontend, 1) < 0)
        return -1;
    if (receive_packet(fd, MESSAGE_HELLO, 1, &payload, &length) < 0)
        return -1;
    int valid = length == 1 && payload[0] == FRONTEND_DAEMON_HELPER;
    free(payload);
    return valid ? 0 : -1;
}

static int send_environment(int fd, const struct env_entry *entries, size_t count) {
    unsigned char *payload;
    size_t length = 3;
    size_t offset = 0;
    size_t i;
    unsigned char *response = NULL;
    uint32_t response_length = 0;
    for (i = 0; i < count; ++i)
        length += 2 + strlen(entries[i].key) + 4 + strlen(entries[i].value);
    if (length > MAX_PAYLOAD)
        return -1;
    payload = malloc(length);
    if (!payload)
        return -1;
    payload[offset++] = FRONTEND_DAEMON_HELPER;
    uint16_t count_be = htons((uint16_t)count);
    memcpy(payload + offset, &count_be, sizeof(count_be));
    offset += sizeof(count_be);
    for (i = 0; i < count; ++i) {
        uint16_t key_length = (uint16_t)strlen(entries[i].key);
        uint32_t value_length = (uint32_t)strlen(entries[i].value);
        uint16_t key_be = htons(key_length);
        uint32_t value_be = htonl(value_length);
        memcpy(payload + offset, &key_be, sizeof(key_be));
        offset += sizeof(key_be);
        memcpy(payload + offset, entries[i].key, key_length);
        offset += key_length;
        memcpy(payload + offset, &value_be, sizeof(value_be));
        offset += sizeof(value_be);
        memcpy(payload + offset, entries[i].value, value_length);
        offset += value_length;
    }
    if (send_packet(fd, MESSAGE_SET_ENVIRONMENT, 2, payload, (uint32_t)length) < 0) {
        free(payload);
        return -1;
    }
    free(payload);
    if (receive_packet(fd, MESSAGE_ACTIVATION_RESULT, 2, &response, &response_length) < 0)
        return -1;
    uint16_t result_code = 0xffff;
    if (response_length >= 2)
        memcpy(&result_code, response, sizeof(result_code));
    int success = response_length >= 2 && ntohs(result_code) == 0;
    free(response);
    return success ? 0 : -1;
}

static int map_result(uint16_t result) {
    switch (result) {
    case 0: return 0;
    case 1: return EXIT_CONFIG_INVALID;
    case 2: return EXIT_SERVICE_NOT_FOUND;
    case 3: return EXIT_PERMISSIONS_INVALID;
    case 4: return EXIT_FILE_INVALID;
    case 6: return EXIT_SERVICE_NOT_FOUND;
    case 7: return EXIT_EXEC_FAILED;
    case 10: return EXIT_CHILD_SIGNALED;
    case 12: return EXIT_INVALID_ARGS;
    case 13: return EXIT_NO_MEMORY;
    case 15: return EXIT_NAME_INVALID;
    case 16: return EXIT_SERVICE_NOT_FOUND;
    case 18: return EXIT_SETUP_FAILED;
    default: return EXIT_GENERIC_FAILURE;
    }
}

static int activate(int fd, const char *name) {
    size_t name_length = strlen(name);
    size_t length = 1 + 2 + name_length;
    unsigned char *payload = calloc(1, length);
    unsigned char *response = NULL;
    uint32_t response_length = 0;
    uint16_t name_be;
    int result;
    if (!payload)
        return EXIT_NO_MEMORY;
    payload[0] = FRONTEND_DAEMON_HELPER;
    name_be = htons((uint16_t)name_length);
    memcpy(payload + 1, &name_be, sizeof(name_be));
    memcpy(payload + 3, name, name_length);
    if (send_packet(fd, MESSAGE_ACTIVATE, 3, payload, (uint32_t)length) < 0) {
        free(payload);
        return EXIT_GENERIC_FAILURE;
    }
    free(payload);
    if (receive_packet(fd, MESSAGE_ACTIVATION_RESULT, 3, &response, &response_length) < 0)
        return EXIT_GENERIC_FAILURE;
    if (response_length < 2) {
        free(response);
        return EXIT_GENERIC_FAILURE;
    }
    uint16_t result_code;
    memcpy(&result_code, response, sizeof(result_code));
    result = map_result(ntohs(result_code));
    free(response);
    return result;
}

int main(int argc, char **argv) {
    struct env_entry *environment = NULL;
    size_t environment_count = 0;
    int fd;
    int result;

    if (argc != 2)
        return EXIT_INVALID_ARGS;
    if (!valid_name(argv[1]))
        return EXIT_NAME_INVALID;
    result = check_invocation();
    if (result != 0)
        return result;
    result = collect_environment(&environment, &environment_count);
    if (result != 0)
        return result;
    clearenv();
    close_unneeded_fds();
    fd = connect_core();
    if (fd < 0) {
        free_environment(environment, environment_count);
        return EXIT_GENERIC_FAILURE;
    }
    if (send_hello(fd) < 0 || send_environment(fd, environment, environment_count) < 0) {
        close(fd);
        free_environment(environment, environment_count);
        return EXIT_GENERIC_FAILURE;
    }
    free_environment(environment, environment_count);
    result = activate(fd, argv[1]);
    close(fd);
    return result;
}
