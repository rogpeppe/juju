// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// Package jujuclient provides functionality to support
// connections to Juju such as controllers cache, accounts cache, etc.

package jujuclient

import (
	"time"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	"github.com/juju/mutex"
	"github.com/juju/utils/clock"

	"github.com/juju/juju/cloud"
)

var _ ClientStore = (*store)(nil)

var logger = loggo.GetLogger("juju.jujuclient")

// A second should be enough to write or read any files. But
// some disks are slow when under load, so lets give the disk a
// reasonable time to get the lock.
var lockTimeout = 5 * time.Second

// NewFileClientStore returns a new filesystem-based client store
// that manages files in $XDG_DATA_HOME/juju.
func NewFileClientStore() ClientStore {
	return &store{}
}

// NewFileCredentialStore returns a new filesystem-based credentials store
// that manages credentials in $XDG_DATA_HOME/juju.
func NewFileCredentialStore() CredentialStore {
	return &store{}
}

type store struct{}

func (s *store) acquireLock() (mutex.Releaser, error) {
	const lockName = "store-lock"
	spec := mutex.Spec{
		Name:    lockName,
		Clock:   clock.WallClock,
		Delay:   20 * time.Millisecond,
		Timeout: lockTimeout,
	}
	releaser, err := mutex.Acquire(spec)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return releaser, nil
}

// AllControllers implements ControllersGetter.
func (s *store) AllControllers() (map[string]ControllerDetails, error) {
	releaser, err := s.acquireLock()
	if err != nil {
		return nil, errors.Annotate(err, "cannot read all controllers")
	}
	defer releaser.Release()
	controllers, err := ReadControllersFile(JujuControllersPath())
	if err != nil {
		return nil, errors.Trace(err)
	}
	return controllers.Controllers, nil
}

// CurrentController implements ControllersGetter.
func (s *store) CurrentController() (string, error) {
	releaser, err := s.acquireLock()
	if err != nil {
		return "", errors.Annotate(err, "cannot get current controller name")
	}
	defer releaser.Release()
	controllers, err := ReadControllersFile(JujuControllersPath())
	if err != nil {
		return "", errors.Trace(err)
	}
	if controllers.CurrentController == "" {
		return "", errors.NotFoundf("current controller")
	}
	return controllers.CurrentController, nil
}

// ControllerByName implements ControllersGetter.
func (s *store) ControllerByName(name string) (*ControllerDetails, error) {
	if err := ValidateControllerName(name); err != nil {
		return nil, errors.Trace(err)
	}

	releaser, err := s.acquireLock()
	if err != nil {
		return nil, errors.Annotatef(err, "cannot read controller %v", name)
	}
	defer releaser.Release()

	controllers, err := ReadControllersFile(JujuControllersPath())
	if err != nil {
		return nil, errors.Trace(err)
	}
	if result, ok := controllers.Controllers[name]; ok {
		return &result, nil
	}
	return nil, errors.NotFoundf("controller %s", name)
}

// UpdateController implements ControllersUpdater.
func (s *store) UpdateController(name string, details ControllerDetails) error {
	if err := ValidateControllerName(name); err != nil {
		return errors.Trace(err)
	}
	if err := ValidateControllerDetails(details); err != nil {
		return errors.Trace(err)
	}

	releaser, err := s.acquireLock()
	if err != nil {
		return errors.Annotatef(err, "cannot update controller %v", name)
	}
	defer releaser.Release()

	all, err := ReadControllersFile(JujuControllersPath())
	if err != nil {
		return errors.Annotate(err, "cannot get controllers")
	}

	if len(all.Controllers) == 0 {
		all.Controllers = make(map[string]ControllerDetails)
	}

	all.Controllers[name] = details
	return WriteControllersFile(all)
}

// SetCurrentController implements ControllersUpdater.
func (s *store) SetCurrentController(name string) error {
	if err := ValidateControllerName(name); err != nil {
		return errors.Trace(err)
	}

	releaser, err := s.acquireLock()
	if err != nil {
		return errors.Annotate(err, "cannot set current controller name")
	}
	defer releaser.Release()

	controllers, err := ReadControllersFile(JujuControllersPath())
	if err != nil {
		return errors.Trace(err)
	}
	if _, ok := controllers.Controllers[name]; !ok {
		return errors.NotFoundf("controller %v", name)
	}
	if controllers.CurrentController == name {
		return nil
	}
	controllers.CurrentController = name
	return WriteControllersFile(controllers)
}

// RemoveController implements ControllersRemover
func (s *store) RemoveController(name string) error {
	if err := ValidateControllerName(name); err != nil {
		return errors.Trace(err)
	}

	releaser, err := s.acquireLock()
	if err != nil {
		return errors.Annotatef(err, "cannot remove controller %v", name)
	}
	defer releaser.Release()

	controllers, err := ReadControllersFile(JujuControllersPath())
	if err != nil {
		return errors.Annotate(err, "cannot get controllers")
	}

	// We remove all controllers with the same UUID as the named one.
	namedControllerDetails, ok := controllers.Controllers[name]
	if !ok {
		return nil
	}
	var names []string
	for name, details := range controllers.Controllers {
		if details.ControllerUUID == namedControllerDetails.ControllerUUID {
			names = append(names, name)
			delete(controllers.Controllers, name)
			if controllers.CurrentController == name {
				controllers.CurrentController = ""
			}
		}
	}

	// Remove models for the controller.
	controllerModels, err := ReadModelsFile(JujuModelsPath())
	if err != nil {
		return errors.Trace(err)
	}
	for _, name := range names {
		if _, ok := controllerModels[name]; ok {
			delete(controllerModels, name)
			if err := WriteModelsFile(controllerModels); err != nil {
				return errors.Trace(err)
			}
		}
	}

	// Remove accounts for the controller.
	controllerAccounts, err := ReadAccountsFile(JujuAccountsPath())
	if err != nil {
		return errors.Trace(err)
	}
	for _, name := range names {
		if _, ok := controllerAccounts[name]; ok {
			delete(controllerAccounts, name)
			if err := WriteAccountsFile(controllerAccounts); err != nil {
				return errors.Trace(err)
			}
		}
	}

	// Remove bootstrap config for the controller.
	bootstrapConfigurations, err := ReadBootstrapConfigFile(JujuBootstrapConfigPath())
	if err != nil {
		return errors.Trace(err)
	}
	for _, name := range names {
		if _, ok := bootstrapConfigurations[name]; ok {
			delete(bootstrapConfigurations, name)
			if err := WriteBootstrapConfigFile(bootstrapConfigurations); err != nil {
				return errors.Trace(err)
			}
		}
	}

	// Finally, remove the controllers. This must be done last
	// so we don't end up with dangling entries in other files.
	return WriteControllersFile(controllers)
}

// UpdateModel implements ModelUpdater.
func (s *store) UpdateModel(controllerName, modelName string, details ModelDetails) error {
	panic("not implemented")
}

// SetCurrentModel implements ModelUpdater.
func (s *store) SetCurrentModel(controllerName, modelName string) error {
	panic("not implemented")
}

// AllModels implements ModelGetter.
func (s *store) AllModels(controllerName string) (map[string]ModelDetails, error) {
	panic("not implemented")
}

// CurrentModel implements ModelGetter.
func (s *store) CurrentModel(controllerName string) (string, error) {
	panic("not implemented")
}

// ModelByName implements ModelGetter.
func (s *store) ModelByName(controllerName, modelName string) (*ModelDetails, error) {
	panic("not implemented")
}

// RemoveModel implements ModelRemover.
func (s *store) RemoveModel(controllerName, modelName string) error {
	panic("not implemented")
}

func updateAccountModels(
	controllerName, accountName string,
	update func(*AccountModels) (bool, error),
) error {
	all, err := ReadModelsFile(JujuModelsPath())
	if err != nil {
		return errors.Trace(err)
	}
	if all == nil {
		all = make(map[string]ControllerAccountModels)
	}
	controllerAccountModels, ok := all[controllerName]
	if !ok {
		controllerAccountModels = ControllerAccountModels{
			make(map[string]*AccountModels),
		}
		all[controllerName] = controllerAccountModels
	}
	accountModels, ok := controllerAccountModels.AccountModels[accountName]
	if !ok {
		accountModels = &AccountModels{
			Models: make(map[string]ModelDetails),
		}
		controllerAccountModels.AccountModels[accountName] = accountModels
	}
	updated, err := update(accountModels)
	if err != nil {
		return errors.Trace(err)
	}
	if updated {
		return errors.Trace(WriteModelsFile(all))
	}
	return nil
}

// UpdateAccount implements AccountUpdater.
func (s *store) UpdateAccount(controllerName string, details AccountDetails) error {
	/*
		if err := ValidateControllerName(controllerName); err != nil {
			return errors.Trace(err)
		}
		if err := ValidateAccountName(accountName); err != nil {
			return errors.Trace(err)
		}
		if err := ValidateAccountDetails(details); err != nil {
			return errors.Trace(err)
		}

		releaser, err := s.acquireLock()
		if err != nil {
			return errors.Trace(err)
		}
		defer releaser.Release()

		controllerAccounts, err := ReadAccountsFile(JujuAccountsPath())
		if err != nil {
			return errors.Trace(err)
		}
		if controllerAccounts == nil {
			controllerAccounts = make(map[string]*ControllerAccounts)
		}
		accounts, ok := controllerAccounts[controllerName]
		if !ok {
			accounts = &ControllerAccounts{
				Accounts: make(map[string]AccountDetails),
			}
			controllerAccounts[controllerName] = accounts
		}
		if oldDetails, ok := accounts.Accounts[accountName]; ok && details == oldDetails {
			return nil
		}

		// NOTE(axw) it is currently not valid for a client to have multiple
		// logins for a controller. We may relax this in the future, but for
		// now we are strict.
		if len(accounts.Accounts) > 0 {
			if _, ok := accounts.Accounts[accountName]; !ok {
				return errors.AlreadyExistsf(
					"alternative account for controller %s",
					controllerName,
				)
			}
		}

		accounts.Accounts[accountName] = details
		return errors.Trace(WriteAccountsFile(controllerAccounts))
	*/
	panic("not implemented")
}

// AccountByName implements AccountGetter.
func (s *store) AccountDetails(controllerName string) (*AccountDetails, error) {
	/*
		if err := ValidateControllerName(controllerName); err != nil {
			return nil, errors.Trace(err)
		}
		if err := ValidateAccountName(accountName); err != nil {
			return nil, errors.Trace(err)
		}

		releaser, err := s.acquireLock()
		if err != nil {
			return nil, errors.Trace(err)
		}
		defer releaser.Release()

		controllerAccounts, err := ReadAccountsFile(JujuAccountsPath())
		if err != nil {
			return nil, errors.Trace(err)
		}
		accounts, ok := controllerAccounts[controllerName]
		if !ok {
			return nil, errors.NotFoundf("controller %s", controllerName)
		}
		details, ok := accounts.Accounts[accountName]
		if !ok {
			return nil, errors.NotFoundf("account %s:%s", controllerName, accountName)
		}
		return &details, nil
	*/
	panic("not implemented")
}

// RemoveAccount implements AccountRemover.
func (s *store) RemoveAccount(controllerName string) error {
	/*
		if err := ValidateControllerName(controllerName); err != nil {
			return errors.Trace(err)
		}
		if err := ValidateAccountName(accountName); err != nil {
			return errors.Trace(err)
		}

		releaser, err := s.acquireLock()
		if err != nil {
			return errors.Trace(err)
		}
		defer releaser.Release()

		controllerAccounts, err := ReadAccountsFile(JujuAccountsPath())
		if err != nil {
			return errors.Trace(err)
		}
		accounts, ok := controllerAccounts[controllerName]
		if !ok {
			return errors.NotFoundf("controller %s", controllerName)
		}
		if _, ok := accounts.Accounts[accountName]; !ok {
			return errors.NotFoundf("account %s:%s", controllerName, accountName)
		}

		delete(accounts.Accounts, accountName)
		if accounts.CurrentAccount == accountName {
			accounts.CurrentAccount = ""
		}
		return errors.Trace(WriteAccountsFile(controllerAccounts))
	*/
	panic("not implemented")
}

// UpdateCredential implements CredentialUpdater.
func (s *store) UpdateCredential(cloudName string, details cloud.CloudCredential) error {
	releaser, err := s.acquireLock()
	if err != nil {
		return errors.Annotatef(err, "cannot update credentials for %v", cloudName)
	}
	defer releaser.Release()

	all, err := ReadCredentialsFile(JujuCredentialsPath())
	if err != nil {
		return errors.Annotate(err, "cannot get credentials")
	}

	if len(all) == 0 {
		all = make(map[string]cloud.CloudCredential)
	}

	// Clear the default credential if we are removing that one.
	if existing, ok := all[cloudName]; ok && existing.DefaultCredential != "" {
		stillHaveDefault := false
		for name := range details.AuthCredentials {
			if name == existing.DefaultCredential {
				stillHaveDefault = true
				break
			}
		}
		if !stillHaveDefault {
			details.DefaultCredential = ""
		}
	}

	all[cloudName] = details
	return WriteCredentialsFile(all)
}

// CredentialForCloud implements CredentialGetter.
func (s *store) CredentialForCloud(cloudName string) (*cloud.CloudCredential, error) {
	cloudCredentials, err := s.AllCredentials()
	if err != nil {
		return nil, errors.Trace(err)
	}
	credentials, ok := cloudCredentials[cloudName]
	if !ok {
		return nil, errors.NotFoundf("credentials for cloud %s", cloudName)
	}
	return &credentials, nil
}

// AllCredentials implements CredentialGetter.
func (s *store) AllCredentials() (map[string]cloud.CloudCredential, error) {
	cloudCredentials, err := ReadCredentialsFile(JujuCredentialsPath())
	if err != nil {
		return nil, errors.Trace(err)
	}
	return cloudCredentials, nil
}

// UpdateBootstrapConfig implements BootstrapConfigUpdater.
func (s *store) UpdateBootstrapConfig(controllerName string, cfg BootstrapConfig) error {
	if err := ValidateControllerName(controllerName); err != nil {
		return errors.Trace(err)
	}
	if err := ValidateBootstrapConfig(cfg); err != nil {
		return errors.Trace(err)
	}

	releaser, err := s.acquireLock()
	if err != nil {
		return errors.Annotatef(err, "cannot update bootstrap config for controller %s", controllerName)
	}
	defer releaser.Release()

	all, err := ReadBootstrapConfigFile(JujuBootstrapConfigPath())
	if err != nil {
		return errors.Annotate(err, "cannot get bootstrap config")
	}

	if all == nil {
		all = make(map[string]BootstrapConfig)
	}
	all[controllerName] = cfg
	return WriteBootstrapConfigFile(all)
}

// BootstrapConfigForController implements BootstrapConfigGetter.
func (s *store) BootstrapConfigForController(controllerName string) (*BootstrapConfig, error) {
	configs, err := ReadBootstrapConfigFile(JujuBootstrapConfigPath())
	if err != nil {
		return nil, errors.Trace(err)
	}
	cfg, ok := configs[controllerName]
	if !ok {
		return nil, errors.NotFoundf("bootstrap config for controller %s", controllerName)
	}
	return &cfg, nil
}
