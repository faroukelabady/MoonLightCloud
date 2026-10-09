package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/app"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
)

// authCmd is the server-side human-account administration (ADR-0053).
// It requires direct server/database access by design: there is no public
// HTTP bootstrap or password-reset endpoint.
//
//	moonlight-cloud auth bootstrap-owner --login L --display-name N [--store ID]... [--password-stdin|--password-file F]
//	moonlight-cloud auth reset-password  --login L [--reset-mfa] [--password-stdin|--password-file F]
//
// Passwords are read from the terminal without echo, from stdin, or from
// a file, never from command-line arguments, and are never printed.
func authCmd(args []string) error {
	usage := "usage: moonlight-cloud auth bootstrap-owner|reset-password [flags]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	login := fs.String("login", "", "login identifier (email or username)")
	display := fs.String("display-name", "", "display name (bootstrap-owner)")
	var stores multiFlag
	fs.Var(&stores, "store", "Store membership (repeatable); omit for access to every Store")
	fromStdin := fs.Bool("password-stdin", false, "read the password from stdin")
	fromFile := fs.String("password-file", "", "read the password from a file (mode 0600 recommended)")
	resetMFA := fs.Bool("reset-mfa", false, "also clear MFA so the user re-enrolls at next login (reset-password)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc, err := app.NewHumanAuthService(cfg, pool, nil)
	if err != nil {
		return err
	}
	password, err := readSecret(*fromStdin, *fromFile)
	if err != nil {
		return err
	}
	switch args[0] {
	case "bootstrap-owner":
		owner, err := svc.BootstrapOwner(ctx, *login, *display, password, stores)
		if err != nil {
			return authCLIError(err)
		}
		scope := "every Store"
		if !owner.AllStores {
			scope = strings.Join(owner.Stores, ", ")
		}
		fmt.Printf("OWNER %s created (access: %s). Sign in at /dashboard and enroll MFA.\n", owner.Login, scope)
		return nil
	case "reset-password":
		user, err := svc.ResetPassword(ctx, *login, password, *resetMFA)
		if err != nil {
			return authCLIError(err)
		}
		fmt.Printf("password reset for %s; all sessions revoked%s.\n", user.Login,
			map[bool]string{true: "; MFA cleared (re-enroll at next login)", false: ""}[*resetMFA])
		return nil
	default:
		return errors.New(usage)
	}
}

func authCLIError(err error) error {
	switch {
	case errors.Is(err, humanauth.ErrAlreadyBootstrapped):
		return errors.New("refused: an OWNER has already been bootstrapped (use reset-password for recovery)")
	case errors.Is(err, humanauth.ErrWeakPassword):
		return fmt.Errorf("refused: password must be %d-%d characters", humanauth.MinPasswordRunes, humanauth.MaxPasswordRunes)
	case errors.Is(err, humanauth.ErrInvalidLogin):
		return errors.New("refused: invalid login identifier")
	case errors.Is(err, humanauth.ErrNotFound):
		return errors.New("refused: no such account")
	case errors.Is(err, humanauth.ErrInvalidInput):
		return errors.New("refused: invalid display name or unknown Store")
	}
	return err
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// readSecret reads the password from a file, from stdin, or interactively
// from the terminal with echo disabled (entered twice).
func readSecret(fromStdin bool, file string) (string, error) {
	switch {
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	case fromStdin:
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, humanauth.MaxPasswordBytes+2)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	first, err := readNoEcho("Password: ")
	if err != nil {
		return "", fmt.Errorf("interactive password entry needs a terminal (or use --password-stdin / --password-file): %w", err)
	}
	second, err := readNoEcho("Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("passwords do not match")
	}
	return first, nil
}
