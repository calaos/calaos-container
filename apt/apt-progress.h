#pragma once

#include <apt-pkg/acquire.h>
#include <apt-pkg/install-progress.h>

#include <string>

#include "apt.h"

/* Shared state for one apt transaction (download + install phases).
 *
 * The global progress window is split in two fixed parts:
 *   [0 .. downloadWeight]   -> download phase (default 40%)
 *   [downloadWeight .. 100] -> install phase (60%)
 * If nothing needs to be fetched, downloadWeight is set to 0 and the
 * install phase covers 0 -> 100.
 *
 * lastTotal implements the monotonicity guard applied by aptEmitProgress():
 * the emitted global percentage never goes backward within a transaction.
 */
struct AptProgressState
{
    AptProgressCb callback = nullptr;
    int downloadWeight = 40;
    int lastTotal = 0;

    void resetTransaction()
    {
        downloadWeight = 40;
        lastTotal = 0;
    }

    static AptProgressState &instance();
};

/* Scale a download-phase-local percentage (0-100) into the global window
 * ([0..downloadWeight]) and emit. */
void aptEmitDownload(const char *stage, const char *pkg, int pkgPercent,
                     int downloadPercent, const char *msg);

/* Scale an install-phase-local percentage (0-100) into the global window
 * ([downloadWeight..100]) and emit. */
void aptEmitInstall(const char *stage, const char *pkg, int pkgPercent,
                    int installPercent, const char *msg);

/* Download progress monitor: forwards pkgAcquire activity as "download"
 * events (global % from pkgAcquireStatus::Pulse weighted bytes+items,
 * per-item % from the worker's CurrentSize/TotalSize). */
class CalaosAcquireStatus : public pkgAcquireStatus
{
public:
    CalaosAcquireStatus();

    bool MediaChange(std::string Media, std::string Drive) override;
    void Fetch(pkgAcquire::ItemDesc &Itm) override;
    void Done(pkgAcquire::ItemDesc &Itm) override;
    void Fail(pkgAcquire::ItemDesc &Itm) override;
    bool Pulse(pkgAcquire *Owner) override;
};

/* Install progress monitor: maps dpkg status lines (LC_ALL=C forced in
 * aptInit) to stage/per-package progress. Do NOT override fork()/StartDpkg:
 * the pty/child handling is done by pkgDPkgPM itself. */
class CalaosInstallProgress : public APT::Progress::PackageManager
{
public:
    bool StatusChanged(std::string PackageName, unsigned int StepsDone,
                       unsigned int TotalSteps,
                       std::string HumanReadableAction) override;
    void Error(std::string PackageName, unsigned int StepsDone,
               unsigned int TotalSteps, std::string ErrorMessage) override;
    void ConffilePrompt(std::string PackageName, unsigned int StepsDone,
                        unsigned int TotalSteps, std::string ConfMessage) override;
};
