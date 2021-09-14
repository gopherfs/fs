// Package msi provides authentication methods using Microsoft Service Identities.
package msi

import (
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/Azure/go-autorest/autorest/adal"
)

const defaultResc = "https://storage.azure.com/"

// AuthMethod represents an MSI authentication method for the Token() call.
type AuthMethod interface {
	authMethod()
	defaults() AuthMethod
}

// SystemAssigned implements AuthMethod when you wish to use a system MSI
// to authenticate to Blob storage.
type SystemAssigned struct {
	// Resource is the resource you will be accessing. If not set this defaults
	// to "https://storage.azure.com/".
	Resource string
}

func (s SystemAssigned) defaults() AuthMethod {
	if s.Resource == "" {
		s.Resource = defaultResc
	}
	return s
}

func (s SystemAssigned) authMethod() {}

// AppID implements AuthMethod when you wish to use a application MSI
// to authenticate to Blob storage.
type AppID struct {
	// ID is the application's managed system identity.
	ID string
	// Resource is the resource you will be accessing. If not set this defaults
	// to "https://storage.azure.com/".
	Resource string
}

func (a AppID) defaults() AuthMethod {
	if a.Resource == "" {
		a.Resource = defaultResc
	}
	return a
}

func (a AppID) authMethod() {}

// ResourceID implements AuthMethod when you wish to use a resource MSI
// to authenticate to Blob storage.
type ResourceID struct {
	// ID is the resource's managed system identity.
	ID string
	// Resource is the resource you will be accessing. If not set this defaults
	// to "https://storage.azure.com/".
	Resource string
}

func (r ResourceID) defaults() AuthMethod {
	if r.Resource == "" {
		r.Resource = defaultResc
	}
	return r
}

func (r ResourceID) authMethod() {}

// Token fetches an azblob.TokenCredential that can be used to access blob storage using MSI.
func Token(authMethod AuthMethod) (*azblob.TokenCredential, error) {
	if authMethod == nil {
		return nil, fmt.Errorf("msi.Token() cannot have a nil authMethod")
	}
	authMethod = authMethod.defaults()

	return getOAuthToken(authMethod)
}

func getOAuthToken(authMethod AuthMethod) (*azblob.TokenCredential, error) {
	spt, err := fetchMSIToken(authMethod)
	if err != nil {
		log.Fatal(err)
	}

	// Refresh obtains a fresh token
	err = spt.Refresh()
	if err != nil {
		log.Fatal(err)
	}

	tc := azblob.NewTokenCredential(spt.Token().AccessToken, func(tc azblob.TokenCredential) time.Duration {
		err := spt.Refresh()
		if err != nil {
			// something went wrong, prevent the refresher from being triggered again
			return 0
		}

		// set the new token value
		tc.SetToken(spt.Token().AccessToken)

		// get the next token slightly before the current one expires
		return time.Until(spt.Token().Expires()) - 10*time.Second
	})

	return &tc, nil
}

var callbacks = []adal.TokenRefreshCallback{func(token adal.Token) error { return nil }}

func fetchMSIToken(authMethod AuthMethod) (*adal.ServicePrincipalToken, error) {
	// msiEndpoint is the well known endpoint for getting MSI authentications tokens
	// msiEndpoint := "http://169.254.169.254/metadata/identity/oauth2/token" for production Jobs
	msiEndpoint, _ := adal.GetMSIVMEndpoint()

	var spt *adal.ServicePrincipalToken
	var err error

	switch auth := authMethod.(type) {
	case SystemAssigned:
		spt, err = adal.NewServicePrincipalTokenFromMSI(msiEndpoint, auth.Resource, callbacks...)
	case AppID:
		spt, err = adal.NewServicePrincipalTokenFromMSIWithUserAssignedID(msiEndpoint, auth.Resource, auth.ID, callbacks...)
	case ResourceID:
		spt, err = adal.NewServicePrincipalTokenFromMSIWithIdentityResourceID(msiEndpoint, auth.Resource, auth.ID, callbacks...)
	default:
		return nil, fmt.Errorf("bug: fetchMSIToken() had unknown authMethod(%T) which wasn't supported", authMethod)
	}

	if err != nil {
		return nil, err
	}

	return spt, spt.Refresh()
}
