package main

import (
	"bytes"
	"errors"
	_ "faviconapi/ico"
	"faviconapi/iconpatch"
	"fmt"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unsafe"
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

func FindFaviconURL(URL *url.URL) (*ResolvedIcon, error) {
	baseURL := getBaseURL(URL)

	res, err := doRequest("GET", baseURL+"/favicon.ico", false)
	if err != nil && !errors.Is(err, errRedirectChangedHosts) {
		return nil, ErrUnreachableServer
	}

	var buf [64]byte
	res.Body.Read(buf[:])
	if iconType, ok := hasValidMimeType(buf); ok {
		return &ResolvedIcon{
			URL:  res.Request.URL.String(),
			Type: iconType,
			Body: ReaderCloser(res.Body, bytes.NewReader(buf[:]), res.Body),
		}, nil
	}

	res, err = doRequest("GET", URL.String(), true)
	if err != nil {
		return nil, ErrUnreachableServer
	}

	htmlTokens := html.NewTokenizer(res.Body)

	baseHref := ""
	iconToTry := ""

	var largestSize int64

	for {
		tt := htmlTokens.Next()
		if tt == html.ErrorToken { // includes EOF
			break
		} else if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			t := htmlTokens.Token()

			if t.Data == "base" {
				for _, attr := range t.Attr {
					if attr.Key == "href" {
						baseHref = attr.Val
					}
				}
			} else if t.Data == "body" {
				break
			} else if t.Data != "link" {
				continue
			}

			relAttr := ""
			hrefAttr := ""
			typeAttr := ""
			sizesAttr := ""

			for _, attr := range t.Attr {
				if attr.Key == "rel" {
					relAttr = attr.Val
					continue
				}

				if attr.Key == "href" {
					hrefAttr = attr.Val
					continue
				}

				if attr.Key == "type" {
					typeAttr = attr.Val
					continue
				}

				if attr.Key == "sizes" {
					sizesAttr = attr.Val
					continue
				}
			}

			if (relAttr != "shortcut icon" && relAttr != "icon") || hrefAttr == "" || typeAttr == "image/svg+xml" {
				continue
			}

			if len(hrefAttr) > 4 && hrefAttr[len(hrefAttr)-4:] == ".svg" {
				continue
			}

			if sizesAttr != "" {
				// Guessing it's place
				xOffset := len(sizesAttr) / 2
				if sizesAttr[xOffset] != 'x' {
					goto setIcon
				}

				size, err := strconv.ParseInt(sizesAttr[:xOffset], 10, 64)
				if err != nil {
					// the
					goto setIcon
				}

				if size < largestSize {
					continue
				}
			}

		setIcon:
			// todo: we need to give priority to the last
			// We take the first non-svg icon that we find.
			iconToTry = hrefAttr
		}
	}

	if iconToTry == "" {
		return nil, ErrIconNotFound
	}

	iconHref := ""

	if baseHref != "" {
		iconHref = baseHref + iconToTry
	} else if iconToTry[0] == '/' {
		parsedIconURL, err := url.ParseRequestURI(res.Request.URL.String())
		if err != nil {
			return nil, ErrIconNotFound
		}

		iconHref = getBaseURL(parsedIconURL) + iconToTry
	} else if _, err = url.ParseRequestURI(iconToTry); err == nil {
		iconHref = iconToTry
	} else {
		iconHref = strings.TrimRight(res.Request.URL.String(), "/") + "/" + iconToTry
	}

	res, err = doRequest("GET", iconHref, true)
	if err != nil {
		return nil, ErrUnreachableServer
	}

	buf = [64]byte{}
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

func (hr MultiReaderOneCloser) Read(p []byte) (int, error) {
	return hr.reader.Read(p)
}

func (hr MultiReaderOneCloser) Close() error {
	return hr.closer.Close()
}

func ReaderCloser(closer io.Closer, buf ...io.Reader) io.ReadCloser {
	return MultiReaderOneCloser{
		closer: closer,
		reader: io.MultiReader(buf...),
	}
}

func PatchIcon(resolvedIcon *ResolvedIcon) (*image.NRGBA64, bool, error) {
	icon, _, err := image.Decode(resolvedIcon.Body)
	if err != nil {
		return nil, false, fmt.Errorf("PatchIcon(%d, %s): %w", resolvedIcon.Type, resolvedIcon.URL, err)
	}

	img, filled := iconpatch.Patch(icon)

	return img, filled, nil
}

func getBaseURL(URL *url.URL) string {
	buf := &strings.Builder{}

	buf.WriteString(URL.Scheme)
	buf.WriteString("://")
	buf.WriteString(URL.Host)

	return buf.String()
}

func doRequest(method string, URL string, allowDomainChange bool) (*http.Response, error) {
	parsedURL, err := url.ParseRequestURI(URL)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if allowDomainChange {
				return nil
			}

			from := parsedURL.Hostname()
			to := req.URL.Hostname()

			// The www. domain is not really another domain, in our case.
			if from != to && (to[4:] != from && to[:4] == "www.") {
				return errRedirectChangedHosts
			}

			return nil
		},
	}

	req, err := http.NewRequest(method, URL, nil)
	if err != nil {
		return nil, err
	}

	baseURL := getBaseURL(parsedURL)

	// should bypass most WAFs
	req.Header.Add("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:124.0) Gecko/20100101 Firefox/124.0")
	req.Header.Add("DNT", "1")
	req.Header.Add("Accept", "image/avif,image/webp,*/*")
	req.Header.Add("Cache-Control", "no-cache")
	req.Header.Add("Referer", baseURL)
	req.Header.Add("Origin", baseURL)
	req.Header.Add("Sec-Fetch-Dest", "image")
	req.Header.Add("Sec-Fetch-Mode", "no-cors")
	req.Header.Add("Sec-Fetch-Site", "same-origin")

	return client.Do(req)
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
