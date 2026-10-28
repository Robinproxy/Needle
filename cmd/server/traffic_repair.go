package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"time"
)

type repairTrafficArgs struct {
	target string
	since  int64
	apply  bool
}

func parseRepairTrafficArgs(args []string) (repairTrafficArgs, error) {
	var options repairTrafficArgs
	flags := flag.NewFlagSet("repair-traffic", flag.ContinueOnError)
	sinceArg := flags.String("since", "", "trusted RFC3339 start time when the faulty accumulator was known active")
	apply := flags.Bool("apply", false, "apply proven legacy corrections after backing up the database")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 1 {
		return options, fmt.Errorf("usage: needle-server -db <path> repair-traffic [--since RFC3339] [--apply] <id|hostname|all>")
	}
	var since int64
	if *sinceArg != "" {
		stamp, err := time.Parse(time.RFC3339, *sinceArg)
		if err != nil {
			return options, fmt.Errorf("--since must be an RFC3339 timestamp: %w", err)
		}
		since = stamp.Unix()
		if since <= 0 {
			return options, fmt.Errorf("--since must be after the Unix epoch")
		}
	}
	if *apply && since == 0 {
		return options, fmt.Errorf("--apply requires --since at a time the faulty accumulator was known active")
	}
	options.target, options.since, options.apply = flags.Arg(0), since, *apply
	if options.target == "" || options.target[0] == '-' {
		return options, fmt.Errorf("repair-traffic target must be an id, hostname, or all")
	}
	return options, nil
}

func runRepairTraffic(dbPath string, args []string) error {
	options, err := parseRepairTrafficArgs(args)
	if err != nil {
		return err
	}
	store, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	agents, err := store.GetAgents()
	if err != nil {
		return err
	}
	target := options.target
	selected := agents[:0]
	if target == "all" {
		selected = agents
	} else {
		if id, parseErr := strconv.ParseInt(target, 10, 64); parseErr == nil {
			for _, a := range agents {
				if a.ID == id {
					selected = append(selected, a)
					break
				}
			}
		}
		if len(selected) == 0 {
			for _, a := range agents {
				if a.Hostname == target {
					selected = append(selected, a)
					break
				}
			}
		}
	}

	if len(selected) == 0 {
		return fmt.Errorf("agent not found: %s", target)
	}
	if options.apply {
		backupPath := dbPath + ".traffic-backup-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".db"
		if err = store.BackupTo(backupPath); err != nil {
			return fmt.Errorf("backup failed; no traffic corrections applied: %w", err)
		}
		fmt.Printf("Database backup: %s\n", backupPath)
	} else {
		fmt.Println("Preview only: usage is unchanged. Use --since at a trusted faulty-accumulator start time; earlier anomalies may remain.")
	}
	for _, a := range selected {
		result, err := store.RepairTrafficUsage(a.ID, options.since, options.apply)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Hostname, err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", a.Hostname, data)
	}
	return nil
}
