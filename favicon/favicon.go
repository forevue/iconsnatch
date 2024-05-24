package favicon

import (
	"errors"
	"fmt"
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

var ErrRedirectChangedHosts = errors.New("bad redirect")

func DoRequest(method string, URL string, allowDomainChange bool) (*http.Response, error) {
	req, err := http.NewRequest(method, URL, nil)
	if err != nil {
		return nil, err
	}

	authority, err := GetFaviconAuthority(URL)
	if err != nil {
		return nil, err // should not happen
	}

	if allowDomainChange {
		client.CheckRedirect = nil
	} else {
		u, _ := url.ParseRequestURI(URL)
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			from := u.Hostname()
			to := req.URL.Hostname()

			// When we don't want a domain change, we don't mean
			// from .x.x to www.x.x, because that's still the same
			// entity. Preventing redirects from other domains is
			// useful for cases where, e.g., company A acquires B
			// and redirects B.com to A.com
			if from != to && (to[4:] != from && to[:4] == "www.") {
				return ErrRedirectChangedHosts
			}

			return nil
		}
	}

	// should bypass most WAFs
	req.Header.Add("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:124.0) Gecko/20100101 Firefox/124.0")
	req.Header.Add("DNT", "1")
	req.Header.Add("Accept", "image/avif,image/webp,*/*")
	req.Header.Add("Cache-Control", "no-cache")
	req.Header.Add("Referer", URL[:authority])
	req.Header.Add("Origin", URL[:authority])
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

	res, err := DoRequest("GET", icon.baseURL+"/favicon.ico", false)
	if err != nil && !errors.Is(err, ErrRedirectChangedHosts) {
		return "", errors.New("unreachable server")
	}

	var buf [64]byte
	res.Body.Read(buf[:])
	if hasValidMimeType(buf) {
		return res.Request.URL.String(), nil
	}

	res, err = DoRequest("GET", icon.RawURL, true)
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
		iconAuthorityOffset, err := GetFaviconAuthority(res.Request.URL.String())
		if err != nil {
			log.Debug().Str("type", "notfound").Str("url", URL).Send()
			return "", nil
		}

		iconHref = res.Request.URL.String()[:iconAuthorityOffset] + iconToTry
	} else if _, err = url.ParseRequestURI(iconToTry); err == nil {
		iconHref = iconToTry
	} else {
		iconHref = strings.TrimRight(res.Request.URL.String(), "/") + "/" + iconToTry
	}

	res, err = DoRequest("GET", iconHref, true)
	if err != nil {
		return "", errors.New("unreachable server")
	}

	buf = [64]byte{}
	res.Body.Read(buf[:])
	if !hasValidMimeType(buf) {
		fmt.Println(buf)
		return "", nil
	}

	return res.Request.URL.String(), nil
}

func hasValidMimeType(buf [64]byte) bool {
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
			return false
		}

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

	// webp
	if str[:4] == "\x52\x49\x46\x46" && str[8:12] == "\x57\x45\x42\x50" {
		return true
	}

	// gif
	if str[:6] == "\x47\x49\x46\x38\x37\x61" || str[:6] == "\x47\x49\x46\x38\x39\x61" {
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
