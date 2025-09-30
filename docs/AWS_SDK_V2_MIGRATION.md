# AWS SDK for Go v2 Migration Guide

## Overview

This document describes the migration of the KES (Key Encryption Service) codebase from AWS SDK for Go v1 to AWS SDK for Go v2. The migration primarily affects the AWS Secrets Manager integration in the `internal/keystore/aws` package.

## Migration Summary

### Key Changes

1. **Module Updates**
   - Removed: `github.com/aws/aws-sdk-go v1.55.8`
   - Added: 
     - `github.com/aws/aws-sdk-go-v2 v1.39.2`
     - `github.com/aws/aws-sdk-go-v2/config v1.31.12`
     - `github.com/aws/aws-sdk-go-v2/credentials v1.18.16`
     - `github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.39.6`
     - `github.com/aws/smithy-go v1.23.0`
     - `github.com/aws/smithy-go/endpoints` (for EndpointResolverV2)

2. **Import Path Changes**
   ```go
   // Before (v1)
   import (
       "github.com/aws/aws-sdk-go/aws"
       "github.com/aws/aws-sdk-go/aws/awserr"
       "github.com/aws/aws-sdk-go/aws/credentials"
       "github.com/aws/aws-sdk-go/aws/session"
       "github.com/aws/aws-sdk-go/service/secretsmanager"
   )

   // After (v2)
   import (
       "github.com/aws/aws-sdk-go-v2/aws"
       "github.com/aws/aws-sdk-go-v2/config"
       "github.com/aws/aws-sdk-go-v2/credentials"
       "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
       "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
       smithy "github.com/aws/smithy-go"
       smithyendpoints "github.com/aws/smithy-go/endpoints"
   )
   ```

## Detailed Changes

### 1. Configuration and Session Management

#### Before (v1)
```go
session, err := session.NewSessionWithOptions(session.Options{
    Config: aws.Config{
        Endpoint:    aws.String(config.Addr),
        Region:      aws.String(config.Region),
        Credentials: credentials,
    },
    SharedConfigState: session.SharedConfigDisable,
})
client := secretsmanager.New(session)
```

#### After (v2)
```go
awsCfg, err := config.LoadDefaultConfig(ctx,
    config.WithRegion(cfg.Region),
    config.WithCredentialsProvider(
        credentials.NewStaticCredentialsProvider(
            cfg.Login.AccessKey,
            cfg.Login.SecretKey,
            cfg.Login.SessionToken,
        ),
    ),
)
client := secretsmanager.NewFromConfig(awsCfg)
```

### 2. API Method Changes

All API methods now require a context as the first parameter and have simplified names:

| v1 Method | v2 Method |
|-----------|-----------|
| `CreateSecretWithContext(ctx, input)` | `CreateSecret(ctx, input)` |
| `GetSecretValueWithContext(ctx, input)` | `GetSecretValue(ctx, input)` |
| `DeleteSecretWithContext(ctx, input)` | `DeleteSecret(ctx, input)` |
| `ListSecretsPagesWithContext(ctx, input, fn)` | Paginator pattern (see below) |

### 3. Error Handling

#### Before (v1)
```go
if err, ok := err.(awserr.Error); ok {
    switch err.Code() {
    case secretsmanager.ErrCodeResourceNotFoundException:
        return kesdk.ErrKeyNotFound
    case secretsmanager.ErrCodeResourceExistsException:
        return kesdk.ErrKeyExists
    }
}
```

#### After (v2)
```go
// Using typed errors
var rnfe *types.ResourceNotFoundException
if errors.As(err, &rnfe) {
    return kesdk.ErrKeyNotFound
}

var rae *types.ResourceExistsException
if errors.As(err, &rae) {
    return kesdk.ErrKeyExists
}

// Using smithy.APIError for generic error handling
var apiErr smithy.APIError
if errors.As(err, &apiErr) {
    switch apiErr.ErrorCode() {
    case "ResourceNotFoundException":
        return kesdk.ErrKeyNotFound
    }
}
```

### 4. Pagination

#### Before (v1)
```go
err := s.client.ListSecretsPagesWithContext(ctx, &secretsmanager.ListSecretsInput{}, 
    func(page *secretsmanager.ListSecretsOutput, lastPage bool) bool {
        for _, secret := range page.SecretList {
            names = append(names, *secret.Name)
        }
        return !lastPage
    })
```

#### After (v2)
```go
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
```

### 5. Credentials Provider

#### Before (v1)
```go
credentials := credentials.NewStaticCredentials(
    config.Login.AccessKey,
    config.Login.SecretKey,
    config.Login.SessionToken,
)
```

#### After (v2)
```go
credProvider := credentials.NewStaticCredentialsProvider(
    cfg.Login.AccessKey,
    cfg.Login.SecretKey,
    cfg.Login.SessionToken,
)
```

### 6. Custom Endpoint Configuration

The v2 SDK requires a different approach for custom endpoints. **Important:** Avoid using deprecated global endpoint resolution methods like `WithEndpointResolverWithOptions` or `BaseEndpoint`.

#### Before (v1)
```go
Config: aws.Config{
    Endpoint: aws.String(config.Addr),
}
```

#### After (v2) - Using EndpointResolverV2 (Recommended)
```go
// Define a custom endpoint resolver that implements EndpointResolverV2
type customEndpointResolver struct {
    endpoint string
}

func (r *customEndpointResolver) ResolveEndpoint(ctx context.Context, params secretsmanager.EndpointParameters) (smithyendpoints.Endpoint, error) {
    u, err := url.Parse(r.endpoint)
    if err != nil {
        return smithyendpoints.Endpoint{}, err
    }
    return smithyendpoints.Endpoint{
        URI: *u,
    }, nil
}

// Use it in client options
clientOpts := []func(*secretsmanager.Options){
    func(o *secretsmanager.Options) {
        o.EndpointResolverV2 = &customEndpointResolver{
            endpoint: "https://" + cfg.Addr,
        }
    }
}
```

**Note:** The global endpoint resolution interface (`WithEndpointResolver`, `WithEndpointResolverWithOptions`, `BaseEndpoint`) is deprecated in SDK v2. Using these deprecated methods will prevent you from using endpoint-related service features and may cause unexpected behavior with services that have endpoint customizations like S3.

## Benefits of Migration

1. **Improved Performance**: SDK v2 has better performance characteristics with reduced memory allocations
2. **Better Error Handling**: Typed errors make error handling more explicit and type-safe
3. **Context-First API**: All operations require context, promoting better cancellation and timeout handling
4. **Modular Design**: Smaller binary sizes as you only import what you need
5. **Future Support**: AWS SDK v1 is in maintenance mode; new features are only added to v2

## Testing Considerations

1. **Credential Chain**: The SDK v2 credential chain behavior is slightly different. Test with:
   - Static credentials
   - Environment variables
   - IAM roles (EC2/ECS/Lambda)
   - Shared configuration files

2. **Error Scenarios**: Verify error handling for:
   - Missing secrets
   - Duplicate secret creation
   - Network timeouts
   - Invalid credentials

3. **Pagination**: Test list operations with large datasets to ensure pagination works correctly

## Rollback Plan

If issues arise, rollback involves:
1. Reverting changes to `internal/keystore/aws/secrets-manager.go`
2. Restoring go.mod to use AWS SDK v1
3. Running `go mod tidy` to restore dependencies

## References

- [AWS SDK for Go v2 Migration Guide](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/migrate-gosdk.html)
- [AWS SDK for Go v2 Documentation](https://aws.github.io/aws-sdk-go-v2/docs/)
- [AWS Secrets Manager SDK v2 Reference](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/secretsmanager)