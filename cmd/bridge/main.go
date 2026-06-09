// Command bridge polls an Eitaa channel and republishes routed messages. See .env.example for config.
package main

import (
	"fmt"
	"os"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/config"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/utils"
)

const defaultEnvPath = config.DefaultEnvPath

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
  bridge dump  [--env PATH]
  bridge run   [--env PATH]

dump   fetches the channel once and writes raw HTML + routed JSON
       under the storage directory. Shows which posts WOULD be
       published. No side effects on the target.
run    polls the channel on EITAA_BRIDGE_SOURCE_POLL_INTERVAL.
       For each new message, it classifies, filters by publishing
       rules, and publishes via the configured target. On the first
       run (empty seen-set) it walks Eitaa's ?before= pagination up to
       EITAA_BRIDGE_SOURCE_BACKFILL_MAX older messages.

flags:
  --env  path to .env file (default: .env, optional — missing is OK)

env:
  Every config value is an EITAA_BRIDGE_* environment variable. Real
  env vars always override the .env file. See .env.example for the
  full schema.

  EITAA_BRIDGE_LOG_LEVEL   debug|info|warn|error (default: info)
`)
}
