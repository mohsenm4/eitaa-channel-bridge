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
	case "seed":
		runSeed(log, os.Args[2:])
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
  bridge seed  [--env PATH]
  bridge run   [--env PATH]

dump   fetches the channel once and writes raw HTML + routed JSON
       under the storage directory. Shows which posts WOULD be
       published. No side effects on the target.
seed   marks every currently-visible message on the channel as
       already-seen, WITHOUT publishing. Use on a fresh install (or
       after deleting seen.json) when you want the bridge to ignore
       everything posted before now. BACKFILL=0 alone does NOT do
       this — the first tick still treats the visible page as new.
run    polls the channel on POLL_COLD (slow) and POLL_HOT (fast,
       while a recent message is still inside EDIT_WINDOW). For each
       new message, it classifies, filters by publishing rules, and
       publishes via the configured target. On the first run (empty
       seen-set) it walks Eitaa's ?before= pagination up to BACKFILL
       older messages.

flags:
  --env  path to .env file (default: .env, optional — missing is OK)

env:
  Real env vars always override values in .env. See .env.example for
  the full list with defaults and inline docs.

  LOG_LEVEL   debug|info|warn|error (default: info)
`)
}
