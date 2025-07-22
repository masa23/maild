package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type ObjectStorage struct {
	Endpoint  string `yaml:"Endpoint"`
	AccessKey string `yaml:"AccessKey"`
	SecretKey string `yaml:"SecretKey"`
	Bucket    string `yaml:"Bucket"`
	Region    string `yaml:"Region"`
}

type Config struct {
	Database      string        `yaml:"Database"`
	LogFile       string        `yaml:"LogFile"`
	ObjectStorage ObjectStorage `yaml:"ObjectStorage"`
}

func Load(path string) (*Config, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var conf Config
	if err := yaml.Unmarshal(buf, &conf); err != nil {
		return nil, err
	}

	return &conf, nil
}
