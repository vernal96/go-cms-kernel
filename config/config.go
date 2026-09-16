package config

import "github.com/kelseyhightower/envconfig"

// Load fills a configuration struct from environment variables. Prefix may be
// empty. T must be a non-pointer struct type.
func Load[T any](prefix string) (*T, error) {
	var config T

	if err := envconfig.Process(prefix, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
