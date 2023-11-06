package structs

type Status struct {
	Status        string `json:"status"`
	CurrentPkg    string `json:"current_pkg,omitempty"`
	Progress      int    `json:"progress,omitempty"`
	ProgressTotal int    `json:"progress_total,omitempty"`
}
