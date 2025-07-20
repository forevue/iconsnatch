package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	_ "iconsnatch/ico"
	"iconsnatch/iconpatch"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unsafe"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
)

type IconType byte

func (i IconType) ContentType() string {
	switch i {
	case Ico:
		return "image/x-icon"
	case Png:
		return "image/png"
	case Jpeg:
		return "image/jpeg"
	case Webp:
		return "image/webp"
	case Gif:
		return "image/gif"
	case Bmp:
		return "image/bmp"
	default:
		panic("should not happen")
	}
}

const (
	Ico = 1 + iota
	Png
	Jpeg
	Webp
	Gif
	Bmp
)

var (
	ErrUnreachableServer    = errors.New("unreachable server")
	ErrIconNotFound         = errors.New("icon not found")
	errRedirectChangedHosts = errors.New("bad redirect")
)

type ResolvedIcon struct {
	URL  string
	Type IconType
	Body io.ReadCloser
}

func FindLogoURL(ctx context.Context, URL *url.URL) (*ResolvedIcon, error) {
	baseURL := getBaseURL(URL)

	// Attempt to find favicon.ico at the base URL.
	icoURL := baseURL + "/favicon.ico"
	res, err := doRequest(ctx, "GET", icoURL, false)
	if err != nil && !errors.Is(err, errRedirectChangedHosts) {
		// Do not return; proceed to check HTML.
	} else if err == nil {
		var buf [64]byte
		res.Body.Read(buf[:])
		if iconType, ok := hasValidMimeType(buf); ok {
			return &ResolvedIcon{
				URL:  res.Request.URL.String(),
				Type: iconType,
				Body: ReaderCloser(res.Body, bytes.NewReader(buf[:]), res.Body),
			}, nil
		}
	}

	// If favicon.ico is not found, fetch the HTML page and look for a <link> tag.
	if err != nil {
		return nil, ErrUnreachableServer
	}
	defer res.Body.Close()

	// Parse the HTML to find the icon link.

	htmlTokens := html.NewTokenizer(res.Body)
	baseHref, iconToTry := parseHTMLLink(htmlTokens)
	if iconToTry == "" {
		return nil, ErrIconNotFound
	}

	iconHref := buildIconURL(iconToTry, baseHref, res.Request.URL)
	if iconHref == "" {
		return nil, ErrIconNotFound
	}

	// Fetch the icon specified in the HTML.
	res, err = doRequest(ctx, "GET", iconHref, true)
	if err != nil {
		return nil, ErrUnreachableServer
	}

	var buf [64]byte
	_, _ = res.Body.Read(buf[:])
	iconType, ok := hasValidMimeType(buf)
	if !ok {
		return nil, ErrIconNotFound
	}

	return &ResolvedIcon{
		URL:  res.Request.URL.String(),
		Type: iconType,
		Body: ReaderCloser(res.Body, bytes.NewReader(buf[:]), res.Body),
	}, nil
}

type MultiReaderOneCloser struct {
	closer io.Closer
	reader io.Reader
}

func (hr MultiReaderOneCloser) Read(p []byte) (int, error) { return hr.reader.Read(p) }
func (hr MultiReaderOneCloser) Close() error               { return hr.closer.Close() }

func ReaderCloser(closer io.Closer, readers ...io.Reader) io.ReadCloser {
	return MultiReaderOneCloser{
		closer: closer,
		reader: io.MultiReader(readers...),
	}
}

// RemoveLogoBackground decodes an image, removes a uniform background, and re-encodes it as a PNG.
// It is instrumented with OpenTelemetry tracing.
func RemoveLogoBackground(ctx context.Context, resolvedIcon *ResolvedIcon) (io.ReadCloser, bool, error) {
	icon, _, err := image.Decode(resolvedIcon.Body)
	if err != nil {
		return nil, false, fmt.Errorf("PatchIcon(%d, %s): %w", resolvedIcon.Type, resolvedIcon.URL, err)
	}

	img, filled := iconpatch.Patch(icon)

	buf := new(bytes.Buffer)
	if err := png.Encode(buf, img); err != nil {
		return nil, false, fmt.Errorf("failed to encode patched icon: %w", err)
	}

	return io.NopCloser(buf), filled, nil
}

func getBaseURL(URL *url.URL) string {
	return fmt.Sprintf("%s://%s", URL.Scheme, URL.Host)
}

func doRequest(ctx context.Context, method string, URL string, allowDomainChange bool) (*http.Response, error) {
	<-requestThrottle

	parsedURL, err := url.ParseRequestURI(URL)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Transport: http.DefaultTransport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if allowDomainChange {
				return nil
			}
			from := parsedURL.Hostname()
			to := req.URL.Hostname()
			if from != to && !strings.HasPrefix(to, "www."+from) {
				return errRedirectChangedHosts
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, method, URL, nil)
	if err != nil {
		return nil, err
	}

	baseURL := getBaseURL(parsedURL)
	req.Header.Add("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:124.0) Gecko/20100101 Firefox/124.0")
	req.Header.Add("Accept", "image/avif,image/webp,*/*")
	req.Header.Add("Referer", baseURL)
	req.Header.Add("Origin", baseURL)

	res, err := client.Do(req)
	return res, err
}

func hasValidMimeType(buf [64]byte) (IconType, bool) {
	// ico
	// layout for future reference since this the way
	// we handle them will probably change a bit
	// 0 0 1 0 @4
	//     ^^^ image type (1 is icon, else we don't care)
	//        1 0 @6
	//        ^^^ number of images in a file (2 bytes)
	//			  16 16 @8 (0 means 256 pixels for each)
	// 			  ^^^^^ width x height
	//                  37 @9 (0 means 256 colors)
	//                  ^^ color count
	//                     0 @10
	//                     ^ reserved bit
	// 					     1 0 @12
	//						 ^^^ color planes (0 or 1 for icon format)
	//    						 1 0 @14
	//						     ^^^ bits per pixels
	//  						     0 0 0 0 @18
	//            					 ^^^^^^^ size of the bitmap data in bytes
	//                                       0 0 0 0 @22
	// 										 ^^^^^^^ offset in the file
	if buf[0] == 0 && buf[1] == 0 && buf[2] == 1 && buf[3] == 0 {
		if buf[8] == 2 { // only two colors? probably a placeholder image
			// this may return some false positives
			return 0, false
		}

		return Ico, true
	}

	str := unsafe.String(unsafe.SliceData(buf[:]), 64)

	// png
	if str[:8] == "\x89\x50\x4E\x47\x0D\x0A\x1A\x0A" {
		return Png, true
	}

	// jpeg
	if str[:9] == "\xFF\xD8\xFF\xFF\xE0\x00\x10\x4A\x46" || str[:4] == "\x49\x46\x00\x01" || str[:4] == "\xFF\xD8\xFF\xEE" || str[:4] == "\xFF\xD8\xFF\xE0" {
		return Jpeg, true
	}

	// jpeg 1?
	if str[:4] == "\xFF\xD8\xFF\xE1" && str[6:12] == "\x45\x78\x69\x66\x00\x00" {
		return Jpeg, true
	}

	// webp
	if str[:4] == "\x52\x49\x46\x46" && str[8:12] == "\x57\x45\x42\x50" {
		return Webp, true
	}

	// gif
	if str[:6] == "\x47\x49\x46\x38\x37\x61" || str[:6] == "\x47\x49\x46\x38\x39\x61" {
		return Gif, true
	}

	// bmp
	if str[:2] == "\x42\x4D" {
		return Bmp, true
	}

	return 0, false
}

func parseHTMLLink(htmlTokens *html.Tokenizer) (baseHref, iconToTry string) {
	var largestSize int64

	for {
		tt := htmlTokens.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			t := htmlTokens.Token()
			switch t.Data {
			case "base":
				for _, attr := range t.Attr {
					if attr.Key == "href" {
						baseHref = attr.Val
					}
				}
			case "body":
				return
			case "link":
				var rel, href, typ, sizes string
				for _, attr := range t.Attr {
					switch attr.Key {
					case "rel":
						rel = attr.Val
					case "href":
						href = attr.Val
					case "type":
						typ = attr.Val
					case "sizes":
						sizes = attr.Val
					}
				}

				if (rel != "shortcut icon" && rel != "icon") || href == "" || typ == "image/svg+xml" || strings.HasSuffix(href, ".svg") {
					continue
				}

				var currentSize int64
				if sizes != "" && sizes != "any" {
					if xOffset := strings.Index(sizes, "x"); xOffset > 0 {
						size, err := strconv.ParseInt(sizes[:xOffset], 10, 64)
						if err == nil {
							currentSize = size
						}
					}
				}

				if iconToTry == "" || currentSize > largestSize {
					largestSize = currentSize
					iconToTry = href
				}
			}
		}
	}
	return
}

func buildIconURL(iconToTry, baseHref string, requestURL *url.URL) string {
	if strings.HasPrefix(iconToTry, "http") {
		return iconToTry
	}

	if strings.HasPrefix(iconToTry, "data:") {
		return "" // Not supported
	}

	base, err := url.Parse(getBaseURL(requestURL))
	if err != nil {
		return ""
	}

	if baseHref != "" {
		if hrefBase, err := url.Parse(baseHref); err == nil {
			base = base.ResolveReference(hrefBase)
		}
	}

	iconURL, err := url.Parse(iconToTry)
	if err != nil {
		return ""
	}

	return base.ResolveReference(iconURL).String()
}
