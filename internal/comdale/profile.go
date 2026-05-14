package comdale

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type BusinessProfile struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Audience         []string `json:"audience"`
	Offers           []string `json:"offers"`
	Voice            string   `json:"voice"`
	ApprovalRequired bool     `json:"approval_required"`
	RepoPaths        []string `json:"repo_paths"`
}

func LoadProfile(path string) (BusinessProfile, error) {
	if path == "" {
		path = os.Getenv("COMDALE_PROFILE")
	}
	if path == "" {
		path = filepath.Join("profiles", "example-business.json")
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && path == filepath.Join("profiles", "example-business.json") {
		return DefaultProfile(), nil
	}
	if err != nil {
		return BusinessProfile{}, err
	}
	var profile BusinessProfile
	if err := json.Unmarshal(b, &profile); err != nil {
		return BusinessProfile{}, err
	}
	if profile.Name == "" {
		return BusinessProfile{}, errors.New("profile name is required")
	}
	return profile, nil
}

func DefaultProfile() BusinessProfile {
	return BusinessProfile{
		Name:        "Example Business",
		Description: "A small organization using local AI to improve operations without giving up control of its data.",
		Audience: []string{
			"small businesses that need practical AI automation",
			"SaaS founders that need productized software",
			"teams that want privacy-conscious local AI systems",
		},
		Offers: []string{
			"AI and machine learning solutions",
			"SaaS development",
			"custom software development",
			"self-hosted automation and local AI workflows",
		},
		Voice:            "direct, technically credible, privacy-conscious, practical",
		ApprovalRequired: true,
		RepoPaths:        []string{"."},
	}
}
