#include "apt.h"
#include "apt-cache-file.h"

#include <climits>
#include <cstdlib>
#include <cstring>
#include <string>
#include <utility>
#include <vector>

#include <apt-pkg/error.h>
#include <apt-pkg/init.h>
#include <apt-pkg/pkgsystem.h>

using namespace APT;
using namespace std;

typedef std::vector<Pkg> PkgList;

namespace
{

/* Drain libapt's global error stack into *errorMsg as a strdup()ed string the
 * caller must free(). `context` prefixes the apt messages (and is used alone
 * when apt recorded nothing, so the caller never gets an empty error).
 *
 * MUST be called BEFORE the AptCacheFile that produced the errors goes out of
 * scope: ~AptCacheFile() -> Close() -> _error->Discard() throws the stack away.
 *
 * Note this deliberately does not reuse fail() from apt-install.cpp: that one
 * also emits an "error" progress event, which is meaningless here (listing the
 * cache is not a transaction and has no progress callback registered). The
 * loop condition is _error->empty() rather than PopMessage()'s return value,
 * because PopMessage() returns false for a *warning* and would stop draining
 * early whenever apt warns before it fails.
 */
void setError(char **errorMsg, const char *context)
{
    std::string all;

    while (_error->empty() == false)
    {
        std::string msg;
        _error->PopMessage(msg);

        if (msg.empty())
            continue;
        if (!all.empty())
            all += "\n";
        all += msg;
    }

    std::string full = context != nullptr ? std::string(context) : std::string();
    if (!all.empty())
        full += full.empty() ? all : ": " + all;
    if (full.empty())
        full = "unknown APT error";

    if (errorMsg != nullptr && *errorMsg == nullptr)
        *errorMsg = strdup(full.c_str());
}

/* Free the strdup()ed strings of one entry. */
void freePkg(Pkg &p)
{
    free(p.name);
    free(p.version_current);
    free(p.version_new);
    p.name = nullptr;
    p.version_current = nullptr;
    p.version_new = nullptr;
}

/* Owner of a PkgList under construction. If anything throws while the list is
 * being filled (bad_alloc growing the vector, ...) the strings strdup()ed so
 * far are released instead of leaked. On success the content is moved out by
 * release(), leaving the builder empty so the destructor frees nothing. */
class PkgListBuilder
{
public:
    PkgListBuilder() = default;
    PkgListBuilder(const PkgListBuilder &) = delete;
    PkgListBuilder &operator=(const PkgListBuilder &) = delete;

    ~PkgListBuilder()
    {
        for (Pkg &p : m_list)
            freePkg(p);
    }

    void add(const char *name, const char *current, const char *newest)
    {
        Pkg p{strdup(name != nullptr ? name : ""),
              strdup(current != nullptr ? current : ""),
              strdup(newest != nullptr ? newest : "")};

        try
        {
            m_list.push_back(p);
        }
        catch (...)
        {
            freePkg(p);
            throw;
        }
    }

    PkgList *release() { return new PkgList(std::move(m_list)); }

private:
    PkgList m_list;
};

} // namespace

int aptInit(char **errorMsg)
{
    if (errorMsg != nullptr)
        *errorMsg = nullptr;

    try
    {
        if (!pkgInitConfig(*_config))
        {
            setError(errorMsg, "failed to initialize the APT configuration");
            return 1;
        }

        if (!pkgInitSystem(*_config, _system))
        {
            setError(errorMsg, "failed to initialize the APT system");
            return 1;
        }

        _config->CndSet("APT::Get::AutomaticRemove::Kernels",
                        _config->FindB("APT::Get::AutomaticRemove", true));

        // Non-interactive dpkg: keep existing conffiles, never prompt
        _config->Set("Dpkg::Options::", "--force-confdef");
        _config->Set("Dpkg::Options::", "--force-confold");
        _config->Set("DPkg::Use-Pty", "false"); // dpkg output -> stdout/journald

        setenv("DEBIAN_FRONTEND", "noninteractive", 1);
        setenv("APT_LISTCHANGES_FRONTEND", "none", 1);
        setenv("LC_ALL", "C", 1); // reliable mapping of dpkg action strings
    }
    catch (const std::exception &e)
    {
        setError(errorMsg, (std::string("APT initialization failed: ") + e.what()).c_str());
        return 1;
    }
    catch (...)
    {
        setError(errorMsg, "APT initialization failed with an unknown exception");
        return 1;
    }

    return 0;
}

void *aptCacheGetArray(char **errorMsg)
{
    if (errorMsg != nullptr)
        *errorMsg = nullptr;

    try
    {
        // Stack-allocated on purpose: every failure path below must drain
        // _error (setError) BEFORE ~AptCacheFile() discards it, and RAII also
        // keeps the cache released if the loop below throws.
        AptCacheFile cache;

        if (cache.Open(true) == false)
        {
            setError(errorMsg, "unable to open the apt cache");
            return nullptr;
        }

        if (!cache.CheckDeps(false))
        {
            setError(errorMsg, "the package system is in an inconsistent state");
            return nullptr;
        }

        if (!cache.DistUpgrade())
        {
            setError(errorMsg, "unable to compute the distribution upgrade");
            return nullptr;
        }

        PkgListBuilder builder;

        pkgDepCache *depCache = cache.GetDepCache();

        for (pkgCache::PkgIterator pkg = cache.GetPkgCache()->PkgBegin(); !pkg.end(); ++pkg)
        {
            const pkgDepCache::StateCache &state = (*depCache)[pkg];

            if (pkg->CurrentVer != 0 && state.Upgradable() && state.CandidateVer != nullptr)
            {
                const char *current = pkg.CurVersion() == 0 ? "none" : pkg.CurVersion();
                const char *newest = pkg.VersionList().end() ? "none" : pkg.VersionList().VerStr();

                builder.add(pkg.Name(), current, newest);
            }
        }

        // A pending error here would mean the listing above is incomplete;
        // report it rather than returning a truncated list.
        if (_error->PendingError())
        {
            setError(errorMsg, "failed to read the apt cache");
            return nullptr;
        }

        return builder.release();
    }
    catch (const std::exception &e)
    {
        setError(errorMsg, (std::string("failed to read the apt cache: ") + e.what()).c_str());
        return nullptr;
    }
    catch (...)
    {
        setError(errorMsg, "failed to read the apt cache: unknown exception");
        return nullptr;
    }
}

int aptCacheArrayCount(void *arr)
{
    PkgList *plist = reinterpret_cast<PkgList *>(arr);
    if (!plist) return 0;

    const size_t count = plist->size();

    return count > (size_t)INT_MAX ? INT_MAX : (int)count;
}

Pkg *aptCacheArrayGet(void *arr, int idx)
{
    PkgList *plist = reinterpret_cast<PkgList *>(arr);
    if (!plist) return nullptr;

    if (idx < 0 || (size_t)idx >= plist->size())
       return nullptr;

    // operator[] and not at(): the bounds are already checked, and a throwing
    // accessor has no business on a boundary crossed by cgo.
    return &(*plist)[idx];
}

void aptCacheArrayFree(void *arr)
{
    PkgList *plist = reinterpret_cast<PkgList *>(arr);
    if (!plist) return;

    for (size_t i = 0;i < plist->size();i++)
        freePkg((*plist)[i]);

    delete plist;
}
