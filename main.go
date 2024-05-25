package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/patrickmn/go-cache"
	"github.com/rs/zerolog/log"
	"go.uber.org/ratelimit"
	"golang.org/x/net/context"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Context struct {
	limiter ratelimit.Limiter
	cache   *cache.Cache
	s3      *s3.Client
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

func unexpectedError(err error) HttpResponse {
	if err != nil {
		log.Error().Err(err).Send()
	}

	return UnexpectedError
}

var s3Bucket string
var cdnHostForBucket string

func GetFaviconEndpoint(ctx Context, rw http.ResponseWriter, r *http.Request) HttpResponse {
	URL := r.URL.String()

	// Sanity check to prevent against path-traversal shenanigans from a malicious user agent.
	if !strings.HasPrefix(URL, "/api/v1/resolve") {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must be a valid url"}
	}

	var err error
	URL, err = url.QueryUnescape(URL[len("/api/v1/resolve")+1:])
	if err != nil {
		return unexpectedError(err)
	}

	fallbackURL := strings.TrimSpace(r.URL.Query().Get("fallbackURL"))

	if len(URL) > 1<<16 {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must not be greater than 65,536 bytes"}
	}

	parsedUrl, err := url.ParseRequestURI(URL)
	if err != nil {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must be a valid url"}
	}

	objectKey := "favicons/" + parsedUrl.Hostname() + ".png"
	objectURL := "https://" + cdnHostForBucket + "/" + objectKey

	rw.Header().Add("Cache-Control", "max-age=604800, immutable") // one week

	if _, ok := ctx.cache.Get(parsedUrl.Hostname()); ok {
		return HttpResponse{Success: true, Value: objectURL}
	}

	_, err = ctx.s3.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: &s3Bucket,
		Key:    &objectKey,
	})
	if err != nil {
		var responseError *awshttp.ResponseError
		if errors.As(err, &responseError) && responseError.ResponseError.HTTPStatusCode() == http.StatusNotFound {
			//
		} else {
			return unexpectedError(err)
		}
	} else {
		ctx.cache.Set(parsedUrl.Hostname(), true, cache.DefaultExpiration)

		return HttpResponse{Success: true, Value: objectURL}
	}

	ctx.limiter.Take()

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

		// The errors returned from FindFaviconUrl are all known
		// and intentionally vague enough that we can safely return
		// them.
		return HttpResponse{
			Status: http.StatusBadRequest,
			Value:  err.Error(),
		}
	}

	patchedIcon, err := PatchIcon(resolvedIcon)
	if err != nil {
		_ = resolvedIcon.Body.Close()
		return unexpectedError(err)
	}

	_ = resolvedIcon.Body.Close()

	buf := new(bytes.Buffer)
	err = png.Encode(buf, patchedIcon)
	if err != nil {
		return unexpectedError(err)
	}

	_, err = ctx.s3.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      &s3Bucket,
		Key:         &objectKey,
		Body:        buf,
		ContentType: aws.String(resolvedIcon.Type.ContentType()),
		ACL:         "public-read",
	})
	if err != nil {
		return unexpectedError(err)
	}

	ctx.cache.Set(parsedUrl.Hostname(), true, cache.DefaultExpiration)

	return HttpResponse{
		Success: true,
		Value:   objectURL,
	}
}

func Endpoint(ctx Context, handler func(Context, http.ResponseWriter, *http.Request) HttpResponse) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		res := handler(ctx, rw, r)
		if res.Status == 0 {
			res.Status = http.StatusOK
		}

		rw.Header().Add("Content-Type", "application/json")
		rw.WriteHeader(res.Status)
		_ = json.NewEncoder(rw).Encode(res)

		ip := r.Header.Get("CF-Connecting-IP")
		if ip == "" {
			ip = "local/" + r.RemoteAddr
		}

		log.Info().Str("ip", ip).
			Str("ua", r.Header.Get("User-Agent")).
			Str("method", r.Method).
			Str("path", r.URL.String()).
			Int("status", res.Status).
			Bool("ok", res.Success).
			Send()

		return
	})
}

func runHttpServer(port string) error {
	accessKeyId := os.Getenv("AWS_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	endpoint := os.Getenv("AWS_ENDPOINT")
	region := os.Getenv("AWS_REGION")
	cdnHostForBucket = os.Getenv("ASSET_URL_FOR_BUCKET")
	s3Bucket = os.Getenv("AWS_BUCKET")

	if cdnHostForBucket == "" {
		return errors.New("ASSET_URL_FOR_BUCKET is empty, please specify a URL")
	}

	if s3Bucket == "" {
		return errors.New("AWS_BUCKET is empty, please specify a s3 bucket")
	}

	ctx := Context{
		limiter: ratelimit.New(100),
		cache:   cache.New(time.Hour*24*30, time.Hour*24*5),
		s3: s3.New(s3.Options{
			Region:       region,
			BaseEndpoint: aws.String("https://" + endpoint),
			Credentials:  credentials.NewStaticCredentialsProvider(accessKeyId, secretAccessKey, ""),
		}),
	}

	http.Handle("/api/v1/resolve/", Endpoint(ctx, GetFaviconEndpoint))

	return http.ListenAndServe(port, nil)
}

func main() {
	err := runHttpServer(":3333")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Cannot start server: %s\n", err)
		os.Exit(1)
	}
}
