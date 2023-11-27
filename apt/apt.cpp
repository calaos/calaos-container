#include "apt.h"
#include "apt-cache-file.h"

#include <vector>
#include <iostream>

#include <apt-pkg/init.h>
#include <apt-pkg/pkgsystem.h>

using namespace APT;
using namespace std;

typedef std::vector<Pkg> PkgList;

void aptInit()
{
    if (!pkgInitConfig(*_config))
    {
        //g_debug("ERROR initializing backend configuration");
    }

    if (!pkgInitSystem(*_config, _system))
    {
        //g_debug("ERROR initializing backend system");
    }

    _config->CndSet("APT::Get::AutomaticRemove::Kernels",
                    _config->FindB("APT::Get::AutomaticRemove", true));
}

void *aptCacheGetArray()
{
    auto cache = new AptCacheFile();

    cout << "AptCacheFile Open()" << endl;
    if (cache->Open(true) == false)
    {
        //Unable to get lock
        //TODO: return error
        delete cache;
        return nullptr;
    }

    /*
    _config->Set("Dpkg::Options::", "--force-confdef");
    _config->Set("Dpkg::Options::", "--force-confold");

    setenv("APT_LISTCHANGES_FRONTEND", "none", true);
    setenv("APT_LISTBUGS_FRONTEND", "none", true);
    */

    cout << "AptCacheFile CheckDeps()" << endl;
    if (!cache->CheckDeps(false))
    {
        //System is in inconsistent state
        //TODO: return error
        delete cache;
        return nullptr;
    }

    cout << "AptCacheFile DistUpgrade()" << endl;
    if (!cache->DistUpgrade())
    {
        //Failed to get updates
        //TODO: return error
        delete cache;
        return nullptr;
    }

    PkgList *plist = new PkgList();

    cout << "Listing pkgs" << endl;
    for (pkgCache::PkgIterator pkg = (*cache)->PkgBegin(); !pkg.end(); ++pkg)
    {
        const auto &state = (*cache)[pkg];

        if (pkg->CurrentVer != 0 && state.Upgradable() && state.CandidateVer != nullptr)
        {
            string current = string(pkg.CurVersion() == 0 ? "none" : pkg.CurVersion());
            string newest = string(pkg.VersionList().end() ? "none" : pkg.VersionList().VerStr());

            cout << "Append pkg " << pkg.Name() << endl;

            Pkg p{nullptr, nullptr, nullptr};
            p.name = strdup(pkg.Name());
            p.version_current = strdup(current.c_str());
            p.version_new = strdup(newest.c_str());

            plist->push_back(p);
        }
    }

    delete cache;

    return plist;
}

int aptCacheArrayCount(void *arr)
{
    PkgList *plist = reinterpret_cast<PkgList *>(arr);
    if (!plist) return 0;

    return plist->size();
}

Pkg *aptCacheArrayGet(void *arr, int idx)
{
    PkgList *plist = reinterpret_cast<PkgList *>(arr);
    if (!plist) return nullptr;

    if (idx < 0 || (size_t)idx > plist->size())
       return nullptr;

    return &plist->at(idx);
}

void aptCacheArrayFree(void *arr)
{
    PkgList *plist = reinterpret_cast<PkgList *>(arr);
    if (!plist) return;

    for (size_t i = 0;i < plist->size();i++)
    {
        free((*plist)[i].name);
        free((*plist)[i].version_current);
        free((*plist)[i].version_new);
    }

    delete plist;
}
