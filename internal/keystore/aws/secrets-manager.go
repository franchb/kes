// Copyright 2019 - MinIO, Inc. All rights reserved.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/smithy-go"
	smithyendpoints "github.com/aws/smithy-go/endpoints"
	"github.com/minio/kes"
	xhttp "github.com/minio/kes/internal/http"
	"github.com/minio/kes/internal/keystore"
	kesdk "github.com/minio/kms-go/kes"
)

// Credentials represents static AWS credentials:
// access key, secret key and a session token
type Credentials struct {
	AccessKey    string // The AWS access key
	SecretKey    string // The AWS secret key
	SessionToken string // The AWS session token
}

// Config is a structure containing configuration
// options for connecting to the AWS SecretsManager.
type Config struct {
	// Addr is the HTTP address of the AWS Secret
	// Manager. In general, the address has the
	// following form:
	//  secretsmanager.<region>.amazonaws.com
	Addr string

	// Region is the AWS region. Even though the Addr
	// endpoint contains that information already, this
	// field is mandatory.
	Region string

	// The KMSKeyID is the AWS-KMS key ID specifying the
	// AWS-KMS key that is used to encrypt (and decrypt) the
	// values stored at AWS Secrets Manager.
	KMSKeyID string

	// Login contains the AWS credentials (access/secret key).
	Login Credentials
}

// Connect establishes and returns a Conn to a AWS SecretManager
// using the given config.
func Connect(ctx context.Context, cfg *Config) (*Store, error) {
	// Configure AWS SDK v2 with custom options
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
	}

	// Configure credentials
	if cfg.Login.AccessKey != "" || cfg.Login.SecretKey != "" || cfg.Login.SessionToken != "" {
		// Use static credentials if any credential is provided
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.Login.AccessKey,
				cfg.Login.SecretKey,
				cfg.Login.SessionToken,
			),
		))
	}
	// If no credentials are provided, the SDK will automatically try:
	//  - Environment Variables
	//  - Shared Credentials file
	//  - EC2 Instance Metadata
	// In particular, when running a kes server on an EC2 instance, the SDK will
	// automatically fetch the temp. credentials from the EC2 metadata service.
	// See: AWS IAM roles for EC2 instances.

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}

	// Create Secrets Manager client with additional options if needed
	var clientOpts []func(*secretsmanager.Options)

	// If a custom endpoint was specified, configure it using EndpointResolverV2
	if cfg.Addr != "" {
		clientOpts = append(clientOpts, func(o *secretsmanager.Options) {
			o.EndpointResolverV2 = &customEndpointResolver{
				endpoint: "https://" + cfg.Addr,
			}
		})
	}

	c := &Store{
		config: *cfg,
		client: secretsmanager.NewFromConfig(awsCfg, clientOpts...),
	}

	if _, err = c.Status(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// customEndpointResolver implements the EndpointResolverV2 interface for Secrets Manager
type customEndpointResolver struct {
	endpoint string
}

func (r *customEndpointResolver) ResolveEndpoint(ctx context.Context,
	params secretsmanager.EndpointParameters,
) (smithyendpoints.Endpoint, error) {
	return secretsmanager.NewDefaultEndpointResolverV2().ResolveEndpoint(ctx, params)
}

// Store is an AWS SecretsManager secret store.
type Store struct {
	config Config
	client *secretsmanager.Client
}

func (s *Store) String() string { return "AWS SecretsManager: " + s.config.Addr }

// Status returns the current state of the AWS SecretsManager instance.
// In particular, whether it is reachable and the network latency.
func (s *Store) Status(ctx context.Context) (kes.KeyStoreState, error) {
	// Build the endpoint URL
	endpoint := "https://" + s.config.Addr
	if s.config.Addr == "" {
		endpoint = fmt.Sprintf("https://secretsmanager.%s.amazonaws.com", s.config.Region)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return kes.KeyStoreState{}, err
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return kes.KeyStoreState{}, &keystore.ErrUnreachable{Err: err}
	}
	defer xhttp.DrainBody(resp.Body)

	return kes.KeyStoreState{
		Latency: time.Since(start),
	}, nil
}

// Create stores the given key-value pair at the AWS SecretsManager
// if and only if it doesn't exists. If such an entry already exists
// it returns kes.ErrKeyExists.
//
// If the SecretsManager.KMSKeyID is set AWS will use this key ID to
// encrypt the values. Otherwise, AWS will use the default key ID for
// encrypting secrets at the AWS SecretsManager.
func (s *Store) Create(ctx context.Context, name string, value []byte) error {
	createInput := &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(string(value)),
	}
	if s.config.KMSKeyID != "" {
		createInput.KmsKeyId = aws.String(s.config.KMSKeyID)
	}

	_, err := s.client.CreateSecret(ctx, createInput)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}

		var rae *types.ResourceExistsException
		if errors.As(err, &rae) {
			return kesdk.ErrKeyExists
		}

		return fmt.Errorf("aws: failed to create '%s': %v", name, err)
	}
	return nil
}

// Set stores the given key-value pair at the AWS SecretsManager
// if and only if it doesn't exists. If such an entry already exists
// it returns kes.ErrKeyExists.
//
// If the SecretsManager.KMSKeyID is set AWS will use this key ID to
// encrypt the values. Otherwise, AWS will use the default key ID for
// encrypting secrets at the AWS SecretsManager.
func (s *Store) Set(ctx context.Context, name string, value []byte) error {
	return s.Create(ctx, name, value)
}

// Get returns the value associated with the given key.
// If no entry for key exists, it returns kes.ErrKeyNotFound.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	response, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		var dfe *types.DecryptionFailure
		if errors.As(err, &dfe) {
			return nil, fmt.Errorf("aws: cannot access '%s': %v", name, err)
		}

		var rnfe *types.ResourceNotFoundException
		if errors.As(err, &rnfe) {
			return nil, kesdk.ErrKeyNotFound
		}

		// Check for other API errors
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "DecryptionFailureException":
				return nil, fmt.Errorf("aws: cannot access '%s': %v", name, err)
			case "ResourceNotFoundException":
				return nil, kesdk.ErrKeyNotFound
			}
		}

		return nil, fmt.Errorf("aws: failed to read '%s': %v", name, err)
	}

	// AWS has two different ways to store a secret. Either as
	// "SecretString" or as "SecretBinary". While they *seem* to
	// be equivalent from an API point of view, AWS console e.g.
	// only shows "SecretString" not "SecretBinary".
	// However, AWS demands and specifies that only one is present -
	// either "SecretString" or "SecretBinary" - we can check which
	// one is present and safely assume that the other one isn't.
	var value []byte
	if response.SecretString != nil {
		value = []byte(*response.SecretString)
	} else {
		value = response.SecretBinary
	}
	return value, nil
}

// Delete removes the key-value pair from the AWS SecretsManager, if
// it exists.
func (s *Store) Delete(ctx context.Context, name string) error {
	_, err := s.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(name),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}

		var rnfe *types.ResourceNotFoundException
		if errors.As(err, &rnfe) {
			return kesdk.ErrKeyNotFound
		}

		// Check for other API errors
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			if apiErr.ErrorCode() == "ResourceNotFoundException" {
				return kesdk.ErrKeyNotFound
			}
		}

		return fmt.Errorf("aws: failed to delete '%s': %v", name, err)
	}
	return nil
}

// List returns a new Iterator over the names of
// all stored keys.
// List returns the first n key names, that start with the given
// prefix, and the next prefix from which the listing should
// continue.
//
// It returns all keys with the prefix if n < 0 and less than n
// names if n is greater than the number of keys with the prefix.
//
// An empty prefix matches any key name. At the end of the listing
// or when there are no (more) keys starting with the prefix, the
// returned prefix is empty.
func (s *Store) List(ctx context.Context, prefix string, n int) ([]string, string, error) {
	var names []string

	paginator := secretsmanager.NewListSecretsPaginator(s.client, &secretsmanager.ListSecretsInput{})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, "", err
		}

		for _, secret := range page.SecretList {
			if secret.Name != nil {
				names = append(names, *secret.Name)
			}
		}
	}

	return keystore.List(names, prefix, n)
}

// Close closes the Store.
func (s *Store) Close() error { return nil }
