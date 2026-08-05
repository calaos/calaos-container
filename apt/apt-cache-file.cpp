#include "apt-cache-file.h"

#include <apt-pkg/algorithms.h>
#include <apt-pkg/error.h>
#include <apt-pkg/progress.h>
#include <apt-pkg/upgrade.h>

using namespace APT;

AptCacheFile::AptCacheFile()
{
}

AptCacheFile::~AptCacheFile()
{
    Close();
}

bool AptCacheFile::Open(bool withLock)
{
    //OpPackageKitProgress progress(m_job);
    return pkgCacheFile::Open(/*&progress*/ nullptr, withLock);
}

void AptCacheFile::Close()
{
    pkgCacheFile::Close();

    // Discard all errors to avoid a future failure when opening
    // the package cache: _error is a process-global stack and a leftover
    // pending error makes every subsequent Open() fail, which matters for a
    // long-lived daemon doing one check after another.
    //
    // ORDERING CONTRACT: this destroys the very messages describing why an
    // operation failed. Callers that report errors across the C ABI must drain
    // _error (setError() in apt.cpp, fail() in apt-install.cpp) BEFORE letting
    // the AptCacheFile go out of scope.
    _error->Discard();
}

bool AptCacheFile::BuildCaches(bool withLock)
{
    //OpPackageKitProgress progress(m_job);
    return pkgCacheFile::BuildCaches(/*&progress*/ nullptr, withLock);
}

bool AptCacheFile::CheckDeps(bool AllowBroken)
{
    if (_error->PendingError() == true) {
        return false;
    }

    // Check that the system is OK
    if (DCache->DelCount() != 0 || DCache->InstCount() != 0) {
        _error->Error("Internal error, non-zero counts");
        //show_errors(m_job, PK_ERROR_ENUM_INTERNAL_ERROR);

        return false;
    }

    // Apply corrections for half-installed packages
    if (pkgApplyStatus(*DCache) == false) {
        _error->Error("Unable to apply corrections for half-installed packages");;
        //show_errors(m_job, PK_ERROR_ENUM_INTERNAL_ERROR);
        return false;
    }

    // Nothing is broken or we don't want to try fixing it
    if (DCache->BrokenCount() == 0 || AllowBroken == true) {
        return true;
    }

    // Attempt to fix broken things
    if (pkgFixBroken(*DCache) == false || DCache->BrokenCount() != 0) {
        // We failed to fix the cache
        //ShowBroken(true, PK_ERROR_ENUM_UNFINISHED_TRANSACTION);

        //g_warning("Unable to correct dependencies");
        return false;
    }

    if (pkgMinimizeUpgrade(*DCache) == false) {
        //g_warning("Unable to minimize the upgrade set");
        //show_errors(m_job, PK_ERROR_ENUM_INTERNAL_ERROR);
        return false;
    }

    // Fixing the cache is DONE no errors were found
    return true;
}

bool AptCacheFile::DistUpgrade()
{
    //OpPackageKitProgress progress(m_job);
    return Upgrade::Upgrade(*this, Upgrade::ALLOW_EVERYTHING, nullptr /*&progress*/);
}
