package terminal

import (
	"bufio"
	"bus/app/internal/lib/logging"
	"bus/app/internal/storage/repository"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

var errChangeUserPassword = errors.New("error change user password")

type Terminal struct {
	store  repository.Repositorer
	logger logging.Logger
}

func New(store repository.Repositorer, logger logging.Logger) *Terminal {
	return &Terminal{
		store:  store,
		logger: logger,
	}
}

func (t Terminal) ChangeUserPassword(ctx context.Context) error {

	fmt.Println(".........Change user password.........")
	fmt.Print("Enter username: ")

	reader := bufio.NewReader(os.Stdin)
	username, err := reader.ReadString('\n')
	if err != nil {
		t.logger.Info("error read username:",
			t.logger.Err(err))
		return errChangeUserPassword
	}
	username = strings.TrimSpace(username)

	fmt.Print("Enter password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		t.logger.Info("error read password",
			t.logger.Err(err))
		return errChangeUserPassword
	}
	password := string(passwordBytes)
	fmt.Println()

	err = t.store.ChangeUserPassword(ctx, username, password)
	if err != nil {
		return errChangeUserPassword
	}

	fmt.Println(".........Password changed.........")
	return nil
}
