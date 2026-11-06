// Package config holds the few values needed before the database exists.
// Everything else the app can be tuned with is a setting (package settings),
// edited on the Settings pages.
package config

import "flag"

type Config struct {
	Addr    string // listen address
	DataDir string // database, secret key and guide files
	EPGDir  string // iptv-org/epg checkout bundled in the image
}

// Load reads the command line. There are no environment variables.
func Load() Config {
	var c Config
	flag.StringVar(&c.Addr, "listen", ":8810", "address to listen on")
	flag.StringVar(&c.DataDir, "data", "/data", "directory for the database and generated files")
	flag.StringVar(&c.EPGDir, "epg", "/opt/epg", "checkout of iptv-org/epg (a missing directory turns the guide off)")
	flag.Parse()
	return c
}
