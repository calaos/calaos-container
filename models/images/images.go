package images

type Image struct {
	Name          string `json:"name"`
	Source        string `json:"source"`
	Version       string `json:"version"`
	CurrentVerion string `json:"current_version"`
}

type ImageList struct {
	Images []Image `json:"images"`
}

type ImageMap map[string]Image
