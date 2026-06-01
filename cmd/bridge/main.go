package main

import (
	"fmt"
	"os"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/utils"
)

const defaultConfigPath = config.DefaultPath

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := utils.NewLogger()
	switch os.Args[1] {
	case "dump":
		runDump(log, os.Args[2:])
	case "run":
		runRun(log, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bridge — read an Eitaa channel and republish it

usage:
  bridge dump  [--config PATH]
  bridge run   [--config PATH]

dump   fetches the channel once and writes raw HTML + routed JSON
       under the storage directory. Shows which posts WOULD be
       published. No side effects on the target.
run    polls the channel on source.poll_interval. For each new
       message, it classifies, filters by publishing rules, and
       publishes via the configured target. On the first run (empty
       seen-set) it walks Eitaa's ?before= pagination up to
       source.backfill_max older messages.

flags:
  --config  path to config file (default: config.yaml)

env:
  EITAA_BRIDGE_<NESTED_KEY>     override config values, e.g.
                                EITAA_BRIDGE_SOURCE_CHANNEL=othername
  EITAA_BRIDGE_LOG_LEVEL        debug|info|warn|error (default: info)

config:
  See config.yaml.example for the schema.
`)
}
