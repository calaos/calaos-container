#pragma once

#include <apt-pkg/cachefile.h>
#include <apt-pkg/pkgrecords.h>
#include <apt-pkg/progress.h>

class AptCacheFile : public pkgCacheFile
{
public:
    AptCacheFile();
    ~AptCacheFile();

    /**
      * Inits the package cache returning false if it can't open
      */
    bool Open(bool withLock = false);

    /**
      * Closes the package cache
      */
    void Close();

    /**
      * Build caches
      */
    bool BuildCaches(bool withLock = false);

    /**
      * This routine generates the caches and then opens the dependency cache
      * and verifies that the system is OK.
      * @param AllowBroken when true it will try to perform the installation
      * even if we have broken packages, when false it will try to fix
      * the current situation
      */
    bool CheckDeps(bool AllowBroken = false);

    /**
     * Mark Cache for dist-upgrade
     */
    bool DistUpgrade();
};