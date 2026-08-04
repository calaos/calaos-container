#pragma once

#ifdef __cplusplus
extern "C" {
#endif

typedef struct
{
    char *name;
    char *version_current;
    char *version_new;
} Pkg;

void aptInit();
void *aptCacheGetArray();
int aptCacheArrayCount(void *arr);
Pkg *aptCacheArrayGet(void *arr, int idx);
void aptCacheArrayFree(void *arr);

/* Progress reporting for apt transactions (upgrade/install).
 *
 * stage: "pending"|"download"|"unpack"|"configure"|"install"|"done"|"error"
 * pkg_percent: 0-100 progress of the current package, -1 if unknown
 * total_percent: 0-100 global progress of the transaction, monotonic
 *
 * The callback is invoked synchronously from the thread running the apt
 * transaction (download/dpkg is suspended meanwhile): it must not block.
 */
typedef void (*AptProgressCb)(const char *stage, const char *pkg,
                              int pkg_percent, int total_percent, const char *msg);

/* Register the progress callback (global pointer). NULL = no-op.
 * Registration is done by function pointer so that libcalaos-apt.so has no
 * unresolved (Go) symbol and remains linkable on its own. */
void aptSetProgressCallback(AptProgressCb cb);

/* Emit one progress event through the registered callback. The global
 * total_percent is kept monotonic (max of last and new value).
 * Exposed mainly so the Go side can exercise the full bridge in tests. */
void aptEmitProgress(const char *stage, const char *pkg,
                     int pkg_percent, int total_percent, const char *msg);

/* Run a full dist-upgrade (fetch + install) of all upgradable packages.
 * Blocking. Returns 0 on success; on failure returns non-zero and sets
 * *errorMsg to a strdup()ed message (caller must free()). */
int aptUpgradeAll(char **errorMsg);

/* Install (or upgrade) a single package by name, resolving dependencies.
 * Same contract as aptUpgradeAll(). */
int aptInstallPackage(const char *pkgName, char **errorMsg);

#ifdef __cplusplus
}  // extern "C"
#endif
