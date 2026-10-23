#pragma once

#ifdef __cplusplus
extern "C" {
#endif

typedef struct
{
    char *name;
    char *version_current;
    char *version_new;
    int held; /* non-zero when the package is on hold (apt-mark hold) */
} Pkg;

/* Error reporting convention of this ABI
 * ---------------------------------------
 * Every fallible entry point takes a `char **errorMsg` out-parameter. It is
 * set to NULL on entry and, on failure only, to a strdup()ed message that the
 * CALLER MUST free(). Messages are drained from libapt's global `_error` stack
 * before the apt objects that produced them are destroyed (destroying an
 * AptCacheFile discards that stack, see apt-cache-file.cpp).
 *
 * No function of this ABI ever lets a C++ exception escape: exceptions are
 * caught at the boundary and turned into an error return, as unwinding through
 * cgo is undefined behaviour.
 */

/* Initialize the libapt-pkg backend (configuration + system). Must be called
 * once before any other function of this ABI.
 * Returns 0 on success; non-zero on failure, with *errorMsg set. */
int aptInit(char **errorMsg);

/* Build the list of upgradable packages, as computed by a simulated
 * dist-upgrade on the local cache. Held packages are listed too, flagged by
 * `held`: the dist-upgrade keeps them back but apt still sees them upgradable.
 *
 * Returns an opaque non-NULL handle to be released with aptCacheArrayFree().
 * On failure (cache lock held by another apt process, broken/inconsistent
 * dpkg state, unsolvable dist-upgrade) returns NULL and sets *errorMsg. A NULL
 * return is ALWAYS an error and never means "no upgradable package": an empty
 * list is a valid non-NULL handle whose count is 0. */
void *aptCacheGetArray(char **errorMsg);

/* Number of entries in the handle returned by aptCacheGetArray (0 if NULL). */
int aptCacheArrayCount(void *arr);

/* Borrowed pointer to entry `idx`, or NULL if out of range. The entry (and the
 * strings it holds) stays owned by the handle: it is valid until
 * aptCacheArrayFree() and must not be freed individually. */
Pkg *aptCacheArrayGet(void *arr, int idx);

/* Release a handle from aptCacheGetArray and all the strings it owns.
 * NULL-safe. */
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
 * Same contract as aptUpgradeAll(). A held package is refused: MarkInstall()
 * from the user would otherwise override the hold. */
int aptInstallPackage(const char *pkgName, char **errorMsg);

#ifdef __cplusplus
}  // extern "C"
#endif
