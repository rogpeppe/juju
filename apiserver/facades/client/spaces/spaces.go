// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package spaces

import (
	"github.com/juju/errors"

	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/apiserver/common/networkingcommon"
	"github.com/juju/juju/apiserver/facade"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/permission"
)

// API defines the methods the Spaces API facade implements.
type API interface {
	CreateSpaces(params.CreateSpacesParams) (params.ErrorResults, error)
	ListSpaces() (params.ListSpacesResults, error)
	ReloadSpaces() error
}

// APIV2 is missing ReloadSpaces method
type APIV2 interface {
	CreateSpaces(params.CreateSpacesParams) (params.ErrorResults, error)
	ListSpaces() (params.ListSpacesResults, error)
}

// spacesAPI implements the API interface.
type spacesAPI struct {
	backing          networkingcommon.NetworkBacking
	resources        facade.Resources
	authorizer       facade.Authorizer
	providerRegistry *environs.ProviderRegistry
}

// NewAPI creates a new Space API server-side facade with a
// state.State backing.
func NewAPI(ctx facade.Context) (API, error) {
	st := ctx.State()
	stateShim, err := networkingcommon.NewStateShim(st)
	if err != nil {
		return nil, errors.Trace(err)
	}
	providerRegistry := ctx.ProviderRegistry()
	return newAPIWithBacking(stateShim, ctx.Resources(), ctx.Auth(), providerRegistry)
}

// newAPIWithBacking creates a new server-side Spaces API facade with
// the given Backing.
func newAPIWithBacking(backing networkingcommon.NetworkBacking, resources facade.Resources, authorizer facade.Authorizer, providerRegistry *environs.ProviderRegistry) (API, error) {
	// Only clients can access the Spaces facade.
	if !authorizer.AuthClient() {
		return nil, common.ErrPerm
	}
	return &spacesAPI{
		backing:          backing,
		resources:        resources,
		authorizer:       authorizer,
		providerRegistry: providerRegistry,
	}, nil
}

// NewAPIV2 is a wrapper that creates a V2 spaces API.
func NewAPIV2(ctx facade.Context) (APIV2, error) {
	return NewAPI(ctx)
}

// CreateSpaces creates a new Juju network space, associating the
// specified subnets with it (optional; can be empty).
func (api *spacesAPI) CreateSpaces(args params.CreateSpacesParams) (results params.ErrorResults, err error) {
	isAdmin, err := api.authorizer.HasPermission(permission.AdminAccess, api.backing.ModelTag())
	if err != nil && !errors.IsNotFound(err) {
		return results, errors.Trace(err)
	}
	if !isAdmin {
		return results, common.ServerError(common.ErrPerm)
	}

	return networkingcommon.CreateSpaces(api.backing, api.providerRegistry, args)
}

// ListSpaces lists all the available spaces and their associated subnets.
func (api *spacesAPI) ListSpaces() (results params.ListSpacesResults, err error) {
	canRead, err := api.authorizer.HasPermission(permission.ReadAccess, api.backing.ModelTag())
	if err != nil && !errors.IsNotFound(err) {
		return results, errors.Trace(err)
	}
	if !canRead {
		return results, common.ServerError(common.ErrPerm)
	}

	err = networkingcommon.SupportsSpaces(api.backing, api.providerRegistry)
	if err != nil {
		return results, common.ServerError(errors.Trace(err))
	}

	spaces, err := api.backing.AllSpaces()
	if err != nil {
		return results, errors.Trace(err)
	}

	results.Results = make([]params.Space, len(spaces))
	for i, space := range spaces {
		result := params.Space{}
		result.Name = space.Name()

		subnets, err := space.Subnets()
		if err != nil {
			err = errors.Annotatef(err, "fetching subnets")
			result.Error = common.ServerError(err)
			results.Results[i] = result
			continue
		}

		result.Subnets = make([]params.Subnet, len(subnets))
		for i, subnet := range subnets {
			result.Subnets[i] = networkingcommon.BackingSubnetToParamsSubnet(subnet)
		}
		results.Results[i] = result
	}
	return results, nil
}

// RefreshSpaces refreshes spaces from substrate
func (api *spacesAPI) ReloadSpaces() error {
	canWrite, err := api.authorizer.HasPermission(permission.WriteAccess, api.backing.ModelTag())
	if err != nil && !errors.IsNotFound(err) {
		return errors.Trace(err)
	}
	if !canWrite {
		return common.ServerError(common.ErrPerm)
	}
	env, err := environs.GetEnviron(api.backing, api.providerRegistry.NewEnviron)
	if err != nil {
		return errors.Trace(err)
	}
	return errors.Trace(api.backing.ReloadSpaces(env))
}
