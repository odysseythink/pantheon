package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/odysseythink/mlog"
)

func HttpClientCall[T any](
	ctx context.Context, method, call_url string,
	query map[string][]string,
	payload any,
	headers map[string]string,
	timeouts ...int,
) (T, error) {
	return HttpClientCallWithClient[T](nil, ctx, method, call_url, query, payload, headers, timeouts...)
}

func HttpClientCallWithClient[T any](
	client *http.Client,
	ctx context.Context, method, call_url string,
	query map[string][]string,
	payload any,
	headers map[string]string,
	timeouts ...int,
) (T, error) {
	var empty_resp T
	u, err := url.Parse(call_url)
	if err != nil {
		mlog.Errorf("parse url(%s) failed:%v", call_url, err)
		return empty_resp, &ProviderError{
			Message: fmt.Sprintf("parse url(%s) failed:%v", call_url, err),
			Status:  http.StatusInternalServerError,
		}
	}
	q := u.Query()
	if len(query) > 0 {
		for k, v := range query {
			if len(v) > 0 {
				for _, sv := range v {
					if sv != "" {
						q.Add(k, sv)
					}
				}
			}
		}
	}
	u.RawQuery = q.Encode()
	call_url = u.String()
	var bodyReader io.Reader
	if payload != nil {
		if _, ok := payload.(io.Reader); ok {
			bodyReader = payload.(io.Reader)
		} else {
			data, err := json.Marshal(payload)
			if err != nil {
				return empty_resp, &ProviderError{
					Message: err.Error(),
					Status:  http.StatusInternalServerError,
				}
			}
			bodyReader = bytes.NewReader(data)
		}
	}

	if client == nil {
		client = http.DefaultClient
	}
	if len(timeouts) > 0 {
		client.Timeout = time.Duration(timeouts[0]) * time.Millisecond
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, method, call_url, bodyReader)
	if err != nil {
		mlog.Errorf("call http url %s[%s] NewRequest failed:%v", method, call_url, err)
		return empty_resp, &ProviderError{
			Message: fmt.Sprintf("call http url %s[%s] NewRequest failed:%v", method, call_url, err),
			Status:  http.StatusInternalServerError,
		}
	}
	if len(headers) > 0 {
		for k, v := range headers {
			request.Header.Set(k, v)
		}
	}

	dump, err := httputil.DumpRequestOut(request, true)
	if err != nil {
		mlog.Errorf("call http url %s[%s] httputil.DumpRequestOut failed:%v", call_url, method, err)
		return empty_resp, &ProviderError{
			Message: fmt.Sprintf("call http url %s[%s] httputil.DumpRequestOut failed:%v", call_url, method, err),
			Status:  http.StatusInternalServerError,
		}
	}
	mlog.Debugf("------request=%s", string(dump))
	resp, err := client.Do(request)
	if err != nil {
		mlog.Errorf("call http url %s[%s] failed:%v", call_url, method, err)
		return empty_resp, &ProviderError{
			Message: fmt.Sprintf("call http url %s[%s] failed:%v", call_url, method, err),
			Status:  http.StatusInternalServerError,
		}
	}
	{
		dump, _ = httputil.DumpResponse(resp, true)
		mlog.Debugf("------response=%s", string(dump))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		mlog.Errorf("call http url %s[%s] return failed, status_code=%d", call_url, method, resp.StatusCode)
		return empty_resp, &ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
		}
	}
	err = json.NewDecoder(resp.Body).Decode(&empty_resp)
	if err != nil && err != io.EOF {
		mlog.Errorf("resp.Body json decode failed:%v", err)
		return empty_resp, &ProviderError{
			Message: fmt.Sprintf("resp.Body json decode failed:%v", err),
			Status:  http.StatusInternalServerError,
		}
	}

	return empty_resp, nil
}
