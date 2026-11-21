#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>

typedef struct { void *ptr; size_t len; } rpc_buffer;
typedef struct {
    uint32_t version;
    void *context;
    int (*call)(void *, const char *, const uint8_t *, size_t, rpc_buffer *);
    void (*release)(void *, size_t);
} host_api;
typedef struct {
    uint32_t version;
    int (*call)(char *, uint8_t *, size_t, rpc_buffer *);
    void (*release)(void *, size_t);
    void (*shutdown)(void);
} plugin_api;
int call_host(const char *method, const uint8_t *data, size_t len, rpc_buffer *out);
void release_host(rpc_buffer buffer);
