#include "apt-progress.h"

#include <apt-pkg/acquire-item.h>
#include <apt-pkg/acquire-worker.h>

#include <algorithm>
#include <iostream>

AptProgressState &AptProgressState::instance()
{
    static AptProgressState state;
    return state;
}

void aptSetProgressCallback(AptProgressCb cb)
{
    AptProgressState::instance().callback = cb;
}

void aptEmitProgress(const char *stage, const char *pkg,
                     int pkgPercent, int totalPercent, const char *msg)
{
    auto &st = AptProgressState::instance();

    if (totalPercent < 0)
        totalPercent = 0;
    else if (totalPercent > 100)
        totalPercent = 100;

    // Monotonicity guard: the global percentage never goes backward
    totalPercent = std::max(st.lastTotal, totalPercent);
    st.lastTotal = totalPercent;

    if (st.callback)
        st.callback(stage ? stage : "", pkg ? pkg : "",
                    pkgPercent, totalPercent, msg ? msg : "");
}

void aptEmitDownload(const char *stage, const char *pkg, int pkgPercent,
                     int downloadPercent, const char *msg)
{
    auto &st = AptProgressState::instance();

    if (downloadPercent < 0)
        downloadPercent = 0;
    else if (downloadPercent > 100)
        downloadPercent = 100;

    aptEmitProgress(stage, pkg, pkgPercent,
                    downloadPercent * st.downloadWeight / 100, msg);
}

void aptEmitInstall(const char *stage, const char *pkg, int pkgPercent,
                    int installPercent, const char *msg)
{
    auto &st = AptProgressState::instance();

    if (installPercent < 0)
        installPercent = 0;
    else if (installPercent > 100)
        installPercent = 100;

    aptEmitProgress(stage, pkg, pkgPercent,
                    st.downloadWeight +
                        installPercent * (100 - st.downloadWeight) / 100,
                    msg);
}

/*
 * CalaosAcquireStatus
 */

CalaosAcquireStatus::CalaosAcquireStatus()
{
    // Get extra Pulse() calls on item start/finish/error
    MorePulses = true;
}

bool CalaosAcquireStatus::MediaChange(std::string, std::string)
{
    // Headless daemon: never prompt for a media change
    return false;
}

void CalaosAcquireStatus::Fetch(pkgAcquire::ItemDesc &Itm)
{
    // Item starts downloading. For .deb archives ShortDesc is the package name.
    aptEmitDownload("download", Itm.ShortDesc.c_str(), 0,
                    static_cast<int>(Percent), Itm.Description.c_str());
}

void CalaosAcquireStatus::Done(pkgAcquire::ItemDesc &Itm)
{
    aptEmitDownload("download", Itm.ShortDesc.c_str(), 100,
                    static_cast<int>(Percent), "");
}

void CalaosAcquireStatus::Fail(pkgAcquire::ItemDesc &Itm)
{
    // Ignore non-failures (idle transitions, cancelation of finished items)
    if (Itm.Owner == nullptr ||
        Itm.Owner->Status == pkgAcquire::Item::StatIdle ||
        Itm.Owner->Status == pkgAcquire::Item::StatDone)
        return;

    // Item-level failure: it may be retried from another source, so this is
    // emitted as a "download" event carrying the error message; the fatal
    // "error" stage is emitted by the transaction itself if the whole fetch
    // fails.
    const std::string msg = "fetch failed: " + Itm.Owner->ErrorText;
    aptEmitDownload("download", Itm.ShortDesc.c_str(), -1,
                    static_cast<int>(Percent), msg.c_str());
}

bool CalaosAcquireStatus::Pulse(pkgAcquire *Owner)
{
    // Updates Percent: global download % weighted on bytes + item count
    pkgAcquireStatus::Pulse(Owner);

    bool emitted = false;
    for (pkgAcquire::Worker *w = Owner->WorkersBegin(); w != nullptr;
         w = Owner->WorkerStep(w))
    {
        if (w->CurrentItem == nullptr)
            continue;

        int pkgPct = -1;
        if (w->CurrentItem->TotalSize > 0)
            pkgPct = static_cast<int>(w->CurrentItem->CurrentSize * 100 /
                                      w->CurrentItem->TotalSize);

        aptEmitDownload("download", w->CurrentItem->ShortDesc.c_str(), pkgPct,
                        static_cast<int>(Percent), "");
        emitted = true;
    }

    if (!emitted)
        aptEmitDownload("download", "", -1, static_cast<int>(Percent), "");

    // Never cancel the download from here
    return true;
}

/*
 * CalaosInstallProgress
 */

bool CalaosInstallProgress::StatusChanged(std::string PackageName,
                                          unsigned int StepsDone,
                                          unsigned int TotalSteps,
                                          std::string HumanReadableAction)
{
    APT::Progress::PackageManager::StatusChanged(PackageName, StepsDone,
                                                 TotalSteps,
                                                 HumanReadableAction);

    const int installPct =
        TotalSteps > 0
            ? static_cast<int>(static_cast<unsigned long long>(StepsDone) * 100 /
                               TotalSteps)
            : 0;

    const auto startsWith = [&HumanReadableAction](const char *prefix)
    { return HumanReadableAction.rfind(prefix, 0) == 0; };

    // Action strings are stable because LC_ALL=C is forced in aptInit().
    const char *stage = "install";
    int pkgPct = -1;

    if (startsWith("Preparing to configure")) // must be tested BEFORE "Preparing"
    {
        stage = "configure";
        pkgPct = 60;
    }
    else if (startsWith("Preparing"))
    {
        stage = "unpack";
        pkgPct = 20;
    }
    else if (startsWith("Unpacking"))
    {
        stage = "unpack";
        pkgPct = 40;
    }
    else if (startsWith("Configuring"))
    {
        stage = "configure";
        pkgPct = 80;
    }
    else if (startsWith("Installed"))
    {
        stage = "done";
        pkgPct = 100;
    }
    // "Removing", "Running trigger", ... -> stage "install", pkgPct -1

    aptEmitInstall(stage, PackageName.c_str(), pkgPct, installPct,
                   HumanReadableAction.c_str());

    return true;
}

void CalaosInstallProgress::Error(std::string PackageName,
                                  unsigned int StepsDone,
                                  unsigned int TotalSteps,
                                  std::string ErrorMessage)
{
    const int installPct =
        TotalSteps > 0
            ? static_cast<int>(static_cast<unsigned long long>(StepsDone) * 100 /
                               TotalSteps)
            : 0;

    aptEmitInstall("error", PackageName.c_str(), -1, installPct,
                   ErrorMessage.c_str());
}

void CalaosInstallProgress::ConffilePrompt(std::string PackageName,
                                           unsigned int /*StepsDone*/,
                                           unsigned int /*TotalSteps*/,
                                           std::string ConfMessage)
{
    // confold/confdef are forced in aptInit(), dpkg answers by itself:
    // just log the event, never prompt.
    std::cout << "apt: conffile prompt for " << PackageName << ": "
              << ConfMessage << std::endl;
}
