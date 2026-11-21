#include "abi.h"

extern int plugin_call(char *, uint8_t *, size_t, rpc_buffer *);
extern void plugin_shutdown(void);

static const host_api *host;

static void release_plugin(void *ptr, size_t len) {
    (void)len;
    free(ptr);
}

static void shutdown_plugin(void) {
    plugin_shutdown();
}

int cliproxy_plugin_init(const host_api *api, plugin_api *plugin) {
    if (!api || api->version != 1 || !api->call || !api->release || !plugin) return 1;
    host = api;
    *plugin = (plugin_api){ 1, plugin_call, release_plugin, shutdown_plugin };
    return 0;
}

int call_host(const char *method, const uint8_t *data, size_t len, rpc_buffer *out) {
    return host->call(host->context, method, data, len, out);
}

void release_host(rpc_buffer buffer) {
    if (buffer.ptr) host->release(buffer.ptr, buffer.len);
}
