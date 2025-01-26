package main

import (
	"os"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Rules []struct {
		DomainPattern string `yaml:"domainPattern"`
		ForwardMethod string `yaml:"forwardMethod"`
	} `yaml:"rules"`
}

var DomainForwardMap []struct {
	DomainPattern string
	ForwardMethod string
}

func LoadConfig() Config {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		logrus.Errorf("failed to read config file: %v", err)
		return Config{}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		logrus.Errorf("failed to parse config: %v", err)
		return Config{}
	}
	logrus.Debug("加载的配置:")
	for _, rule := range cfg.Rules {
		logrus.Debugf("%v", rule)
	}
	logrus.Debug("配置加载完成")

	return cfg
}
