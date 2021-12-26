// Package awslambdaplugin exposes a middleware that invokes a lambda function mimicking the ALB.
package awslambdaplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/lambda"
)

// Config the plugin configuration.
type Config struct {
	AccessKey   string `json:"accessKey,omitempty"`
	SecretKey   string `json:"secretKey,omitempty"`
	Region      string `json:"region,omitempty"`
	FunctionArn string `json:"functionArn,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		Region:      "",
		FunctionArn: "",
		Endpoint:    "",
	}
}

// AwsLambdaPlugin plugin main struct.
type AwsLambdaPlugin struct {
	next        http.Handler
	functionArn string
	name        string
	client      *lambda.Lambda
}

// LambdaRequest represents a request to send to lambda.
type LambdaRequest struct {
	HTTPMethod                      string              `json:"httpMethod"`
	Path                            string              `json:"path"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	Headers                         map[string]string   `json:"headers"`
	Body                            string              `json:"body"`
	IsBase64Encoded                 bool                `json:"isBase64Encoded"`
}

// LambdaResponse represents a response to a lambda HTTP request from LB.
type LambdaResponse struct {
	StatusCode        int                 `json:"statusCode"`
	StatusDescription string              `json:"statusDescription"`
	IsBase64Encoded   bool                `json:"isBase64Encoded"`
	Headers           map[string]string   `json:"headers"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders"`
	Body              string              `json:"body"`
}

// New created a new AwsLambdaPlugin plugin.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if len(config.FunctionArn) == 0 {
		return nil, fmt.Errorf("function arn cannot be empty")
	}

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))

	var region *string
	if len(config.Region) > 0 {
		region = aws.String(config.Region)
	}

	var endpoint *string
	if len(config.Endpoint) > 0 {
		endpoint = aws.String(config.Endpoint)
	}

	var creds *credentials.Credentials
	if len(config.AccessKey) > 0 && len(config.SecretKey) > 0 {
		creds = credentials.NewStaticCredentials(config.AccessKey, config.SecretKey, "")
	}

	client := lambda.New(sess, &aws.Config{
		Region:      region,
		Endpoint:    endpoint,
		Credentials: creds,
	})

	return &AwsLambdaPlugin{
		functionArn: config.FunctionArn,
		client:      client,
		next:        next,
		name:        name,
	}, nil
}

func (a *AwsLambdaPlugin) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	base64Encoded, body := bodyToBase64(req)
	resp := a.invokeFunction(LambdaRequest{
		HTTPMethod:                      req.Method,
		Path:                            req.URL.Path,
		QueryStringParameters:           valuesToMap(req.URL.Query()),
		MultiValueQueryStringParameters: valuesToMultiMap(req.URL.Query()),
		Headers:                         headersToMap(req.Header),
		MultiValueHeaders:               headersToMultiMap(req.Header),
		Body:                            body,
		IsBase64Encoded:                 base64Encoded,
	})

	body = resp.Body
	if resp.IsBase64Encoded {
		buf, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			panic(err)
		}

		body = string(buf)
	}

	for key, value := range resp.Headers {
		rw.Header().Set(key, value)
	}

	for key, values := range resp.MultiValueHeaders {
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}

	rw.WriteHeader(resp.StatusCode)
	_, err := rw.Write([]byte(body))
	if err != nil {
		panic(err)
	}
}

func bodyToBase64(req *http.Request) (bool, string) {
	base64Encoded := false
	body := ""
	if req.ContentLength != 0 {
		var buf bytes.Buffer
		encoder := base64.NewEncoder(base64.StdEncoding, &buf)

		_, err := io.Copy(encoder, req.Body)
		if err != nil {
			panic(err)
		}

		err = encoder.Close()
		if err != nil {
			panic(err)
		}

		body = buf.String()
		base64Encoded = true
	}

	return base64Encoded, body
}

func (a *AwsLambdaPlugin) invokeFunction(request LambdaRequest) LambdaResponse {
	payload, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}

	result, err := a.client.Invoke(&lambda.InvokeInput{
		FunctionName: aws.String(a.functionArn),
		Payload:      payload,
	})
	if err != nil {
		panic(err)
	}

	if *result.StatusCode != 200 {
		panic(fmt.Errorf("call to lambda failed"))
	}

	var resp LambdaResponse
	err = json.Unmarshal(result.Payload, &resp)
	if err != nil {
		panic(err)
	}

	return resp
}

func headersToMap(h http.Header) map[string]string {
	values := map[string]string{}
	for name, headers := range h {
		if len(headers) != 1 {
			continue
		}

		values[name] = headers[0]
	}

	return values
}

func headersToMultiMap(h http.Header) map[string][]string {
	values := map[string][]string{}
	for name, headers := range h {
		if len(headers) < 2 {
			continue
		}

		values[name] = headers
	}

	return values
}

func valueToString(f interface{}) (string, bool) {
	var v string
	typeof := reflect.TypeOf(f)
	s := reflect.ValueOf(f)

	switch typeof.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v = strconv.FormatInt(s.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v = strconv.FormatUint(s.Uint(), 10)
	case reflect.Float32:
		v = strconv.FormatFloat(s.Float(), 'f', 4, 32)
	case reflect.Float64:
		v = strconv.FormatFloat(s.Float(), 'f', 4, 64)
	case reflect.String:
		v = s.String()
	case reflect.Slice:
		t, valid := valuesToStrings(f)
		if !valid || len(t) != 1 {
			return "", false
		}

		v = t[0]
	default:
		return "", false
	}

	return v, true
}

func valuesToStrings(f interface{}) ([]string, bool) {
	typeof := reflect.TypeOf(f)
	if typeof.Kind() != reflect.Slice {
		return []string{}, false
	}

	var v []string
	switch typeof.Elem().Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Float32, reflect.Float64, reflect.String:
		s := reflect.ValueOf(f)

		for i := 0; i < s.Len(); i++ {
			conv, valid := valueToString(s.Index(i).Interface())
			if !valid {
				continue
			}

			v = append(v, conv)
		}
	default:
		return []string{}, false
	}

	return v, true
}

func valuesToMap(i url.Values) map[string]string {
	values := map[string]string{}
	for name, val := range i {
		value, valid := valueToString(val)
		if !valid {
			continue
		}

		values[name] = value
	}

	return values
}

func valuesToMultiMap(i url.Values) map[string][]string {
	values := map[string][]string{}
	for name, val := range i {
		value, valid := valuesToStrings(val)
		if !valid || len(value) == 1 {
			continue
		}

		values[name] = value
	}

	return values
}
