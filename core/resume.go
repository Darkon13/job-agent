package core

// ResumeTarget is one addressable resume of a profile. Alias is the
// user-facing name from config; Primary marks the profile's default resume.
type ResumeTarget struct {
	ID      string `json:"id"`
	Alias   string `json:"alias,omitempty"`
	Title   string `json:"title,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}
