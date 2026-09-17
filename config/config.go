package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

// Load fills a configuration struct from environment variables. Prefix may be
// empty. T must be a non-pointer struct type.
func Load[T any](prefix string) (*T, error) {
	var config T

	if err := envconfig.Process(prefix, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// LoadDotEnv loads environment variables from the named dotenv files, then
// fills a configuration struct. When filenames is empty, it reads .env from
// the current working directory. Existing process environment values take
// precedence. Missing dotenv files are optional.
func LoadDotEnv[T any](prefix string, filenames ...string) (*T, error) {
	if err := godotenv.Load(filenames...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load dotenv: %w", err)
	}

	return Load[T](prefix)
}
