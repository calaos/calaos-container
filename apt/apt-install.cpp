#include "apt.h"
#include "apt-cache-file.h"
#include "apt-progress.h"

#include <apt-pkg/acquire.h>
#include <apt-pkg/acquire-item.h>
#include <apt-pkg/algorithms.h>
#include <apt-pkg/configuration.h>
#include <apt-pkg/depcache.h>
#include <apt-pkg/error.h>
#include <apt-pkg/packagemanager.h>
#include <apt-pkg/pkgrecords.h>
#include <apt-pkg/pkgsystem.h>
#include <apt-pkg/sourcelist.h>

#include <cstring>
#include <memory>
#include <string>

/* Flush the libapt error stack into *errorMsg (strdup, caller frees),
 * emit a final "error" progress event and return 1. */
static int fail(char **errorMsg)
{
    std::string all;
    std::string msg;
    while (_error->PopMessage(msg))
    {
        if (!all.empty())
            all += "\n";
        all += msg;
    }

    if (all.empty())
        all = "unknown APT error";

    if (errorMsg != nullptr)
        *errorMsg = strdup(all.c_str());

    // total=0 here: the monotonic guard in aptEmitProgress keeps the last
    // global percentage reached by the transaction.
    aptEmitProgress("error", "", -1, 0, all.c_str());

    return 1;
}

/* Commit the transaction currently marked in the dep cache:
 * announce it (stage=pending), fetch the archives (CalaosAcquireStatus),
 * then run dpkg through pkgPackageManager (CalaosInstallProgress). */
static int doCommit(AptCacheFile &cache, char **errorMsg)
{
    pkgDepCache *depCache = cache.GetDepCache();

    // 1. Announce the exact transaction before anything starts
    for (pkgCache::PkgIterator pkg = cache.GetPkgCache()->PkgBegin();
         !pkg.end(); ++pkg)
    {
        const pkgDepCache::StateCache &state = (*depCache)[pkg];

        if (state.NewInstall() || state.Upgrade() || state.Downgrade())
        {
            const char *cand = state.CandVersion != nullptr ? state.CandVersion : "";
            aptEmitProgress("pending", pkg.Name(), -1, 0, cand);
        }
        else if (state.Delete())
        {
            aptEmitProgress("pending", pkg.Name(), -1, 0, "remove");
        }
    }

    // 2. Set up the fetcher and the package manager
    pkgRecords recs(*cache.GetPkgCache());
    if (_error->PendingError())
        return fail(errorMsg);

    CalaosAcquireStatus acqStatus;
    pkgAcquire fetcher(&acqStatus);
    if (!fetcher.GetLock(_config->FindDir("Dir::Cache::Archives")))
        return fail(errorMsg);

    std::unique_ptr<pkgPackageManager> pm(_system->CreatePM(depCache));
    if (!pm->GetArchives(&fetcher, cache.GetSourceList(), &recs))
        return fail(errorMsg);

    // Fixed 40% download / 60% install global window; if everything is
    // already local the install phase covers 0 -> 100.
    if (fetcher.FetchNeeded() == 0)
        AptProgressState::instance().downloadWeight = 0;

    // 3. Download the archives (Pulse every 0.5s)
    fetcher.Run(500000);

    bool fetchFailed = false;
    for (pkgAcquire::ItemIterator it = fetcher.ItemsBegin();
         it != fetcher.ItemsEnd(); ++it)
    {
        if ((*it)->Status == pkgAcquire::Item::StatDone && (*it)->Complete)
            continue;

        fetchFailed = true;
        _error->Error("Failed to fetch %s: %s", (*it)->DescURI().c_str(),
                      (*it)->ErrorText.c_str());
    }

    if (fetchFailed || _error->PendingError())
        return fail(errorMsg);

    // Close the download window (global % reaches downloadWeight)
    aptEmitDownload("download", "", -1, 100, "");

    // 4. Install with dpkg.
    //
    // AptCacheFile::Open(withLock=true) took the inner dpkg lock
    // (/var/lib/dpkg/lock) in addition to the frontend lock. dpkg, spawned by
    // DoInstall, needs that inner lock itself, so we release it here (keeping
    // the outer/frontend lock so no other APT instance can start meanwhile).
    // dpkg re-acquires the inner lock; we take it back afterwards.
    _system->UnLockInner();

    CalaosInstallProgress instProgress;
    const pkgPackageManager::OrderResult res = pm->DoInstall(&instProgress);

    _system->LockInner();

    if (res == pkgPackageManager::Failed || _error->PendingError())
        return fail(errorMsg);

    if (res == pkgPackageManager::Incomplete)
    {
        _error->Error("Transaction incomplete: a media change was required");
        return fail(errorMsg);
    }

    // 5. All done
    aptEmitProgress("done", "", 100, 100, "");

    return 0;
}

int aptUpgradeAll(char **errorMsg)
{
    if (errorMsg != nullptr)
        *errorMsg = nullptr;

    AptProgressState::instance().resetTransaction();

    // Open with lock: dpkg + archives locks are kept for the whole
    // transaction (wanted). ~AptCacheFile() does Close() + _error->Discard().
    AptCacheFile cache;
    if (!cache.Open(true))
        return fail(errorMsg);

    if (!cache.CheckDeps(false))
        return fail(errorMsg);

    if (!cache.DistUpgrade())
        return fail(errorMsg);

    pkgDepCache *depCache = cache.GetDepCache();
    if (depCache->InstCount() + depCache->DelCount() == 0)
    {
        aptEmitProgress("done", "", 100, 100, "nothing to upgrade");
        return 0;
    }

    return doCommit(cache, errorMsg);
}

int aptInstallPackage(const char *pkgName, char **errorMsg)
{
    if (errorMsg != nullptr)
        *errorMsg = nullptr;

    AptProgressState::instance().resetTransaction();

    if (pkgName == nullptr || *pkgName == '\0')
    {
        _error->Error("No package name given");
        return fail(errorMsg);
    }

    AptCacheFile cache;
    if (!cache.Open(true))
        return fail(errorMsg);

    if (!cache.CheckDeps(false))
        return fail(errorMsg);

    pkgDepCache *depCache = cache.GetDepCache();

    pkgCache::PkgIterator P = cache.GetPkgCache()->FindPkg(pkgName);
    if (P.end())
    {
        _error->Error("Package %s not found", pkgName);
        return fail(errorMsg);
    }

    // A package known to the cache may still have no installable version
    // (pure virtual package, or all versions pinned away). Without this check
    // MarkInstall() would be a silent no-op and the transaction would be
    // reported as "already installed".
    if ((*depCache)[P].CandidateVer == nullptr && P->CurrentVer == 0)
    {
        _error->Error("Package %s has no installation candidate", pkgName);
        return fail(errorMsg);
    }

    if (P->SelectedState == pkgCache::State::Hold)
    {
        _error->Error("Package %s is held", pkgName);
        return fail(errorMsg);
    }

    depCache->MarkInstall(P, /*AutoInst*/ true, /*Depth*/ 0, /*FromUser*/ true);

    pkgProblemResolver fix(depCache);
    fix.Protect(P);
    fix.Resolve(true);

    if (depCache->BrokenCount() != 0)
    {
        _error->Error("Unable to resolve dependencies for %s", pkgName);
        return fail(errorMsg);
    }

    if (depCache->InstCount() + depCache->DelCount() == 0)
    {
        aptEmitProgress("done", "", 100, 100, "already installed");
        return 0;
    }

    return doCommit(cache, errorMsg);
}
