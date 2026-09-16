package seo

import (
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type Metadata struct {
	ResourceID            resource.ID
	SiteID                site.ID
	TitleTemplate         string
	DescriptionTemplate   string
	KeywordsTemplate      string
	CanonicalTemplate     string
	RobotsIndex           bool
	RobotsFollow          bool
	OGTitleTemplate       string
	OGDescriptionTemplate string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CreatedBy             *security.UserID
	UpdatedBy             *security.UserID
}

type Settings struct {
	TitleTemplate         string `json:"title_template"`
	DescriptionTemplate   string `json:"description_template"`
	KeywordsTemplate      string `json:"keywords_template"`
	CanonicalTemplate     string `json:"canonical_template"`
	RobotsIndex           bool   `json:"robots_index"`
	RobotsFollow          bool   `json:"robots_follow"`
	OGTitleTemplate       string `json:"og_title_template"`
	OGDescriptionTemplate string `json:"og_description_template"`
}

func settingsFromMetadata(metadata Metadata) Settings {
	return Settings{
		TitleTemplate:         metadata.TitleTemplate,
		DescriptionTemplate:   metadata.DescriptionTemplate,
		KeywordsTemplate:      metadata.KeywordsTemplate,
		CanonicalTemplate:     metadata.CanonicalTemplate,
		RobotsIndex:           metadata.RobotsIndex,
		RobotsFollow:          metadata.RobotsFollow,
		OGTitleTemplate:       metadata.OGTitleTemplate,
		OGDescriptionTemplate: metadata.OGDescriptionTemplate,
	}
}
