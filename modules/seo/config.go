package seo

const (
	DefaultMaxTemplateLength = 2000
	DefaultMaxResultLength   = 1000
)

type Config struct {
	DefaultTitleTemplate         string
	DefaultDescriptionTemplate   string
	DefaultKeywordsTemplate      string
	DefaultCanonicalTemplate     string
	DefaultRobotsIndex           *bool
	DefaultRobotsFollow          *bool
	DefaultOGTitleTemplate       string
	DefaultOGDescriptionTemplate string
	MaxTemplateLength            int
	MaxResultLength              int
}

type runtimeConfig struct {
	defaults          Settings
	maxTemplateLength int
	maxResultLength   int
}

func normalizeConfig(config Config) runtimeConfig {
	if config.DefaultTitleTemplate == "" {
		config.DefaultTitleTemplate = "{{ resource.title }}"
	}
	if config.DefaultDescriptionTemplate == "" {
		config.DefaultDescriptionTemplate = "{{ resource.annotation }}"
	}
	if config.MaxTemplateLength == 0 {
		config.MaxTemplateLength = DefaultMaxTemplateLength
	}
	if config.MaxResultLength == 0 {
		config.MaxResultLength = DefaultMaxResultLength
	}
	robotsIndex := true
	if config.DefaultRobotsIndex != nil {
		robotsIndex = *config.DefaultRobotsIndex
	}
	robotsFollow := true
	if config.DefaultRobotsFollow != nil {
		robotsFollow = *config.DefaultRobotsFollow
	}
	return runtimeConfig{
		defaults: Settings{
			TitleTemplate:         config.DefaultTitleTemplate,
			DescriptionTemplate:   config.DefaultDescriptionTemplate,
			KeywordsTemplate:      config.DefaultKeywordsTemplate,
			CanonicalTemplate:     config.DefaultCanonicalTemplate,
			RobotsIndex:           robotsIndex,
			RobotsFollow:          robotsFollow,
			OGTitleTemplate:       config.DefaultOGTitleTemplate,
			OGDescriptionTemplate: config.DefaultOGDescriptionTemplate,
		},
		maxTemplateLength: config.MaxTemplateLength,
		maxResultLength:   config.MaxResultLength,
	}
}
