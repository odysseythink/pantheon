package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/odysseythink/ai/core"
)

type bedrockClient struct {
	region       string
	accessKeyID  string
	secretKey    string
	sessionToken string
	httpClient   *http.Client
	signer       *v4.Signer
	endpoint     string // override for testing
}

func newBedrockClient(region, accessKeyID, secretKey, sessionToken string) *bedrockClient {
	return &bedrockClient{
		region:       region,
		accessKeyID:  accessKeyID,
		secretKey:    secretKey,
		sessionToken: sessionToken,
		httpClient:   http.DefaultClient,
		signer:       v4.NewSigner(),
	}
}

func (c *bedrockClient) invoke(ctx context.Context, modelID string, body any, dst any) error {
	url := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke", c.region, modelID)
	if c.endpoint != "" {
		url = fmt.Sprintf("%s/model/%s/invoke", c.endpoint, modelID)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	creds := credentials.NewStaticCredentialsProvider(c.accessKeyID, c.secretKey, c.sessionToken)
	credValues, err := creds.Retrieve(ctx)
	if err != nil {
		return err
	}
	payloadHash := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := c.signer.SignHTTP(ctx, credValues, req, payloadHash, "bedrock", c.region, time.Now()); err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		return &core.ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
		}
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
