package main

import (
	"context"
	"errors"
	"io"
	"os"

	"akasha/internal/ilang"
)

func ilangFile[T any](path string) (T, error) {
	var zero T
	if path == "" {
		return zero, errors.New("--file UTF-8 JSON required")
	}
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return zero, err
	}
	return ilang.Decode[T](b)
}
func runIlang(ctx context.Context, s ilang.Service, command, name, id, path string) error {
	switch command {
	case "ilang-enable", "ilang-disable":
		enabled := command == "ilang-enable"
		if err := s.Configure(ctx, enabled); err != nil {
			return err
		}
		return output(map[string]any{"enabled": enabled, "project": s.Project, "local_only": true, "mcp": "reload project MCP to refresh its tool list"})
	case "ilang-status":
		enabled, err := s.Enabled(ctx)
		if err != nil {
			return err
		}
		return output(map[string]any{"enabled": enabled, "project": s.Project, "format": ilang.Format})
	case "ilang-list":
		v, err := s.Catalog(ctx, name)
		if err != nil {
			return err
		}
		return output(v)
	case "ilang-define":
		d, err := ilangFile[ilang.Definition](path)
		if err != nil {
			return err
		}
		v, err := s.Define(ctx, d)
		if err != nil {
			return err
		}
		return output(v)
	case "ilang-prepare":
		r, err := ilangFile[ilang.Request](path)
		if err != nil {
			return err
		}
		v, err := s.Start(ctx, r)
		if err != nil {
			return err
		}
		return output(v)
	case "ilang-answer":
		a, err := ilangFile[ilang.Answers](path)
		if err != nil {
			return err
		}
		v, err := s.Answer(ctx, id, a)
		if err != nil {
			return err
		}
		return output(v)
	case "ilang-read":
		v, err := s.Read(ctx, id)
		if err != nil {
			return err
		}
		return output(v)
	case "ilang-report":
		r, err := ilangFile[ilang.Report](path)
		if err != nil {
			return err
		}
		v, err := s.Assess(ctx, id, r)
		if err != nil {
			return err
		}
		return output(v)
	case "ilang-capture":
		v, err := s.Capture(ctx, id, name)
		if err != nil {
			return err
		}
		return output(v)
	}
	return errors.New("unknown ilang command")
}
