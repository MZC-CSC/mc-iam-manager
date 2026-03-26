package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/m-cmp/mc-iam-manager/csp"
)

// AWSIDPManager AWS IAM IDP Provider 관리 클라이언트
type AWSIDPManager struct {
	client *iam.Client
}

// NewAWSIDPManagerWithSecretKey Secret Key로 AWSIDPManager 생성
func NewAWSIDPManagerWithSecretKey(accessKeyID, secretAccessKey, region string) (*AWSIDPManager, error) {
	if region == "" {
		region = "ap-northeast-2"
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &AWSIDPManager{client: iam.NewFromConfig(cfg)}, nil
}

// NewAWSIDPManagerWithSessionToken 임시 자격 증명(세션 토큰)으로 AWSIDPManager 생성
func NewAWSIDPManagerWithSessionToken(accessKeyID, secretAccessKey, sessionToken, region string) (*AWSIDPManager, error) {
	if region == "" {
		region = "ap-northeast-2"
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			sessionToken,
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &AWSIDPManager{client: iam.NewFromConfig(cfg)}, nil
}

// GetCspType CSP 타입 반환
func (m *AWSIDPManager) GetCspType() string {
	return "aws"
}

// CreateOIDCProvider OIDC Provider 생성, ARN 반환
func (m *AWSIDPManager) CreateOIDCProvider(ctx context.Context, req *csp.OIDCProviderRequest) (string, error) {
	input := &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String(req.Url),
		ClientIDList:   req.ClientIDList,
		ThumbprintList: req.ThumbprintList,
	}

	if len(req.Tags) > 0 {
		input.Tags = convertTags(req.Tags)
	}

	result, err := m.client.CreateOpenIDConnectProvider(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to create OIDC provider: %w", err)
	}

	return aws.ToString(result.OpenIDConnectProviderArn), nil
}

// GetOIDCProvider OIDC Provider 조회
func (m *AWSIDPManager) GetOIDCProvider(ctx context.Context, arn string) (*csp.OIDCProviderInfo, error) {
	result, err := m.client.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get OIDC provider: %w", err)
	}

	info := &csp.OIDCProviderInfo{
		Arn:            arn,
		Url:            aws.ToString(result.Url),
		ClientIDList:   result.ClientIDList,
		ThumbprintList: result.ThumbprintList,
	}
	if result.CreateDate != nil {
		info.CreateDate = *result.CreateDate
	}

	return info, nil
}

// DeleteOIDCProvider OIDC Provider 삭제
func (m *AWSIDPManager) DeleteOIDCProvider(ctx context.Context, arn string) error {
	_, err := m.client.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	if err != nil {
		return fmt.Errorf("failed to delete OIDC provider: %w", err)
	}
	return nil
}

// ListOIDCProviders OIDC Provider 목록 조회
func (m *AWSIDPManager) ListOIDCProviders(ctx context.Context) ([]*csp.OIDCProviderInfo, error) {
	result, err := m.client.ListOpenIDConnectProviders(ctx, &iam.ListOpenIDConnectProvidersInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDC providers: %w", err)
	}

	providers := make([]*csp.OIDCProviderInfo, 0, len(result.OpenIDConnectProviderList))
	for _, p := range result.OpenIDConnectProviderList {
		arn := aws.ToString(p.Arn)
		detail, err := m.GetOIDCProvider(ctx, arn)
		if err != nil {
			// 상세 조회 실패한 항목은 ARN만 포함
			providers = append(providers, &csp.OIDCProviderInfo{Arn: arn})
			continue
		}
		providers = append(providers, detail)
	}

	return providers, nil
}

// CreateSAMLProvider SAML Provider 생성, ARN 반환
func (m *AWSIDPManager) CreateSAMLProvider(ctx context.Context, req *csp.SAMLProviderRequest) (string, error) {
	input := &iam.CreateSAMLProviderInput{
		Name:                 aws.String(req.Name),
		SAMLMetadataDocument: aws.String(req.SAMLMetadataDocument),
	}

	if len(req.Tags) > 0 {
		input.Tags = convertTags(req.Tags)
	}

	result, err := m.client.CreateSAMLProvider(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to create SAML provider: %w", err)
	}

	return aws.ToString(result.SAMLProviderArn), nil
}

// GetSAMLProvider SAML Provider 조회
func (m *AWSIDPManager) GetSAMLProvider(ctx context.Context, arn string) (*csp.SAMLProviderInfo, error) {
	result, err := m.client.GetSAMLProvider(ctx, &iam.GetSAMLProviderInput{
		SAMLProviderArn: aws.String(arn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get SAML provider: %w", err)
	}

	// ARN에서 이름 추출 (arn:aws:iam::ACCOUNT:saml-provider/NAME)
	name := arn
	if idx := strings.LastIndex(arn, "/"); idx >= 0 {
		name = arn[idx+1:]
	}

	info := &csp.SAMLProviderInfo{
		Arn:  arn,
		Name: name,
	}
	if result.CreateDate != nil {
		info.CreateDate = *result.CreateDate
	}
	if result.ValidUntil != nil {
		info.ValidUntil = *result.ValidUntil
	}

	return info, nil
}

// UpdateSAMLProvider SAML Provider 메타데이터 업데이트, ARN 반환
func (m *AWSIDPManager) UpdateSAMLProvider(ctx context.Context, arn string, samlMetadataDocument string) (string, error) {
	result, err := m.client.UpdateSAMLProvider(ctx, &iam.UpdateSAMLProviderInput{
		SAMLProviderArn:      aws.String(arn),
		SAMLMetadataDocument: aws.String(samlMetadataDocument),
	})
	if err != nil {
		return "", fmt.Errorf("failed to update SAML provider: %w", err)
	}

	return aws.ToString(result.SAMLProviderArn), nil
}

// DeleteSAMLProvider SAML Provider 삭제
func (m *AWSIDPManager) DeleteSAMLProvider(ctx context.Context, arn string) error {
	_, err := m.client.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{
		SAMLProviderArn: aws.String(arn),
	})
	if err != nil {
		return fmt.Errorf("failed to delete SAML provider: %w", err)
	}
	return nil
}

// ListSAMLProviders SAML Provider 목록 조회
func (m *AWSIDPManager) ListSAMLProviders(ctx context.Context) ([]*csp.SAMLProviderInfo, error) {
	result, err := m.client.ListSAMLProviders(ctx, &iam.ListSAMLProvidersInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to list SAML providers: %w", err)
	}

	providers := make([]*csp.SAMLProviderInfo, 0, len(result.SAMLProviderList))
	for _, p := range result.SAMLProviderList {
		arn := aws.ToString(p.Arn)
		name := arn
		if idx := strings.LastIndex(arn, "/"); idx >= 0 {
			name = arn[idx+1:]
		}
		info := &csp.SAMLProviderInfo{
			Arn:  arn,
			Name: name,
		}
		if p.CreateDate != nil {
			info.CreateDate = *p.CreateDate
		}
		if p.ValidUntil != nil {
			info.ValidUntil = *p.ValidUntil
		}
		providers = append(providers, info)
	}

	return providers, nil
}

// compile-time interface check
var _ csp.IDPManager = (*AWSIDPManager)(nil)
