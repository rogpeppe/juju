// Copyright 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package user

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/juju/cmd"
	"github.com/juju/errors"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/juju/names.v2"
	"gopkg.in/macaroon.v1"

	"github.com/juju/juju/cmd/juju/block"
	"github.com/juju/juju/cmd/modelcmd"
)

const userChangePasswordDoc = `
The user is, by default, the current user. The latter can be confirmed with
the ` + "`juju show-user`" + ` command.

A controller administrator can change the password for another user (on
that controller).

Examples:

    juju change-user-password
    juju change-user-password bob

See also: add-user

`

func NewChangePasswordCommand() cmd.Command {
	return modelcmd.WrapController(&changePasswordCommand{})
}

// changePasswordCommand changes the password for a user.
type changePasswordCommand struct {
	modelcmd.ControllerCommandBase
	api  ChangePasswordAPI
	User string
}

// Info implements Command.Info.
func (c *changePasswordCommand) Info() *cmd.Info {
	return &cmd.Info{
		Name:    "change-user-password",
		Purpose: "Changes the password for the current Juju user of a controller",
		Doc:     userChangePasswordDoc,
	}
}

// Init implements Command.Init.
func (c *changePasswordCommand) Init(args []string) error {
	var err error
	c.User, err = cmd.ZeroOrOneArgs(args)
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}

// ChangePasswordAPI defines the usermanager API methods that the change
// password command uses.
type ChangePasswordAPI interface {
	CreateLocalLoginMacaroon(names.UserTag) (*macaroon.Macaroon, error)
	SetPassword(username, password string) error
	Close() error
}

// Run implements Command.Run.
func (c *changePasswordCommand) Run(ctx *cmd.Context) error {
	if c.api == nil {
		api, err := c.NewUserManagerAPIClient()
		if err != nil {
			return errors.Trace(err)
		}
		c.api = api
		defer c.api.Close()
	}

	newPassword, err := readAndConfirmPassword(ctx)
	if err != nil {
		return errors.Trace(err)
	}

	controllerName := c.ControllerName()
	store := c.ClientStore()
	accountDetails, err := store.AccountDetails(controllerName)
	if err != nil && !errors.IsNotFound(err) {
		return errors.Trace(err)
	}
	var userTag names.UserTag
	if c.User != "" {
		if !names.IsValidUserName(c.User) {
			return errors.NotValidf("user name %q", c.User)
		}
		userTag = names.NewUserTag(c.User)
		if accountDetails != nil && userTag.Canonical() != accountDetails.User {
			// The account details don't correspond to the username
			// being changed, so we don't need to update the account
			// locally.
			accountDetails = nil
		}
	} else {
		if accountDetails == nil {
			return errors.Errorf("no current user to change")
		}
		if !names.IsValidUser(accountDetails.User) {
			return errors.Errorf("invalid user in account %q", accountDetails.User)
		}
		userTag = names.NewUserTag(accountDetails.User)
		if !userTag.IsLocal() {
			return errors.Errorf("cannot change password for external user %q", userTag)
		}
	}

	if accountDetails != nil && accountDetails.Macaroon == "" {
		// Generate a macaroon first to guard against I/O failures
		// occurring after the password has been changed, preventing
		// future logins.
		macaroon, err := c.api.CreateLocalLoginMacaroon(userTag)
		if err != nil {
			return errors.Trace(err)
		}
		accountDetails.Password = ""

		// TODO(axw) update jujuclient with code for marshalling
		// and unmarshalling macaroons as YAML.
		macaroonJSON, err := macaroon.MarshalJSON()
		if err != nil {
			return errors.Trace(err)
		}
		accountDetails.Macaroon = string(macaroonJSON)

		if err := store.UpdateAccount(controllerName, *accountDetails); err != nil {
			return errors.Annotate(err, "failed to update client credentials")
		}
	}

	if err := c.api.SetPassword(userTag.Canonical(), newPassword); err != nil {
		return block.ProcessBlockedError(err, block.BlockChange)
	}
	if accountDetails == nil {
		ctx.Infof("Password for %q has been updated.", c.User)
	} else {
		ctx.Infof("Your password has been updated.")
	}
	return nil
}

func readAndConfirmPassword(ctx *cmd.Context) (string, error) {
	// Don't add the carriage returns before readPassword, but add
	// them directly after the readPassword so any errors are output
	// on their own lines.
	//
	// TODO(axw) retry/loop on failure
	fmt.Fprint(ctx.Stderr, "password: ")
	password, err := readPassword(ctx.Stdin)
	fmt.Fprint(ctx.Stderr, "\n")
	if err != nil {
		return "", errors.Trace(err)
	}
	if password == "" {
		return "", errors.Errorf("you must enter a password")
	}

	fmt.Fprint(ctx.Stderr, "type password again: ")
	verify, err := readPassword(ctx.Stdin)
	fmt.Fprint(ctx.Stderr, "\n")
	if err != nil {
		return "", errors.Trace(err)
	}
	if password != verify {
		return "", errors.New("Passwords do not match")
	}
	return password, nil
}

func readPassword(stdin io.Reader) (string, error) {
	if f, ok := stdin.(*os.File); ok && terminal.IsTerminal(int(f.Fd())) {
		password, err := terminal.ReadPassword(int(f.Fd()))
		if err != nil {
			return "", errors.Trace(err)
		}
		return string(password), nil
	}
	return readLine(stdin)
}

func readLine(stdin io.Reader) (string, error) {
	// Read one byte at a time to avoid reading beyond the delimiter.
	line, err := bufio.NewReader(byteAtATimeReader{stdin}).ReadString('\n')
	if err != nil {
		return "", errors.Trace(err)
	}
	return line[:len(line)-1], nil
}

type byteAtATimeReader struct {
	io.Reader
}

func (r byteAtATimeReader) Read(out []byte) (int, error) {
	return r.Reader.Read(out[:1])
}
