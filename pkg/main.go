package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/allegro/bigcache"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"go.uber.org/ratelimit"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Context struct {
	limiter ratelimit.Limiter
	cache   *bigcache.BigCache
	s3      *session.Session
}

type HttpResponse struct {
	Success bool           `json:"success"`
	Status  int            `json:"status"`
	Value   any            `json:"value"`
	Meta    map[string]any `json:"meta"`
}

var UnexpectedError = HttpResponse{
	Status: http.StatusInternalServerError,
	Value:  "unexpected error",
}

var bucket string

func GetFaviconEndpoint(ctx Context, rw http.ResponseWriter, r *http.Request) HttpResponse {
	URL := strings.TrimSpace(r.URL.Query().Get("url"))
	fallbackURL := strings.TrimSpace(r.URL.Query().Get("fallbackURL"))

	if len(URL) > 1<<16 {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must not be greater than 65,536 bytes"}
	}

	parsedUrl, err := url.ParseRequestURI(URL)
	if err != nil {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must be a valid url"}
	}

	baseURL := getBaseURL(parsedUrl)

	resolvedIcon, err := FindFaviconURL(parsedUrl)
	if err != nil {
		if errors.Is(err, ErrIconNotFound) {
			return HttpResponse{
				Success: true,
				Status:  http.StatusOK,
				Value:   fallbackURL,
				Meta: map[string]any{
					"isFallback": true,
				},
			}
		}

		return UnexpectedError
	}

	patchedIcon, err := PatchIcon(resolvedIcon)
	if err != nil {
		_ = resolvedIcon.Body.Close()
		return UnexpectedError
	}

	_ = resolvedIcon.Body.Close()

	buf := new(bytes.Buffer)
	err = png.Encode(buf, patchedIcon)
	if err != nil {
		return UnexpectedError
	}

	uploader := s3manager.NewUploader(ctx.s3)
	_, err = uploader.UploadWithContext(context.TODO(), &s3manager.UploadInput{
		Bucket: &bucket,
		Key:    &baseURL,
		Body:   buf,
		ACL:    aws.String("public-read"),
	})
	if err != nil {
		return UnexpectedError
	}

	return HttpResponse{
		Success: true,
		Value:   "https://" + *ctx.s3.Config.Endpoint + "/" + baseURL,
	}
}

func Endpoint(ctx Context, handler func(Context, http.ResponseWriter, *http.Request) HttpResponse) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		res := handler(ctx, rw, r)
		if res.Status == 0 {
			res.Status = http.StatusOK
		}

		rw.WriteHeader(res.Status)
		_ = json.NewEncoder(rw).Encode(res)
		return
	})
}

func runHttpServer(port string) error {
	cache, err := bigcache.NewBigCache(bigcache.DefaultConfig(time.Hour * 24))
	if err != nil {
		return err
	}

	accessKeyId := os.Getenv("AWS_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	bucket = os.Getenv("AWS_BUCKET")
	endpoint := os.Getenv("AWS_ENDPOINT")

	s3Config := &aws.Config{
		Endpoint:    aws.String(endpoint),
		Credentials: credentials.NewStaticCredentials(accessKeyId, secretAccessKey, ""),
	}

	s3session, err := session.NewSession(s3Config)
	if err != nil {
		return err
	}

	if bucket == "" {
		return errors.New("no bucket defined (AWS_BUCKET is empty)")
	}

	ctx := Context{
		limiter: ratelimit.New(100),
		cache:   cache,
		s3:      s3session,
	}

	http.Handle("/api/v1/resolve", Endpoint(ctx, GetFaviconEndpoint))

	return http.ListenAndServe(port, nil)
}

func main() {
	err := runHttpServer(":3333")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, " Cannot start server: %s\n", err)
		os.Exit(1)
	}
}
