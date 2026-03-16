package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
)

func sendWebhookJSON(ctx context.Context, client *http.Client, webhookURL string, body []byte, buildErrMsg string, sendErrMsg string, statusErrFmt string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", buildErrMsg, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", sendErrMsg, err)
	}
	defer func() {
		if closeErr := closeResponseBody(resp); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(statusErrFmt, resp.StatusCode)
	}

	return nil
}

func closeResponseBody(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	return resp.Body.Close()
}
