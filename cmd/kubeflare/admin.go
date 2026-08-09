package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	iamapplication "github.com/lanyulei/kubeflare/internal/module/iam/application"
	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	iamauthstate "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/authstate"
	iampostgres "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/postgres"
	iamredis "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/redis"
	"github.com/lanyulei/kubeflare/internal/platform/cache"
	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

type adminPasswordOptions struct {
	configPath    string
	create        bool
	passwordStdin bool
	resetMFA      bool
}

func init() {
	rootCmd.AddCommand(newAdminCommand())
}

func newAdminCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "admin",
		Short: "Manage the built-in administrator account",
	}
	command.AddCommand(newAdminResetPasswordCommand())
	return command
}

func newAdminResetPasswordCommand() *cobra.Command {
	options := adminPasswordOptions{}
	command := &cobra.Command{
		Use:   "reset-password",
		Short: "Initialize or reset the built-in administrator password",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runAdminResetPassword(command, options)
		},
	}

	flags := command.Flags()
	flags.StringVar(&options.configPath, "config", "", "path to the configuration file")
	flags.BoolVar(&options.create, "create", false, "create the admin user when it does not exist")
	flags.BoolVar(&options.passwordStdin, "password-stdin", false, "read one password line from standard input")
	flags.BoolVar(&options.resetMFA, "reset-mfa", false, "disable and clear MFA for the admin user")
	flags.String("database.dsn", "", "postgres dsn")
	flags.Bool("database.enabled", false, "enable postgres")
	flags.String("redis.address", configpkg.Default().Redis.Address, "redis address")
	flags.Bool("redis.enabled", false, "enable redis session revocation")
	return command
}

func runAdminResetPassword(command *cobra.Command, options adminPasswordOptions) (runErr error) {
	password, err := readAdminPassword(command, options.passwordStdin)
	if err != nil {
		return err
	}

	cfg, err := configpkg.LoadForAdmin(options.configPath, command.Flags())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gormDB, err := dbplatform.OpenPostgres(cfg.Database)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, dbplatform.Close(gormDB))
	}()

	redisClient, err := cache.NewRedisClient(cfg.Redis)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, cache.Close(redisClient))
	}()

	userRepository := iampostgres.NewUserRepository(gormDB, cfg.Database.QueryTimeout)
	databaseAuthState := iampostgres.NewAuthStateRepository(gormDB, cfg.Database.QueryTimeout)
	var tokenState middleware.TokenStateStore = databaseAuthState
	var securityState domain.SecurityStateStore = databaseAuthState
	if redisClient != nil {
		combinedState := iamauthstate.NewFailoverStore(iamredis.NewAuthStateStore(redisClient), databaseAuthState)
		tokenState = combinedState
		securityState = combinedState
	}

	credentialService := iamapplication.NewAdminCredentialService(
		userRepository,
		tokenState,
		securityState,
		cfg.Auth.RefreshTokenTTL,
	)
	result, err := credentialService.Reset(ctx, iamapplication.AdminCredentialRequest{
		Password:        password,
		CreateIfMissing: options.create,
		ResetMFA:        options.resetMFA,
	})
	if err != nil {
		return err
	}

	if result.Created {
		_, err = fmt.Fprintln(command.OutOrStdout(), "admin user initialized")
		return err
	}
	_, err = fmt.Fprintln(command.OutOrStdout(), "admin password reset; existing sessions revoked")
	return err
}

func readAdminPassword(command *cobra.Command, passwordStdin bool) (string, error) {
	if passwordStdin {
		return readPasswordLine(command.InOrStdin())
	}

	inputFile, ok := command.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(inputFile.Fd())) {
		return "", errors.New("interactive password input requires a terminal; use --password-stdin")
	}
	if _, err := fmt.Fprint(command.ErrOrStderr(), "New admin password: "); err != nil {
		return "", err
	}
	password, err := term.ReadPassword(int(inputFile.Fd()))
	if err != nil {
		return "", fmt.Errorf("read admin password: %w", err)
	}
	if _, err := fmt.Fprint(command.ErrOrStderr(), "\nConfirm admin password: "); err != nil {
		return "", err
	}
	confirmation, err := term.ReadPassword(int(inputFile.Fd()))
	if err != nil {
		return "", fmt.Errorf("confirm admin password: %w", err)
	}
	if _, err := fmt.Fprintln(command.ErrOrStderr()); err != nil {
		return "", err
	}
	if !bytes.Equal(password, confirmation) {
		return "", errors.New("password confirmation does not match")
	}
	return string(password), nil
}

func readPasswordLine(reader io.Reader) (string, error) {
	password, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read admin password from stdin: %w", err)
	}
	password = strings.TrimSuffix(password, "\n")
	password = strings.TrimSuffix(password, "\r")
	return password, nil
}
