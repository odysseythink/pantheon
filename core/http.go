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

	// 调试输出是尽力而为的旁路：dump 失败绝不应影响请求本身，
	// 更不能把一个本该正常发出的请求变成错误返回。
	if VerboseHTTP() {
		dumpRequest(request)
	}
	resp, err := client.Do(request)
	if err != nil {
		mlog.Errorf("call http url %s[%s] failed:%v", call_url, method, err)
		return empty_resp, &ProviderError{
			Message: fmt.Sprintf("call http url %s[%s] failed:%v", call_url, method, err),
			Status:  http.StatusInternalServerError,
			Err:     err,
		}
	}
	if VerboseHTTP() {
		// 这些内容含业务数据（模型返回的解析结果），故默认不输出。
		if dump, derr := httputil.DumpResponse(resp, true); derr == nil {
			mlog.Debugf("------response=%s", string(dump))
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		mlog.Errorf("call http url %s[%s] return failed, status_code=%d", call_url, method, resp.StatusCode)
		return empty_resp, &ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
			Headers: resp.Header.Clone(),
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

// sensitiveHeaders 需要在调试日志中脱敏的请求头。
//
// Authorization 里是 API 密钥；其余是常见 provider 的密钥头。
// 日志会被采集、归档、复制到工单中，密钥一旦落盘等同泄露。
var sensitiveHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Api-Key",
	"X-Api-Key",
	"X-Goog-Api-Key",
}

// dumpRequest 输出请求原文，凭证头会被临时脱敏。
//
// 注意不要改成对 req.Clone() 做 dump：Clone 是浅拷贝，Clone 与原请求的
// Body 指向同一个 reader，而 DumpRequestOut 会把它读干，
// 结果导致真正要发出的请求体为空。
// 因此这里临时替换凭证头，dump 完立即恢复。
func dumpRequest(req *http.Request) {
	var saved map[string]string
	for _, h := range sensitiveHeaders {
		if v := req.Header.Get(h); v != "" {
			if saved == nil {
				saved = make(map[string]string, len(sensitiveHeaders))
			}
			saved[h] = v
			req.Header.Set(h, redactedPlaceholder)
		}
	}
	if dump, err := httputil.DumpRequestOut(req, true); err == nil {
		mlog.Debugf("------request=%s", string(dump))
	}
	for h, v := range saved {
		req.Header.Set(h, v)
	}
}

const redactedPlaceholder = "***REDACTED***"
