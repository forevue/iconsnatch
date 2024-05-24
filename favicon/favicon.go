package favicon

import (
	"errors"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/html"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unsafe"
)

type Icon struct {
	DownloadUrl string
	RawURL      string
	baseURL     string
}

func Resolve(URL string) (string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	offset, err := GetFaviconAuthority(URL)
	if err != nil {
		return "", errors.New("bad url")
	}

	icon := &Icon{
		RawURL:  URL,
		baseURL: URL[:offset],
	}

	res, err := client.Get(icon.baseURL + "/favicon.ico")
	if err != nil {
		return "", errors.New("unreachable server")
	}

	var buf [64]byte
	res.Body.Read(buf[:])
	if hasValidMimeType(buf) {
		return res.Request.URL.String(), nil
	}

	res, err = http.DefaultClient.Get(icon.RawURL)
	if err != nil {
		return "", errors.New("unreachable server")
	}

	htmlTokens := html.NewTokenizer(res.Body)

	baseHref := ""
	iconToTry := ""

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

			}

			if relAttr == "" || hrefAttr == "" || (relAttr != "shortcut icon" && relAttr != "icon") {
				continue
			}

			// We take the first non-svg icon that we find.
			if relAttr == "shortcut icon" {
				iconToTry = hrefAttr

				if typeAttr != "image/svg+xml" {
					break
				}
			} else {
				if typeAttr != "image/svg+xml" {
					iconToTry = hrefAttr
				}

				iconToTry = hrefAttr
			}

		}
	}

	if iconToTry == "" {
		log.Debug().Str("type", "notfound").Str("url", URL).Send()
		return "", nil
	}

	iconHref := ""

	if baseHref != "" {
		iconHref = baseHref + iconToTry
	} else if iconToTry[0] == '/' {
		iconHref = URL[:offset] + iconToTry
	} else if _, err = url.ParseRequestURI(iconToTry); err == nil {
		iconHref = iconToTry
	} else {
		iconHref = strings.TrimRight(res.Request.URL.String(), "/") + "/" + iconToTry
	}

	res, err = http.DefaultClient.Get(iconHref)
	if err != nil {
		return "", errors.New("unreachable server")
	}

	buf = [64]byte{}
	res.Body.Read(buf[:])
	if !hasValidMimeType(buf) {
		return "", nil
	}

	return res.Request.URL.String(), nil
}

func hasValidMimeType(buf [64]byte) bool {
	// ico
	if buf[0] == 0 && buf[1] == 0 && buf[2] == 1 && buf[3] == 0 {
		return true
	}

	str := unsafe.String(unsafe.SliceData(buf[:]), 64)

	// png
	if str[:8] == "\x89\x50\x4E\x47\x0D\x0A\x1A\x0A" {
		return true
	}

	// jpeg
	if str[:13] == "\xFF\xD8\xFF\xFF\xE0\x00\x10\x4A\x46\x49\x46\x00\x01" || str[:4] == "\xFF\xD8\xFF\xEE" || str[:4] == "\xFF\xD8\xFF\xE0" {
		return true
	}

	// bmp
	return str[:2] == "\x42\x4D"
}

func isAlpha(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'A')
}

func GetFaviconAuthority(url string) (int, error) {
	urlLen := len(url)
	schemeIndex := 0

	for ; schemeIndex < urlLen; schemeIndex++ {
		c := url[schemeIndex]
		if isAlpha(c) || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '+' {
			continue
		}

		break
	}

	if schemeIndex == 0 || schemeIndex+3 >= urlLen || !isAlpha(url[0]) || url[schemeIndex:schemeIndex+3] != "://" {
		return 0, errors.New("invalid scheme")
	}

	ptr := schemeIndex + 3

	for ; ptr < urlLen; ptr++ {
		ch := url[ptr]

		// user:password@ is not supported. It's been deprecated for 20 years.
		// it is only accidentally supported.
		if ch == '/' || ch == '?' || ch == '#' {
			break
		}
	}

	return ptr, nil
}
