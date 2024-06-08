package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"faviconapi/defaults"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/patrickmn/go-cache"
	"github.com/rs/zerolog"
	"go.uber.org/ratelimit"
	"golang.org/x/net/context"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const Version = "13"

type Context struct {
	limiter ratelimit.Limiter
	cache   *cache.Cache
	s3      *s3.Client
	log     zerolog.Logger
}

type HttpResponse struct {
	Success bool              `json:"success"`
	Status  int               `json:"status"`
	Value   any               `json:"value"`
	Meta    map[string]string `json:"meta"`
}

var UnexpectedError = HttpResponse{
	Status: http.StatusInternalServerError,
	Value:  "unexpected error",
}

func unexpectedError(ctx Context, err error) HttpResponse {
	if err != nil {
		ctx.log.Error().Err(err).Send()
	}

	return UnexpectedError
}

var s3Bucket string
var cdnHostForBucket string

func GetFaviconEndpoint(ctx Context, rw http.ResponseWriter, r *http.Request) HttpResponse {
	URL := r.URL.String()

	iconMetadata := map[string]string{
		"version": Version,
	}

	// Sanity check to prevent against path-traversal shenanigans from a malicious user agent.
	if !strings.HasPrefix(URL, "/api/v1/resolve") {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must be a valid url"}
	}

	var err error
	URL, err = url.QueryUnescape(URL[len("/api/v1/resolve")+1:])
	if err != nil {
		return unexpectedError(ctx, err)
	}

	fallbackURL := strings.TrimSpace(r.URL.Query().Get("fallbackURL"))

	if len(URL) > 1<<16 {
		return HttpResponse{Status: http.StatusBadRequest, Value: "url field must not be greater than 65,536 bytes"}
	}

	parsedURL, err := url.ParseRequestURI(URL)
	if err != nil {
		// Is the scheme missing?
		fixedURL, err := url.ParseRequestURI("https://" + URL)
		if err != nil {
			return HttpResponse{Status: http.StatusBadRequest, Value: "url field must be a valid url"}
		}

		parsedURL = fixedURL
	}

	objectKey := "favicons/" + parsedURL.Hostname() + ".png"
	objectURL := "https://" + cdnHostForBucket + "/" + objectKey
	cacheKey := parsedURL.Hostname() + Version

	if defaults.CacheStatus == defaults.CacheEnabled {
		// Zeroth layer: browser caching
		rw.Header().Add("Cache-Control", "max-age=604800, immutable") // one week

		// First layer: memory cache
		if meta, ok := ctx.cache.Get(cacheKey); ok {
			return HttpResponse{Success: true, Value: objectURL, Meta: meta.(map[string]string)}
		}

		// Second layer: lookup to see if there's an object with the future name of the icon.
		head, err := ctx.s3.HeadObject(context.TODO(), &s3.HeadObjectInput{
			Bucket: &s3Bucket,
			Key:    &objectKey,
		})
		if err != nil {
			var responseError *awshttp.ResponseError
			if errors.As(err, &responseError) && responseError.ResponseError.HTTPStatusCode() == http.StatusNotFound {
				//
			} else {
				return unexpectedError(ctx, err)
			}
		} else {
			if head.Metadata["version"] == Version {
				ctx.cache.Set(cacheKey, head.Metadata, cache.DefaultExpiration)

				return HttpResponse{Success: true, Value: objectURL, Meta: head.Metadata}
			}

			// New version, keep going
		}
	}

	ctx.limiter.Take()

	resolvedIcon, err := FindFaviconURL(parsedURL)
	if err != nil {
		if errors.Is(err, ErrIconNotFound) {
			return HttpResponse{
				Success: true,
				Status:  http.StatusOK,
				Value:   fallbackURL,
				Meta:    iconMetadata,
			}
		}

		if errors.Is(err, ErrUnreachableServer) {
			return HttpResponse{
				Status: http.StatusBadRequest,
				Value:  err.Error(),
			}
		}

		return unexpectedError(ctx, err)
	}

	patchedIcon, filled, err := PatchIcon(resolvedIcon)
	if err != nil {
		_ = resolvedIcon.Body.Close()
		return unexpectedError(ctx, err)
	}

	if filled {
		iconMetadata["filled"] = "yes"
	} else {
		iconMetadata["filled"] = "no"
	}

	_ = resolvedIcon.Body.Close()

	buf := new(bytes.Buffer)
	err = png.Encode(buf, patchedIcon)
	if err != nil {
		return unexpectedError(ctx, err)
	}

	_, err = ctx.s3.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      &s3Bucket,
		Key:         &objectKey,
		Body:        buf,
		ContentType: aws.String(resolvedIcon.Type.ContentType()),
		Metadata:    iconMetadata,
		ACL:         "public-read",
	})
	if err != nil {
		return unexpectedError(ctx, err)
	}

	if defaults.CacheStatus == defaults.CacheEnabled {
		ctx.cache.Set(cacheKey, iconMetadata, cache.DefaultExpiration)
	}

	return HttpResponse{
		Success: true,
		Value:   objectURL,
		Meta:    iconMetadata,
	}
}

func Endpoint(ctx Context, handler func(Context, http.ResponseWriter, *http.Request) HttpResponse) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		res := handler(ctx, rw, r)
		elapsed := time.Since(start)
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

		msg := ""

		if !res.Success {
			v, ok := res.Value.(string)
			if ok {
				msg = v
			}
		}

		ctx.log.Info().Str("ip", ip).
			Str("ua", r.Header.Get("User-Agent")).
			Str("method", r.Method).
			Str("path", r.URL.String()).
			Int("status", res.Status).
			Bool("ok", res.Success).
			Str("resp", msg).
			Int64("elapsed", elapsed.Milliseconds()).
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
		log: zerolog.New(os.Stderr).With().Timestamp().Str("version", Version).Logger(),
	}

	http.Handle("/api/v1/resolve/", Endpoint(ctx, GetFaviconEndpoint))

	ctx.log.Debug().Str("cacheStatus", defaults.CacheStatus).Msg("starting server")

	return http.ListenAndServe(port, nil)
}

func main() {
	cacheFlag := flag.Bool("cache", defaults.CacheStatus == defaults.CacheEnabled, "enable caching")

	flag.Parse()

	if *cacheFlag {
		defaults.CacheStatus = defaults.CacheEnabled
	} else {
		defaults.CacheStatus = defaults.CacheDisabled
	}

	err := runHttpServer(":3333")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Cannot start server: %s\n", err)
		os.Exit(1)
	}
}
