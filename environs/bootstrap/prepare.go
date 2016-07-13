// Copyright 2011, 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package bootstrap

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/juju/errors"

	"github.com/juju/juju/cert"
	"github.com/juju/juju/cloud"
	"github.com/juju/juju/controller"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/jujuclient"
)

// ControllerModelName is the name of the admin model in each controller.
const ControllerModelName = "controller"

// PrepareParams contains the parameters for preparing a controller Environ
// for bootstrapping.
type PrepareParams struct {
	// BaseConfig contains the base configuration for the controller model.
	//
	// This includes the model name, cloud type, and any user-supplied
	// configuration. It does not include any default attributes.
	BaseConfig map[string]interface{}

	// ControllerConfig is the configuration of the controller being prepared.
	ControllerConfig controller.Config

	// ControllerName is the name of the controller being prepared.
	ControllerName string

	// CloudName is the name of the cloud that the controller is being
	// prepared for.
	CloudName string

	// CloudRegion is the name of the region of the cloud to create
	// the Juju controller in. This will be empty for clouds without
	// regions.
	CloudRegion string

	// CloudEndpoint is the location of the primary API endpoint to
	// use when communicating with the cloud.
	CloudEndpoint string

	// CloudStorageEndpoint is the location of the API endpoint to use
	// when communicating with the cloud's storage service. This will
	// be empty for clouds that have no cloud-specific API endpoint.
	CloudStorageEndpoint string

	// Credential is the credential to use to bootstrap.
	Credential cloud.Credential

	// CredentialName is the name of the credential to use to bootstrap.
	// This will be empty for auto-detected credentials.
	CredentialName string
}

// Prepare prepares a new controller based on the provided configuration.
// It is an error to prepare a controller if there already exists an
// entry in the client store with the same name.
//
// Upon success, Prepare will update the ClientStore with the details of
// the controller, admin account, and admin model.
func Prepare(
	ctx environs.BootstrapContext,
	store jujuclient.ClientStore,
	args PrepareParams,
) (environs.Environ, error) {

	_, err := store.ControllerByName(args.ControllerName)
	if err == nil {
		return nil, errors.AlreadyExistsf("controller %q", args.ControllerName)
	} else if !errors.IsNotFound(err) {
		return nil, errors.Annotatef(err, "error reading controller %q info", args.ControllerName)
	}

	cloudType, ok := args.BaseConfig["type"].(string)
	if !ok {
		return nil, errors.NotFoundf("cloud type in base configuration")
	}

	p, err := environs.Provider(cloudType)
	if err != nil {
		return nil, errors.Trace(err)
	}

	env, details, err := prepare(ctx, p, args)
	if err != nil {
		return nil, errors.Trace(err)
	}

	if err := decorateAndWriteInfo(
		store, details, args.ControllerName, env.Config().Name(),
	); err != nil {
		return nil, errors.Annotatef(err, "cannot create controller %q info", args.ControllerName)
	}
	return env, nil
}

// decorateAndWriteInfo decorates the info struct with information
// from the given cfg, and the writes that out to the filesystem.
func decorateAndWriteInfo(
	store jujuclient.ClientStore,
	details prepareDetails,
	controllerName, modelName string,
) error {
	if err := store.UpdateController(controllerName, details.ControllerDetails); err != nil {
		return errors.Trace(err)
	}
	if err := store.UpdateBootstrapConfig(controllerName, details.BootstrapConfig); err != nil {
		return errors.Trace(err)
	}
	if err := store.UpdateAccount(controllerName, details.AccountDetails); err != nil {
		return errors.Trace(err)
	}
	if err := store.UpdateModel(controllerName, modelName, details.ModelDetails); err != nil {
		return errors.Trace(err)
	}
	if err := store.SetCurrentModel(controllerName, modelName); err != nil {
		return errors.Trace(err)
	}
	return nil
}

func prepare(
	ctx environs.BootstrapContext,
	p environs.EnvironProvider,
	args PrepareParams,
) (environs.Environ, prepareDetails, error) {
	var details prepareDetails

	cfg, err := config.New(config.UseDefaults, args.BaseConfig)
	if err != nil {
		return nil, details, errors.Trace(err)
	}
	// TODO(wallyworld) - we need to separate controller and model schemas
	// Config schema is still defined together for model and controller,
	// hence any defaults are filled in when we construct a new model config above.
	// Extract any controller specific defaults.
	for attr, value := range cfg.AllAttrs() {
		if controller.ControllerOnlyAttribute(attr) {
			if _, ok := args.ControllerConfig[attr]; !ok {
				args.ControllerConfig[attr] = value
			}
		}
	}
	// Remove any remaining controller attributes from the env config.
	cfg, err = cfg.Remove(controller.ControllerOnlyConfigAttributes)
	if err != nil {
		return nil, details, errors.Annotate(err, "cannot remove controller attributes")
	}

	cfg, adminSecret, err := ensureAdminSecret(cfg)
	if err != nil {
		return nil, details, errors.Annotate(err, "cannot generate admin-secret")
	}
	caCert, err := ensureCertificate(cfg.Name(), args.ControllerConfig)
	if err != nil {
		return nil, details, errors.Annotate(err, "cannot ensure CA certificate")
	}

	cfg, err = p.BootstrapConfig(environs.BootstrapConfigParams{
		args.ControllerConfig.ControllerUUID(), cfg, args.Credential, args.CloudRegion,
		args.CloudEndpoint, args.CloudStorageEndpoint,
	})
	if err != nil {
		return nil, details, errors.Trace(err)
	}
	env, err := p.PrepareForBootstrap(ctx, cfg)
	if err != nil {
		return nil, details, errors.Trace(err)
	}

	// We store the base configuration only; we don't want the
	// default attributes, generated secrets/certificates, or
	// UUIDs stored in the bootstrap config. Make a copy, so
	// we don't disturb the caller's config map.
	details.Config = make(map[string]interface{})
	for k, v := range args.BaseConfig {
		details.Config[k] = v
	}
	delete(details.Config, config.UUIDKey)

	details.CACert = caCert
	details.ControllerUUID = args.ControllerConfig.ControllerUUID()
	details.User = environs.AdminUser
	details.Password = adminSecret
	details.ModelUUID = cfg.UUID()
	details.ControllerDetails.Cloud = args.CloudName
	details.ControllerDetails.CloudRegion = args.CloudRegion
	details.BootstrapConfig.Cloud = args.CloudName
	details.BootstrapConfig.CloudRegion = args.CloudRegion
	details.CloudEndpoint = args.CloudEndpoint
	details.CloudStorageEndpoint = args.CloudStorageEndpoint
	details.Credential = args.CredentialName

	return env, details, nil
}

type prepareDetails struct {
	jujuclient.ControllerDetails
	jujuclient.BootstrapConfig
	jujuclient.AccountDetails
	jujuclient.ModelDetails
}

// ensureAdminSecret returns a config with a non-empty admin-secret.
func ensureAdminSecret(cfg *config.Config) (*config.Config, string, error) {
	if secret := cfg.AdminSecret(); secret != "" {
		return cfg, secret, nil
	}

	// Generate a random string.
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return nil, "", errors.Annotate(err, "generating random secret")
	}
	secret := fmt.Sprintf("%x", buf)

	cfg, err := cfg.Apply(map[string]interface{}{"admin-secret": secret})
	if err != nil {
		return nil, "", errors.Trace(err)
	}
	return cfg, secret, nil
}

// ensureCertificate generates a new CA certificate and
// attaches it to the given controller configuration,
// unless the configuration already has one.
func ensureCertificate(name string, controllerCfg controller.Config) (string, error) {
	caCert, hasCACert := controllerCfg.CACert()
	_, hasCAKey := controllerCfg.CAPrivateKey()
	if hasCACert && hasCAKey {
		return caCert, nil
	}
	if hasCACert && !hasCAKey {
		return "", errors.Errorf("controller configuration with a certificate but no CA private key")
	}

	// TODO(perrito666) 2016-05-02 lp:1558657
	caCert, caKey, err := cert.NewCA(name, controllerCfg.ControllerUUID(), time.Now().UTC().AddDate(10, 0, 0))
	if err != nil {
		return "", errors.Trace(err)
	}
	controllerCfg[controller.CACertKey] = string(caCert)
	controllerCfg[controller.CAPrivateKey] = string(caKey)
	if err != nil {
		return "", errors.Trace(err)
	}
	return string(caCert), nil
}
