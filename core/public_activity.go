package core

// PublicActivity is an allowlisted execution receipt, separate from raw tool
// arguments/results and model reasoning. Adapters supply it only from typed
// runtime events, never by interpreting command text or user intent.
type PublicActivity struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	URL    string `json:"url,omitempty"`
}
