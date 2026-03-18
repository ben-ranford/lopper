package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
)

func sendWebhookJSON(ctx context.Context, client *http.Client, webhookURL string, body []byte, buildErrMsg string, sendErrMsg string, statusErrFmt string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", buildErrMsg, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", sendErrMsg, err)
	}
	defer closeResponseBody(resp)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(statusErrFmt, resp.StatusCode)
	}

	return nil
}

func closeResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if resp.Body.Close() != nil {
		return
	}
}
