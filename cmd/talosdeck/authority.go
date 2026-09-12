package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"talosdeck/internal/executionauthority"
)

func authorityCommand(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: talosdeck authority serve --state-dir ABSOLUTE_PATH")
	}
	fs := flag.NewFlagSet("authority serve", flag.ContinueOnError)
	dir := fs.String("state-dir", "", "independent persistent authority state")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" {
		return errors.New("authority state directory required")
	}
	return executionauthority.Serve(context.Background(), *dir, os.Stdin, os.Stdout)
}
