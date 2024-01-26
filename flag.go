package main

import (
	"sync"

	"github.com/spf13/pflag"
)

type flags struct {
	confPath string
}

var f flags
var fOnce sync.Once

func Flags() *flags {
	fOnce.Do(func() {
		pflag.CommandLine.StringVar(&f.confPath, "conf-path", "./config/config.yaml", "config file path")
		pflag.Parse()
	})
	return &f
}
