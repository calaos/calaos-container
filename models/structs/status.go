package structs

const (
	PkgStatePending     = "pending"
	PkgStateDownloading = "downloading"
	PkgStateUnpacking   = "unpacking"
	PkgStateInstalling  = "installing"
	PkgStateDone        = "done"
	PkgStateFailed      = "failed"
)

type PkgStatus struct {
	Name    string `json:"name"`
	State   string `json:"state"`   // pending|downloading|unpacking|installing|done|failed
	Percent int    `json:"percent"` // 0-100
}

type Status struct {
	// existants, sémantique conservée (rétro-compat UIs)
	Status        string `json:"status"` // idle|upgrading|done|error
	CurrentPkg    string `json:"current_pkg,omitempty"`
	Progress      int    `json:"progress,omitempty"`       // = PkgIndex
	ProgressTotal int    `json:"progress_total,omitempty"` // = PkgCount
	// nouveaux
	TotalPercent   int         `json:"total_percent"`             // 0-100 global
	CurrentStep    string      `json:"current_step,omitempty"`    // download|unpack|configure|install
	CurrentPercent int         `json:"current_percent,omitempty"` // 0-100 paquet courant
	PkgIndex       int         `json:"pkg_index,omitempty"`       // 1-based
	PkgCount       int         `json:"pkg_count,omitempty"`
	Packages       []PkgStatus `json:"packages,omitempty"`
	Message        string      `json:"message,omitempty"`
	Error          string      `json:"error,omitempty"`
}
