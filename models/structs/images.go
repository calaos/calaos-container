package structs

type Image struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	CurrentVersion string `json:"current_version"`
}

type ImageMap map[string]Image
