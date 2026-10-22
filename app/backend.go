package app

import "github.com/calaos/calaos-container/models"

// Handlers reach the system only through these variables, so handler tests
// (legacy golden tests included) can stub apt, dpkg, systemctl and friends.
var (
	runCommand            = models.RunCommand
	checkUpdates          = models.CheckUpdates
	getNewVersions        = models.GetNewVersions
	getInstalledPackages  = models.GetInstalledCalaosPackages
	upgradeAll            = models.UpgradeAll
	upgradePackage        = models.Upgrade
	updateStatus          = models.UpdateStatus
	getSystemInfo         = models.GetSystemInfo
	getNetInterfaces      = models.GetAllNetInterfaces
	interfaceExists       = models.InterfaceExists
	configureNetInterface = models.ConfigureNetInterface
	startLoggedCommand    = models.StartLoggedCommand
	rebootGuard           = models.RebootGuard

	installLogFile      = INSTALL_LOG_FILE
	installExitCodeFile = INSTALL_EXIT_CODE_FILE
)
