package structs

type Image struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	CurrentVerion string `json:"current_version"`
}

type ImageList struct {
	Images []Image `json:"images"`
}

type ImageMap map[string]Image
