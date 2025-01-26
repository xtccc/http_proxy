package main

import (
	"fmt"
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
		fmt.Printf("failed to read config file: %v", err)
		return Config{}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("failed to parse config: %v", err)
		return Config{}
	}
	n := len(cfg.Rules)
	logrus.Info("config 加载数量", n, "config 前后的值", cfg.Rules[0], cfg.Rules[1], cfg.Rules[2], cfg.Rules[n-1], cfg.Rules[n-2], cfg.Rules[n-3])
	return cfg
}
