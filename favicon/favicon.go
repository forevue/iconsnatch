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

var client = &http.Client{
	Timeout: 5 * time.Second,
}

func DoRequest(method string, url string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	authority, err := GetFaviconAuthority(url)
	if err != nil {
		return nil, err // should not happen
	}

	// should bypass most WAFs
	req.Header.Add("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:124.0) Gecko/20100101 Firefox/124.0")
	req.Header.Add("DNT", "1")
	req.Header.Add("Accept", "image/avif,image/webp,*/*")
	req.Header.Add("Cache-Control", "no-cache")
	req.Header.Add("Referer", url[:authority])
	req.Header.Add("Origin", url[:authority])
	req.Header.Add("Sec-Fetch-Dest", "image")
	req.Header.Add("Sec-Fetch-Mode", "no-cors")
	req.Header.Add("Sec-Fetch-Site", "same-origin")

	return client.Do(req)
}

func Resolve(URL string) (string, error) {

	offset, err := GetFaviconAuthority(URL)
	if err != nil {
		return "", errors.New("bad url")
	}

	icon := &Icon{
		RawURL:  URL,
		baseURL: URL[:offset],
	}

	res, err := DoRequest("GET", icon.baseURL+"/favicon.ico")
	if err != nil {
		return "", errors.New("unreachable server")
	}

	var buf [64]byte
	res.Body.Read(buf[:])
	if hasValidMimeType(buf) {
		return res.Request.URL.String(), nil
	}

	res, err = DoRequest("GET", icon.RawURL)
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
			//typeAttr := ""

			for _, attr := range t.Attr {
				if attr.Key == "rel" {
					relAttr = attr.Val
					continue
				}

				if attr.Key == "href" {
					hrefAttr = attr.Val
					continue
				}

				//if attr.Key == "type" {
				//	typeAttr = attr.Val
				//	continue
				//}

			}

			if relAttr == "" || hrefAttr == "" || (relAttr != "shortcut icon" && relAttr != "icon") {
				continue
			}

			// todo: we need to give priority to the last
			// We take the first non-svg icon that we find.
			iconToTry = hrefAttr
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

	res, err = DoRequest("GET", iconHref)
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
	if str[:9] == "\xFF\xD8\xFF\xFF\xE0\x00\x10\x4A\x46" || str[:4] == "\x49\x46\x00\x01" || str[:4] == "\xFF\xD8\xFF\xEE" || str[:4] == "\xFF\xD8\xFF\xE0" {
		return true
	}

	// jpeg 1?
	if str[:4] == "\xFF\xD8\xFF\xE1" && str[6:12] == "\x45\x78\x69\x66\x00\x00" {
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
